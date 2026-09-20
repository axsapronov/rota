package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alpkeskin/rota/core/internal/checkstats"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/gammazero/workerpool"
	"github.com/jackc/pgx/v5"
)

// hcBatchSize bounds the keyset pagination of bulk health checks: each query
// fetches at most this many proxies, so memory stays flat at any scale.
const hcBatchSize = 1000

// HealthChecker manages proxy health checking
type HealthChecker struct {
	proxyRepo    *repository.ProxyRepository
	settingsRepo *repository.SettingsRepository
	tracker      *UsageTracker
	results      *ResultWriter
	// dedup is the in-flight registry shared with every other check path; a
	// proxy being checked by one job is skipped (not re-checked) by others.
	// Nil falls back to the process-wide registry.
	dedup *CheckDedup
	// checkFn, when set, replaces CheckProxy in bulk runs (test hook for a
	// scripted/counter check).
	checkFn func(ctx context.Context, p *models.Proxy, immediate bool) (*models.ProxyTestResult, error)
	logger  *logger.Logger
	// settingsMu guards settings, which CheckAllProxies rewrites while worker
	// goroutines / CheckProxy read it — periodic and API-triggered checks can
	// otherwise race (AUD-8).
	settingsMu sync.RWMutex
	settings   *models.HealthCheckSettings
}

// getSettings returns the cached health-check settings under the read lock.
func (h *HealthChecker) getSettings() *models.HealthCheckSettings {
	h.settingsMu.RLock()
	defer h.settingsMu.RUnlock()
	return h.settings
}

// setSettings atomically swaps the cached health-check settings (AUD-8).
func (h *HealthChecker) setSettings(settings *models.HealthCheckSettings) {
	h.settingsMu.Lock()
	defer h.settingsMu.Unlock()
	h.settings = settings
}

// loadSettings reads the health-check settings from the repository and caches
// them under the lock (AUD-8).
func (h *HealthChecker) loadSettings(ctx context.Context) (*models.HealthCheckSettings, error) {
	all, err := h.settingsRepo.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %w", err)
	}
	settings := &all.HealthCheck
	h.setSettings(settings)
	return settings, nil
}

// lastCheckTTLCond returns the shared SQL fragment restricting a sweep to
// proxies that were never checked (last_check IS NULL) or whose last check is
// older than ttlMinutes. It returns an empty string when ttlMinutes <= 0,
// disabling the filter. The value is inlined: it is an int from validated
// settings (0-10080), never user input.
func lastCheckTTLCond(ttlMinutes int) string {
	if ttlMinutes <= 0 {
		return ""
	}
	return fmt.Sprintf("(p.last_check IS NULL OR p.last_check < NOW() - (%d * INTERVAL '1 minute'))", ttlMinutes)
}

// proxySelectColumns is the standard 14-column proxy projection shared by the
// bulk health-check queries.
const proxySelectColumns = `
		id, address, protocol, username, password, status,
		requests, successful_requests, failed_requests,
		avg_response_time, last_check, last_error, created_at, updated_at
`

// scanProxyRows scans the proxySelectColumns projection into Proxy models.
func scanProxyRows(rows pgx.Rows) ([]*models.Proxy, error) {
	proxies := make([]*models.Proxy, 0)
	for rows.Next() {
		var p models.Proxy
		if err := rows.Scan(
			&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Password, &p.Status,
			&p.Requests, &p.SuccessfulRequests, &p.FailedRequests,
			&p.AvgResponseTime, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan proxy: %w", err)
		}
		proxies = append(proxies, &p)
	}
	return proxies, rows.Err()
}

// runCheckedBatches streams proxies through load (keyset batches) and checks
// each batch concurrently on a shared worker pool, waiting for a batch to
// finish before fetching the next one. onProgress receives cumulative
// (checked, active, failed) totals after every proxy. A proxy already being
// checked by another in-flight job is skipped (in-flight dedup): no duplicate
// network check, no result, and it does not count toward progress.
// dedupRegistry returns the in-flight registry for this checker (falling back
// to the process-wide one when a test constructs the checker directly).
func (h *HealthChecker) dedupRegistry() *CheckDedup {
	if h.dedup != nil {
		return h.dedup
	}
	return globalCheckDedup
}

func (h *HealthChecker) runCheckedBatches(
	ctx context.Context,
	load func(ctx context.Context, lastID int) ([]*models.Proxy, error),
	onProgress func(checked, active, failed int),
	immediate bool,
	workers int,
	logLabel string,
	jobID string,
) ([]models.ProxyTestResult, int, error) {
	check := h.CheckProxy
	if h.checkFn != nil {
		check = h.checkFn
	}

	wp := workerpool.New(workers)
	defer wp.StopWait()

	// Progress counters are atomic: workers update them lock-free, keeping the
	// per-check mutex off the hot path. The results slice still needs a mutex
	// (append is not atomic).
	var (
		resultsMu sync.Mutex
		results   []models.ProxyTestResult
		checked   atomic.Int64
		active    atomic.Int64
		failed    atomic.Int64
		skipped   atomic.Int64
	)

	lastID := 0
	for {
		batch, err := load(ctx, lastID)
		if err != nil {
			return nil, 0, err
		}
		if len(batch) == 0 {
			break
		}

		var batchWG sync.WaitGroup
		for _, p := range batch {
			batchWG.Add(1)
			p := p
			wp.Submit(func() {
				defer batchWG.Done()
				dedup := h.dedupRegistry()
				acquired, _ := dedup.TryAcquire(p.ID, jobID)
				if !acquired {
					skipped.Add(1)
					return
				}
				defer dedup.Release(p.ID, jobID)
				result, err := check(ctx, p, immediate)
				if err != nil {
					h.logger.Error(logLabel+" error",
						"proxy_id", p.ID,
						"proxy_address", p.Address,
						"error", err,
					)
					errMsg := err.Error()
					resultsMu.Lock()
					results = append(results, models.ProxyTestResult{
						ID: p.ID, Address: p.Address, Status: "failed",
						TestedAt: time.Now(), Error: &errMsg,
					})
					resultsMu.Unlock()
					failed.Add(1)
				} else {
					resultsMu.Lock()
					results = append(results, *result)
					resultsMu.Unlock()
					if result.Status == "active" {
						active.Add(1)
					} else {
						failed.Add(1)
					}
				}
				checked.Add(1)
				if onProgress != nil {
					onProgress(int(checked.Load()), int(active.Load()), int(failed.Load()))
				}
			})
		}

		lastID = batch[len(batch)-1].ID
		batchWG.Wait()

		if len(batch) < hcBatchSize {
			break
		}
	}

	return results, int(skipped.Load()), nil
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(
	proxyRepo *repository.ProxyRepository,
	settingsRepo *repository.SettingsRepository,
	tracker *UsageTracker,
	log *logger.Logger,
) *HealthChecker {
	return &HealthChecker{
		proxyRepo:    proxyRepo,
		settingsRepo: settingsRepo,
		tracker:      tracker,
		results:      NewResultWriter(proxyRepo, log),
		dedup:        globalCheckDedup,
		logger:       log,
	}
}

// ResultWriter exposes the batched check-result writer (for pool sweeps and
// shutdown draining).
func (h *HealthChecker) ResultWriter() *ResultWriter {
	return h.results
}

// CheckProxy tests a single proxy. When immediate is true (user-initiated
// manual test) the result is applied to the proxy status right away; when
// false (periodic/background) the consecutive-failure accounting applies.
func (h *HealthChecker) CheckProxy(ctx context.Context, proxy *models.Proxy, immediate bool) (*models.ProxyTestResult, error) {
	startTime := time.Now()

	// Load settings if not cached (guarded — AUD-8).
	settings := h.getSettings()
	if settings == nil {
		all, err := h.settingsRepo.GetAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load settings: %w", err)
		}
		settings = &all.HealthCheck
		h.setSettings(settings)
	}

	result := &models.ProxyTestResult{
		ID:       proxy.ID,
		Address:  proxy.Address,
		TestedAt: startTime,
	}

	// Create HTTP client with proxy
	transport, err := h.createTransport(proxy)
	if err != nil {
		result.Status = "failed"
		errMsg := fmt.Sprintf("failed to create transport: %v", err)
		result.Error = &errMsg
		h.persistCheckResult(ctx, proxy.ID, false, errMsg, int(time.Since(startTime).Milliseconds()), immediate)
		return result, nil
	}
	// Per-check transports are single-use; close their idle connections when
	// done so they don't linger ~90s each run (AUD-41).
	defer transport.CloseIdleConnections()

	// StrictTLS (default false) keeps the legacy permissive behavior; when
	// enabled, real certificate validation catches proxies that intercept TLS
	// with expired/invalid certs but would otherwise pass the health check.
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	// Go's defaults: minimum TLS 1.2, tracking future Go releases. The shared
	// transport pins TLS 1.0 for legacy proxies, so reset it here.
	transport.TLSClientConfig.MinVersion = 0
	transport.TLSClientConfig.MaxVersion = 0
	transport.TLSClientConfig.CipherSuites = nil

	if settings.StrictTLS {
		transport.TLSClientConfig.InsecureSkipVerify = false
		transport.TLSClientConfig.VerifyPeerCertificate = nil
	} else {
		transport.TLSClientConfig.InsecureSkipVerify = true
		// This callback allows us to accept even unparseable certificates
		transport.TLSClientConfig.VerifyPeerCertificate = func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			// Always return nil to accept any certificate, even malformed ones
			return nil
		}
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(settings.Timeout) * time.Second,
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", settings.URL, nil)
	if err != nil {
		result.Status = "failed"
		errMsg := fmt.Sprintf("failed to create request: %v", err)
		result.Error = &errMsg
		h.persistCheckResult(ctx, proxy.ID, false, errMsg, int(time.Since(startTime).Milliseconds()), immediate)
		return result, nil
	}

	// Add custom headers
	for _, header := range settings.Headers {
		parts := strings.SplitN(header, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			req.Header.Set(key, value)
		}
	}

	// Send request
	resp, err := client.Do(req)
	duration := int(time.Since(startTime).Milliseconds())

	if err != nil {
		result.Status = "failed"
		errMsg := err.Error()

		// Make TLS errors more user-friendly
		if strings.Contains(errMsg, "x509:") || strings.Contains(errMsg, "tls:") {
			if settings.StrictTLS {
				errMsg = fmt.Sprintf("TLS/SSL error: %s", err.Error())
			} else {
				errMsg = fmt.Sprintf("TLS/SSL error: %s (Note: Certificate verification is disabled, but proxy may have issues)", err.Error())
			}
		} else if strings.Contains(errMsg, "timeout") {
			errMsg = fmt.Sprintf("Connection timeout after %ds", settings.Timeout)
		} else if strings.Contains(errMsg, "connection refused") {
			errMsg = "Connection refused - proxy may be offline"
		}

		result.Error = &errMsg

		// Record health check failure
		h.persistCheckResult(ctx, proxy.ID, false, errMsg, duration, immediate)

		return result, nil
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != settings.Status {
		result.Status = "failed"
		errMsg := fmt.Sprintf("unexpected status code: got %d, expected %d", resp.StatusCode, settings.Status)
		result.Error = &errMsg

		// Record health check failure
		h.persistCheckResult(ctx, proxy.ID, false, errMsg, duration, immediate)

		return result, nil
	}

	// Success!
	result.Status = "active"
	result.ResponseTime = &duration

	// Record health check success
	h.persistCheckResult(ctx, proxy.ID, true, "", duration, immediate)

	return result, nil
}

// persistCheckResult enqueues a check result for batched persistence. Manual
// tests (immediate) use the immediate-status semantics; periodic checks use
// the consecutive-failure hysteresis. The enqueue is non-blocking, so a slow
// or dead database never blocks the check path; failed flushes are logged by
// the writer only.
func (h *HealthChecker) persistCheckResult(ctx context.Context, proxyID int, success bool, errMsg string, duration int, immediate bool) {
	// Record the completion synchronously (not inside the async DB write) so the
	// rolling window reflects when checks finish, not when the DB write lands.
	checkstats.Record(success)

	kind := KindPeriodic
	if immediate {
		kind = KindManual
	}
	if h.results != nil {
		h.results.Record(CheckResultRecord{
			ProxyID:   proxyID,
			Success:   success,
			Duration:  duration,
			LastError: errMsg,
			Kind:      kind,
			Timestamp: time.Now(),
		})
	}
}

// CheckAllProxies tests all proxies concurrently with periodic
// (non-immediate) status accounting.
func (h *HealthChecker) CheckAllProxies(ctx context.Context) ([]models.ProxyTestResult, error) {
	return h.CheckAllProxiesWithProgress(ctx, nil, false, "")
}

// CheckAllProxiesWithProgress tests all proxies concurrently, calling
// onProgress(checked, active, failed) after each proxy is checked (nil-safe).
// immediate=true applies each result to the proxy status right away
// (user-initiated); false keeps the consecutive-failure accounting. jobID
// identifies this run in the in-flight dedup registry ("" outside a job).
func (h *HealthChecker) CheckAllProxiesWithProgress(
	ctx context.Context,
	onProgress func(checked, active, failed int),
	immediate bool,
	jobID string,
) ([]models.ProxyTestResult, error) {
	settings, err := h.loadSettings(ctx)
	if err != nil {
		return nil, err
	}

	workers := settings.Workers
	if workers <= 0 {
		workers = 20
		h.logger.Warn("health check workers is non-positive, using fallback",
			"configured_workers", settings.Workers, "fallback_workers", workers)
	}

	h.logger.Info("starting health check", "workers", workers, "batch_size", hcBatchSize)
	startedAt := time.Now()

	// Get only orphan proxies (not attached to any pool) so the global health
	// check does not interfere with pool-level health checks, which are the
	// single source of truth for pool-marked proxies. The TTL filter skips
	// orphans checked more recently than the configured window. Rows stream in
	// keyset batches (id > last, ORDER BY id) so memory stays flat.
	baseQuery := "SELECT " + proxySelectColumns + `
		FROM proxies p
		WHERE NOT EXISTS (
			SELECT 1
			FROM pool_proxies ppm
			WHERE ppm.proxy_id = p.id
		)
	`
	if cond := lastCheckTTLCond(settings.OrphanTTLMinutes); cond != "" {
		baseQuery += "AND " + cond + "\n"
	}
	baseQuery += "AND p.id > $1\nORDER BY p.id\nLIMIT $2"

	results, skipped, err := h.runCheckedBatches(ctx, func(ctx context.Context, lastID int) ([]*models.Proxy, error) {
		rows, err := h.proxyRepo.GetDB().Pool.Query(ctx, baseQuery, lastID, hcBatchSize)
		if err != nil {
			return nil, fmt.Errorf("failed to get proxies: %w", err)
		}
		defer rows.Close()
		return scanProxyRows(rows)
	}, onProgress, immediate, workers, "health check", jobID)
	if err != nil {
		return nil, err
	}
	h.drainResults(ctx)

	h.logger.Info("health check completed",
		"checked", len(results),
		"skipped_inflight", skipped,
		"throughput_per_s", ChecksPerSecond(len(results), time.Since(startedAt)),
		"orphan_ttl_minutes", settings.OrphanTTLMinutes,
	)

	return results, nil
}

// CheckProxiesWithProgress health-checks the given proxy IDs concurrently,
// calling onProgress(checked, active, failed) after each proxy is checked
// (nil-safe). immediate=true applies each result to the proxy status right
// away (user-initiated). IDs that no longer exist are silently skipped. jobID
// identifies this run in the in-flight dedup registry ("" outside a job).
func (h *HealthChecker) CheckProxiesWithProgress(
	ctx context.Context,
	proxyIDs []int,
	onProgress func(checked, active, failed int),
	immediate bool,
	jobID string,
) ([]models.ProxyTestResult, error) {
	if len(proxyIDs) == 0 {
		return []models.ProxyTestResult{}, nil
	}

	// Explicit user-selected IDs: no TTL filter (the user asked for these).
	settings, err := h.loadSettings(ctx)
	if err != nil {
		return nil, err
	}

	workers := settings.Workers
	if workers <= 0 {
		workers = 20
	}

	h.logger.Info("starting bulk proxy health check", "requested", len(proxyIDs), "workers", workers, "batch_size", hcBatchSize)
	startedAt := time.Now()

	query := "SELECT " + proxySelectColumns + `
		FROM proxies
		WHERE id = ANY($1)
		AND id > $2
		ORDER BY id
		LIMIT $3
	`

	results, skipped, err := h.runCheckedBatches(ctx, func(ctx context.Context, lastID int) ([]*models.Proxy, error) {
		rows, err := h.proxyRepo.GetDB().Pool.Query(ctx, query, proxyIDs, lastID, hcBatchSize)
		if err != nil {
			return nil, fmt.Errorf("failed to get proxies: %w", err)
		}
		defer rows.Close()
		return scanProxyRows(rows)
	}, onProgress, immediate, workers, "bulk health check", jobID)
	if err != nil {
		return nil, err
	}
	h.drainResults(ctx)

	h.logger.Info("bulk proxy health check completed",
		"checked", len(results),
		"skipped_inflight", skipped,
		"throughput_per_s", ChecksPerSecond(len(results), time.Since(startedAt)),
	)

	return results, nil
}

// CountOrphanProxies returns the number of proxies not attached to any pool,
// applying the orphan TTL filter (same window as the sweep, so a job's Total
// equals the number of proxies it will actually check).
func (h *HealthChecker) CountOrphanProxies(ctx context.Context) (int, error) {
	settings, err := h.loadSettings(ctx)
	if err != nil {
		return 0, err
	}
	query := `
		SELECT COUNT(*)
		FROM proxies p
		WHERE NOT EXISTS (
			SELECT 1
			FROM pool_proxies ppm
			WHERE ppm.proxy_id = p.id
		)
	`
	if cond := lastCheckTTLCond(settings.OrphanTTLMinutes); cond != "" {
		query += "AND " + cond
	}
	var total int
	if err := h.proxyRepo.GetDB().Pool.QueryRow(ctx, query).Scan(&total); err != nil {
		return 0, fmt.Errorf("failed to count orphan proxies: %w", err)
	}
	return total, nil
}

// listOrphanIdleBatch loads one keyset batch (id > lastID, ORDER BY id LIMIT
// hcBatchSize) of orphan proxies with status idle, applying the idle TTL
// filter.
func (h *HealthChecker) listOrphanIdleBatch(ctx context.Context, lastID int) ([]*models.Proxy, error) {
	settings, err := h.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	query := "SELECT " + proxySelectColumns + `
		FROM proxies p
		WHERE p.status = 'idle'
		AND NOT EXISTS (
			SELECT 1
			FROM pool_proxies ppm
			WHERE ppm.proxy_id = p.id
		)
	`
	if cond := lastCheckTTLCond(settings.IdleTTLMinutes); cond != "" {
		query += "AND " + cond + "\n"
	}
	query += "AND p.id > $1\nORDER BY p.id\nLIMIT $2"
	rows, err := h.proxyRepo.GetDB().Pool.Query(ctx, query, lastID, hcBatchSize)
	if err != nil {
		return nil, fmt.Errorf("failed to get orphan idle proxies: %w", err)
	}
	defer rows.Close()
	return scanProxyRows(rows)
}

// CountOrphanIdleProxies returns the number of orphan proxies with status
// idle, applying the idle TTL filter (same window as the sweep).
func (h *HealthChecker) CountOrphanIdleProxies(ctx context.Context) (int, error) {
	settings, err := h.loadSettings(ctx)
	if err != nil {
		return 0, err
	}
	query := `
		SELECT COUNT(*)
		FROM proxies p
		WHERE p.status = 'idle'
		AND NOT EXISTS (
			SELECT 1
			FROM pool_proxies ppm
			WHERE ppm.proxy_id = p.id
		)
	`
	if cond := lastCheckTTLCond(settings.IdleTTLMinutes); cond != "" {
		query += "AND " + cond
	}
	var total int
	if err := h.proxyRepo.GetDB().Pool.QueryRow(ctx, query).Scan(&total); err != nil {
		return 0, fmt.Errorf("failed to count orphan idle proxies: %w", err)
	}
	return total, nil
}

// CheckOrphanIdleProxiesWithProgress tests orphan proxies with status idle,
// reporting progress after each proxy. immediate follows the same semantics as
// CheckAllProxiesWithProgress. jobID identifies this run in the in-flight
// dedup registry ("" outside a job).
func (h *HealthChecker) CheckOrphanIdleProxiesWithProgress(
	ctx context.Context,
	onProgress func(checked, active, failed int),
	immediate bool,
	jobID string,
) ([]models.ProxyTestResult, error) {
	settings, err := h.loadSettings(ctx)
	if err != nil {
		return nil, err
	}

	workers := settings.Workers
	if workers <= 0 {
		workers = 20
		h.logger.Warn("health check workers is non-positive, using fallback",
			"configured_workers", settings.Workers, "fallback_workers", workers)
	}

	h.logger.Info("starting orphan idle health check", "workers", workers, "batch_size", hcBatchSize)
	startedAt := time.Now()

	results, skipped, err := h.runCheckedBatches(ctx, h.listOrphanIdleBatch, onProgress, immediate, workers,
		"orphan idle health check", jobID)
	if err != nil {
		return nil, err
	}
	h.drainResults(ctx)

	h.logger.Info("orphan idle health check completed",
		"checked", len(results),
		"skipped_inflight", skipped,
		"throughput_per_s", ChecksPerSecond(len(results), time.Since(startedAt)),
		"idle_ttl_minutes", settings.IdleTTLMinutes,
	)

	return results, nil
}

// drainResults flushes buffered check results before a bulk run finishes, so
// a job's finish state reflects every result it produced.
func (h *HealthChecker) drainResults(ctx context.Context) {
	if h.results != nil {
		h.results.Drain(ctx)
	}
}

// ChecksPerSecond returns a coarse checks/s throughput figure for job logs.
func ChecksPerSecond(checked int, elapsed time.Duration) int {
	if checked <= 0 || elapsed <= 0 {
		return 0
	}
	return int(float64(checked) / elapsed.Seconds())
}

// createTransport creates an HTTP transport for the proxy
func (h *HealthChecker) createTransport(p *models.Proxy) (*http.Transport, error) {
	// Use shared transport creation utility
	return CreateProxyTransport(p)
}

// StartPeriodicHealthCheck starts a background health check routine
func (h *HealthChecker) StartPeriodicHealthCheck(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	h.logger.Info("starting periodic health check", "interval", interval)

	for {
		select {
		case <-ticker.C:
			h.logger.Info("running periodic health check")
			_, err := h.CheckAllProxies(ctx)
			if err != nil {
				h.logger.Error("periodic health check failed", "error", err)
			}
		case <-ctx.Done():
			h.logger.Info("stopping periodic health check")
			return
		}
	}
}
