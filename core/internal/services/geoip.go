package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/config"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"golang.org/x/time/rate"
)

const defaultGeoIPBatchURL = "http://ip-api.com/batch"

// ipAPIResponse is the response from ip-api.com batch endpoint
type ipAPIResponse struct {
	Status      string  `json:"status"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	Region      string  `json:"regionName"`
	City        string  `json:"city"`
	ISP         string  `json:"isp"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Query       string  `json:"query"`
}

type cacheEntry struct {
	geo      models.GeoInfo
	cachedAt time.Time
}

// GeoIPService performs IP geolocation lookups via ip-api.com (free, no key needed).
// Free tier: ~45 lookups/min; each IP in a batch counts as one lookup.
type GeoIPService struct {
	client            *http.Client
	batchURL          string
	cache             map[string]cacheEntry
	failUntil         map[string]time.Time
	mu                sync.RWMutex
	logger            *logger.Logger
	cacheTTL          time.Duration
	negativeTTL       time.Duration
	batchMax          int
	maxRetries        int
	queriesPerMinute  int
	limiter           *rate.Limiter
	metrics           *geoMetrics
}

// NewGeoIPService creates a new GeoIPService.
func NewGeoIPService(log *logger.Logger, cfg config.GeoIPConfig) *GeoIPService {
	burst := cfg.BatchMax
	if burst > cfg.QueriesPerMinute {
		burst = cfg.QueriesPerMinute
	}
	return &GeoIPService{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		batchURL:         defaultGeoIPBatchURL,
		cache:            make(map[string]cacheEntry),
		failUntil:        make(map[string]time.Time),
		logger:           log,
		cacheTTL:         time.Duration(cfg.CacheTTLHours) * time.Hour,
		negativeTTL:      time.Duration(cfg.NegativeCacheMinutes) * time.Minute,
		batchMax:         cfg.BatchMax,
		maxRetries:       cfg.MaxRetries,
		queriesPerMinute: cfg.QueriesPerMinute,
		limiter: rate.NewLimiter(
			rate.Limit(float64(cfg.QueriesPerMinute)/60.0),
			burst,
		),
		metrics: newGeoMetrics(),
	}
}

// MetricsSnapshot returns a compact GeoIP metrics view for the dashboard.
func (g *GeoIPService) MetricsSnapshot(queuePending int) GeoIPMetricsSnapshot {
	now := time.Now()
	lookups1m := g.metrics.sumSince(now.Add(-time.Minute), func(s geoSample) int { return s.lookups })
	processed10m := g.metrics.sumSince(now.Add(-10*time.Minute), func(s geoSample) int { return s.processed })

	usage := 0.0
	if g.queriesPerMinute > 0 {
		usage = float64(lookups1m) / float64(g.queriesPerMinute) * 100
		if usage > 100 {
			usage = 100
		}
	}

	return GeoIPMetricsSnapshot{
		QueuePending:      queuePending,
		ProcessedLast10m:  processed10m,
		LookupsLastMinute: lookups1m,
		QueriesPerMinute:  g.queriesPerMinute,
		UsagePercent1m:    usage,
	}
}

func (g *GeoIPService) recordProcessed(n int) {
	if n > 0 {
		g.metrics.record(geoSample{processed: n})
	}
}

// newGeoIPServiceForTest is used by unit tests to point at httptest.Server.
func newGeoIPServiceForTest(log *logger.Logger, cfg config.GeoIPConfig, batchURL string) *GeoIPService {
	g := NewGeoIPService(log, cfg)
	g.batchURL = batchURL
	return g
}

// extractIP parses "host:port" and returns just the host IP.
func extractIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return strings.TrimSpace(address)
	}
	return strings.TrimSpace(host)
}

func (g *GeoIPService) isCachedLocked(ip string, now time.Time) (models.GeoInfo, bool) {
	if entry, ok := g.cache[ip]; ok && now.Sub(entry.cachedAt) < g.cacheTTL {
		return entry.geo, true
	}
	return models.GeoInfo{}, false
}

func (g *GeoIPService) isNegativeLocked(ip string, now time.Time) bool {
	if until, ok := g.failUntil[ip]; ok && now.Before(until) {
		return true
	}
	return false
}

func (g *GeoIPService) markFailed(ips []string) {
	if g.negativeTTL <= 0 || len(ips) == 0 {
		return
	}
	until := time.Now().Add(g.negativeTTL)
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, ip := range ips {
		g.failUntil[ip] = until
	}
}

// LookupOne returns GeoInfo for a single proxy address ("host:port" or bare IP).
func (g *GeoIPService) LookupOne(ctx context.Context, address string) (*models.GeoInfo, error) {
	ip := extractIP(address)
	if ip == "" {
		return nil, fmt.Errorf("empty address")
	}

	now := time.Now()
	g.mu.RLock()
	if geo, ok := g.isCachedLocked(ip, now); ok {
		g.mu.RUnlock()
		g.mu.RUnlock()
		return &geo, nil
	}
	if g.isNegativeLocked(ip, now) {
		g.mu.RUnlock()
		return nil, fmt.Errorf("geoip lookup skipped for %s (recent failure)", ip)
	}
	g.mu.RUnlock()

	raw, err := g.resolveIPs(ctx, []string{ip})
	if err != nil {
		return nil, err
	}
	geo, ok := raw[ip]
	if !ok {
		return nil, fmt.Errorf("no result for %s", ip)
	}
	return &geo, nil
}

// LookupBatch resolves GeoInfo for addresses. Returns map[address] -> GeoInfo.
func (g *GeoIPService) LookupBatch(ctx context.Context, addresses []string) map[string]models.GeoInfo {
	result := make(map[string]models.GeoInfo)
	ipToAddr := make(map[string]string)
	var needed []string

	now := time.Now()
	g.mu.RLock()
	for _, addr := range addresses {
		ip := extractIP(addr)
		if ip == "" {
			continue
		}
		ipToAddr[ip] = addr
		if geo, ok := g.isCachedLocked(ip, now); ok {
			result[addr] = geo
			continue
		}
		if g.isNegativeLocked(ip, now) {
			continue
		}
		needed = append(needed, ip)
	}
	g.mu.RUnlock()

	if len(needed) == 0 {
		return result
	}

	raw, err := g.resolveIPs(ctx, needed)
	if err != nil {
		g.logger.Warn("geoip batch lookup failed", "error", err)
		return result
	}
	for ip, geo := range raw {
		if addr, ok := ipToAddr[ip]; ok {
			result[addr] = geo
		}
	}
	return result
}

// EnrichProxies calls ip-api.com for all addresses and returns map[address]->GeoInfo.
func (g *GeoIPService) EnrichProxies(ctx context.Context, addresses []string) map[string]models.GeoInfo {
	if len(addresses) == 0 {
		return nil
	}

	ipToAddr := make(map[string]string)
	for _, addr := range addresses {
		ip := extractIP(addr)
		if ip != "" {
			ipToAddr[ip] = addr
		}
	}

	ips := make([]string, 0, len(ipToAddr))
	for ip := range ipToAddr {
		ips = append(ips, ip)
	}

	result := make(map[string]models.GeoInfo)
	now := time.Now()

	var needed []string
	g.mu.RLock()
	for _, ip := range ips {
		if geo, ok := g.isCachedLocked(ip, now); ok {
			if addr, ok2 := ipToAddr[ip]; ok2 {
				result[addr] = geo
			}
			continue
		}
		if g.isNegativeLocked(ip, now) {
			continue
		}
		needed = append(needed, ip)
	}
	g.mu.RUnlock()

	if len(needed) == 0 {
		return result
	}

	raw, err := g.resolveIPs(ctx, needed)
	if err != nil {
		g.logger.Warn("geoip enrichment failed", "error", err, "ips", len(needed))
		return result
	}
	for ip, geo := range raw {
		if addr, ok := ipToAddr[ip]; ok {
			result[addr] = geo
		}
	}
	return result
}

// resolveIPs fetches geo data for IPs not in cache/negative cache, respecting rate limits.
func (g *GeoIPService) resolveIPs(ctx context.Context, ips []string) (map[string]models.GeoInfo, error) {
	if len(ips) == 0 {
		return nil, nil
	}

	out := make(map[string]models.GeoInfo)
	for i := 0; i < len(ips); i += g.batchMax {
		end := i + g.batchMax
		if end > len(ips) {
			end = len(ips)
		}
		batch := ips[i:end]

		if err := g.limiter.WaitN(ctx, len(batch)); err != nil {
			return out, err
		}

		g.metrics.record(geoSample{lookups: len(batch)})

		part, err := g.lookupBatchRaw(ctx, batch)
		if err != nil {
			g.markFailed(batch)
			return out, err
		}
		for ip, geo := range part {
			out[ip] = geo
		}
	}
	return out, nil
}

// lookupBatchRaw fetches geo data and returns map[ip] -> GeoInfo.
func (g *GeoIPService) lookupBatchRaw(ctx context.Context, ips []string) (map[string]models.GeoInfo, error) {
	if len(ips) == 0 {
		return nil, nil
	}

	responses, err := g.doBatchRequest(ctx, ips)
	if err != nil {
		return nil, err
	}

	result := make(map[string]models.GeoInfo, len(responses))
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range responses {
		if r.Status != "success" {
			continue
		}
		geo := models.GeoInfo{
			CountryCode: r.CountryCode,
			CountryName: r.Country,
			RegionName:  r.Region,
			CityName:    r.City,
			ISP:         r.ISP,
			Latitude:    r.Lat,
			Longitude:   r.Lon,
		}
		result[r.Query] = geo
		g.cache[r.Query] = cacheEntry{geo: geo, cachedAt: now}
		delete(g.failUntil, r.Query)
	}
	return result, nil
}

func (g *GeoIPService) doBatchRequest(ctx context.Context, ips []string) ([]ipAPIResponse, error) {
	type reqItem struct {
		Query  string `json:"query"`
		Fields string `json:"fields"`
	}
	items := make([]reqItem, len(ips))
	fields := "status,country,countryCode,regionName,city,isp,lat,lon,query"
	for i, ip := range ips {
		items[i] = reqItem{Query: ip, Fields: fields}
	}

	body, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal geoip request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= g.maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt, lastErr)
			g.logger.Warn("geoip retry",
				"attempt", attempt,
				"ips", len(ips),
				"delay", delay.String(),
				"error", lastErr,
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.batchURL, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := g.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("geoip request failed: %w", err)
			if attempt < g.maxRetries {
				continue
			}
			return nil, lastErr
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("geoip read body: %w", readErr)
			if attempt < g.maxRetries {
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode == http.StatusOK {
			var responses []ipAPIResponse
			if err := json.Unmarshal(respBody, &responses); err != nil {
				return nil, fmt.Errorf("failed to decode geoip response: %w", err)
			}
			return responses, nil
		}

		lastErr = fmt.Errorf("geoip api returned %d", resp.StatusCode)
		if !isRetryableStatus(resp.StatusCode) || attempt >= g.maxRetries {
			return nil, lastErr
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if sec, parseErr := strconv.Atoi(ra); parseErr == nil && sec > 0 {
					lastErr = &retryAfterError{
						delay: time.Duration(sec) * time.Second,
						err:   lastErr,
					}
				}
			}
		}
		continue
	}
	return nil, lastErr
}

type retryAfterError struct {
	delay time.Duration
	err   error
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func retryDelay(attempt int, err error) time.Duration {
	if ra, ok := err.(*retryAfterError); ok && ra.delay > 0 {
		return ra.delay + jitter(200*time.Millisecond)
	}
	base := time.Second
	for i := 1; i < attempt; i++ {
		base *= 2
	}
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	return base + jitter(500*time.Millisecond)
}

func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max)))
}
