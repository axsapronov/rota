package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/checkstats"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/gammazero/workerpool"
)

// HealthChecker manages proxy health checking
type HealthChecker struct {
	proxyRepo    *repository.ProxyRepository
	settingsRepo *repository.SettingsRepository
	tracker      *UsageTracker
	logger       *logger.Logger
	settings     *models.HealthCheckSettings
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
		logger:       log,
	}
}

// CheckProxy tests a single proxy.
// When immediate is true (manual test), proxy status in the DB is updated right away.
func (h *HealthChecker) CheckProxy(ctx context.Context, proxy *models.Proxy, immediate bool) (*models.ProxyTestResult, error) {
	startTime := time.Now()

	// Load settings if not cached
	if h.settings == nil {
		settings, err := h.settingsRepo.GetAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load settings: %w", err)
		}
		h.settings = &settings.HealthCheck
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
		h.persistCheckResult(ctx, proxy.ID, false, errMsg, immediate)
		return result, nil
	}

	// Override TLS config for health checks to be maximally permissive
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.InsecureSkipVerify = true
	transport.TLSClientConfig.MinVersion = 0 // Allow all TLS versions including SSLv3
	transport.TLSClientConfig.MaxVersion = 0 // No maximum version restriction
	transport.TLSClientConfig.CipherSuites = nil // Accept all cipher suites
	// This callback allows us to accept even unparseable certificates
	transport.TLSClientConfig.VerifyPeerCertificate = func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		// Always return nil to accept any certificate, even malformed ones
		return nil
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(h.settings.Timeout) * time.Second,
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", h.settings.URL, nil)
	if err != nil {
		result.Status = "failed"
		errMsg := fmt.Sprintf("failed to create request: %v", err)
		result.Error = &errMsg
		h.persistCheckResult(ctx, proxy.ID, false, errMsg, immediate)
		return result, nil
	}

	// Add custom headers
	for _, header := range h.settings.Headers {
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
			errMsg = fmt.Sprintf("TLS/SSL error: %s (Note: Certificate verification is disabled, but proxy may have issues)", err.Error())
		} else if strings.Contains(errMsg, "timeout") {
			errMsg = fmt.Sprintf("Connection timeout after %ds", h.settings.Timeout)
		} else if strings.Contains(errMsg, "connection refused") {
			errMsg = "Connection refused - proxy may be offline"
		}

		result.Error = &errMsg
		h.persistCheckResult(ctx, proxy.ID, false, errMsg, immediate)

		return result, nil
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != h.settings.Status {
		result.Status = "failed"
		errMsg := fmt.Sprintf("unexpected status code: got %d, expected %d", resp.StatusCode, h.settings.Status)
		result.Error = &errMsg
		h.persistCheckResult(ctx, proxy.ID, false, errMsg, immediate)

		return result, nil
	}

	// Success!
	result.Status = "active"
	result.ResponseTime = &duration
	h.persistCheckResult(ctx, proxy.ID, true, "", immediate)

	return result, nil
}

func (h *HealthChecker) persistCheckResult(ctx context.Context, proxyID int, success bool, errMsg string, immediate bool) {
	var err error
	if immediate {
		err = h.tracker.RecordManualTestResult(ctx, proxyID, success, errMsg)
	} else {
		err = h.tracker.RecordHealthCheck(ctx, proxyID, success, errMsg)
	}
	if err != nil {
		h.logger.Error("failed to persist proxy check result",
			"proxy_id", proxyID,
			"success", success,
			"immediate", immediate,
			"error", err,
		)
	}
	checkstats.Record(success)
}

// CheckAllProxies tests orphan proxies (not attached to any pool) concurrently.
func (h *HealthChecker) CheckAllProxies(ctx context.Context) ([]models.ProxyTestResult, error) {
	return h.CheckAllProxiesWithProgress(ctx, nil)
}

// CountOrphanProxies returns amount of proxies not attached to any pool.
func (h *HealthChecker) CountOrphanProxies(ctx context.Context) (int, error) {
	query := `
		SELECT COUNT(*)
		FROM proxies p
		WHERE NOT EXISTS (
			SELECT 1
			FROM pool_proxies ppm
			WHERE ppm.proxy_id = p.id
		)
	`
	var total int
	if err := h.proxyRepo.GetDB().Pool.QueryRow(ctx, query).Scan(&total); err != nil {
		return 0, fmt.Errorf("failed to count orphan proxies: %w", err)
	}
	return total, nil
}

// CountOrphanIdleProxies returns orphan proxies with status idle (not in any pool).
func (h *HealthChecker) CountOrphanIdleProxies(ctx context.Context) (int, error) {
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
	var total int
	if err := h.proxyRepo.GetDB().Pool.QueryRow(ctx, query).Scan(&total); err != nil {
		return 0, fmt.Errorf("failed to count orphan idle proxies: %w", err)
	}
	return total, nil
}

// ListOrphanIdleProxies loads orphan proxies with status idle.
func (h *HealthChecker) ListOrphanIdleProxies(ctx context.Context) ([]*models.Proxy, error) {
	query := `
		SELECT
			id, address, protocol, username, password, status,
			requests, successful_requests, failed_requests,
			avg_response_time, last_check, last_error, created_at, updated_at
		FROM proxies p
		WHERE p.status = 'idle'
		AND NOT EXISTS (
			SELECT 1
			FROM pool_proxies ppm
			WHERE ppm.proxy_id = p.id
		)
		ORDER BY address
	`

	rows, err := h.proxyRepo.GetDB().Pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get orphan idle proxies: %w", err)
	}
	defer rows.Close()

	proxies := make([]*models.Proxy, 0)
	for rows.Next() {
		var p models.Proxy
		err := rows.Scan(
			&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Password, &p.Status,
			&p.Requests, &p.SuccessfulRequests, &p.FailedRequests,
			&p.AvgResponseTime, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan proxy: %w", err)
		}
		proxies = append(proxies, &p)
	}
	return proxies, nil
}

// CheckOrphanIdleProxiesWithProgress tests orphan idle proxies and reports progress.
func (h *HealthChecker) CheckOrphanIdleProxiesWithProgress(
	ctx context.Context,
	onProgress func(checked, active, failed int),
) ([]models.ProxyTestResult, error) {
	settings, err := h.settingsRepo.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %w", err)
	}
	h.settings = &settings.HealthCheck

	proxies, err := h.ListOrphanIdleProxies(ctx)
	if err != nil {
		return nil, err
	}

	if len(proxies) == 0 {
		return []models.ProxyTestResult{}, nil
	}

	h.logger.Info("starting orphan idle health check", "proxy_count", len(proxies), "workers", h.settings.Workers)

	workers := h.settings.Workers
	if workers <= 0 {
		workers = 20
		h.logger.Warn("health check workers is non-positive, using fallback", "configured_workers", h.settings.Workers, "fallback_workers", workers)
	}

	wp := workerpool.New(workers)
	results := make([]models.ProxyTestResult, len(proxies))
	var statsMu sync.Mutex
	checked := 0
	active := 0
	failed := 0

	for i, proxy := range proxies {
		idx := i
		p := proxy
		wp.Submit(func() {
			result, err := h.CheckProxy(ctx, p, true)
			statsMu.Lock()
			defer statsMu.Unlock()
			if err != nil {
				h.logger.Error("orphan idle health check error",
					"proxy_id", p.ID,
					"proxy_address", p.Address,
					"error", err,
				)
				results[idx] = models.ProxyTestResult{
					ID:       p.ID,
					Address:  p.Address,
					Status:   "failed",
					TestedAt: time.Now(),
				}
				errMsg := err.Error()
				results[idx].Error = &errMsg
				failed++
			} else {
				results[idx] = *result
				if result.Status == "active" {
					active++
				} else {
					failed++
				}
			}
			checked++
			if onProgress != nil {
				onProgress(checked, active, failed)
			}
		})
	}

	wp.StopWait()

	h.logger.Info("orphan idle health check completed", "proxy_count", len(proxies))

	return results, nil
}

// CheckAllProxiesWithProgress tests orphan proxies and reports progress.
func (h *HealthChecker) CheckAllProxiesWithProgress(
	ctx context.Context,
	onProgress func(checked, active, failed int),
) ([]models.ProxyTestResult, error) {
	// Load settings
	settings, err := h.settingsRepo.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load settings: %w", err)
	}
	h.settings = &settings.HealthCheck

	// Get only orphan proxies (not assigned to any pool) to avoid
	// interfering with pool-level health checks.
	query := `
		SELECT
			id, address, protocol, username, password, status,
			requests, successful_requests, failed_requests,
			avg_response_time, last_check, last_error, created_at, updated_at
		FROM proxies p
		WHERE NOT EXISTS (
			SELECT 1
			FROM pool_proxies ppm
			WHERE ppm.proxy_id = p.id
		)
		ORDER BY address
	`

	rows, err := h.proxyRepo.GetDB().Pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxies: %w", err)
	}
	defer rows.Close()

	proxies := make([]*models.Proxy, 0)
	for rows.Next() {
		var p models.Proxy
		err := rows.Scan(
			&p.ID, &p.Address, &p.Protocol, &p.Username, &p.Password, &p.Status,
			&p.Requests, &p.SuccessfulRequests, &p.FailedRequests,
			&p.AvgResponseTime, &p.LastCheck, &p.LastError, &p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan proxy: %w", err)
		}
		proxies = append(proxies, &p)
	}

	if len(proxies) == 0 {
		return []models.ProxyTestResult{}, nil
	}

	h.logger.Info("starting health check", "proxy_count", len(proxies), "workers", h.settings.Workers)

	workers := h.settings.Workers
	if workers <= 0 {
		workers = 20
		h.logger.Warn("health check workers is non-positive, using fallback", "configured_workers", h.settings.Workers, "fallback_workers", workers)
	}

	// Create worker pool
	wp := workerpool.New(workers)
	results := make([]models.ProxyTestResult, len(proxies))
	var statsMu sync.Mutex
	checked := 0
	active := 0
	failed := 0

	// Submit jobs
	for i, proxy := range proxies {
		idx := i
		p := proxy
		wp.Submit(func() {
			result, err := h.CheckProxy(ctx, p, true)
			statsMu.Lock()
			defer statsMu.Unlock()
			if err != nil {
				h.logger.Error("health check error",
					"proxy_id", p.ID,
					"proxy_address", p.Address,
					"error", err,
				)
				results[idx] = models.ProxyTestResult{
					ID:       p.ID,
					Address:  p.Address,
					Status:   "failed",
					TestedAt: time.Now(),
				}
				errMsg := err.Error()
				results[idx].Error = &errMsg
				failed++
			} else {
				results[idx] = *result
				if result.Status == "active" {
					active++
				} else {
					failed++
				}
			}
			checked++
			if onProgress != nil {
				onProgress(checked, active, failed)
			}
		})
	}

	// Wait for all jobs to complete
	wp.StopWait()

	h.logger.Info("health check completed", "proxy_count", len(proxies))

	return results, nil
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
