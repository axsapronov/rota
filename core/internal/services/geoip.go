package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alpkeskin/rota/core/internal/config"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"golang.org/x/time/rate"
)

const defaultGeoIPBatchURL = "http://ip-api.com/batch"

const geoBatchFields = "status,message,country,countryCode,regionName,city,isp,lat,lon,query"

// ipAPIResponse is the response from ip-api.com batch endpoint
type ipAPIResponse struct {
	Status      string  `json:"status"`
	Message     string  `json:"message"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	Region      string  `json:"regionName"`
	City        string  `json:"city"`
	ISP         string  `json:"isp"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Query       string  `json:"query"`
}

// GeoIPService calls ip-api.com batch endpoint (no in-memory cache).
type GeoIPService struct {
	client               *http.Client
	batchURL             string
	batchSize            int
	batchRequestsPerMin  int
	maxRetries           int
	limiter              *rate.Limiter
	logger               *logger.Logger
	metrics              *geoMetrics
}

// NewGeoIPService creates a new GeoIPService.
func NewGeoIPService(log *logger.Logger, cfg config.GeoIPConfig) *GeoIPService {
	return &GeoIPService{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		batchURL:            defaultGeoIPBatchURL,
		batchSize:             cfg.BatchSize,
		batchRequestsPerMin:   cfg.BatchRequestsPerMinute,
		maxRetries:            cfg.MaxRetries,
		limiter: rate.NewLimiter(
			rate.Limit(float64(cfg.BatchRequestsPerMinute)/60.0),
			1,
		),
		logger:  log,
		metrics: newGeoMetrics(),
	}
}

func newGeoIPServiceForTest(log *logger.Logger, cfg config.GeoIPConfig, batchURL string) *GeoIPService {
	g := NewGeoIPService(log, cfg)
	g.batchURL = batchURL
	return g
}

// BatchSize returns max IPs per batch HTTP request.
func (g *GeoIPService) BatchSize() int {
	return g.batchSize
}

// BatchRequestsPerMinute returns configured batch HTTP rate limit.
func (g *GeoIPService) BatchRequestsPerMinute() int {
	return g.batchRequestsPerMin
}

// Metrics returns a metrics snapshot for the dashboard.
func (g *GeoIPService) Metrics(queuePending, queuedInMemory int) GeoIPMetricsSnapshot {
	return g.metrics.snapshot(queuePending, queuedInMemory, g.batchRequestsPerMin)
}

// LookupBatch performs one ip-api.com batch POST for up to batchSize IPs.
// Returns map[ip]GeoInfo for successful lookups only.
func (g *GeoIPService) LookupBatch(ctx context.Context, ips []string) (map[string]models.GeoInfo, error) {
	if len(ips) == 0 {
		return nil, nil
	}
	if len(ips) > g.batchSize {
		return nil, fmt.Errorf("geoip batch size %d exceeds limit %d", len(ips), g.batchSize)
	}

	if err := g.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	responses, apiRemaining, err := g.doBatchHTTP(ctx, ips)
	if err != nil {
		return nil, err
	}

	result := make(map[string]models.GeoInfo, len(responses))
	var success, failed int
	for _, r := range responses {
		if r.Status != "success" {
			failed++
			g.logger.Warn("geoip lookup failed for ip",
				"ip", r.Query,
				"message", r.Message,
			)
			continue
		}
		success++
		result[r.Query] = models.GeoInfo{
			CountryCode: r.CountryCode,
			CountryName: r.Country,
			RegionName:  r.Region,
			CityName:    r.City,
			ISP:         r.ISP,
			Latitude:    r.Lat,
			Longitude:   r.Lon,
		}
	}

	g.metrics.RecordBatchResult(1, success, failed, 0)

	if apiRemaining >= 0 {
		g.logger.Debug("geoip batch api quota", "remaining", apiRemaining)
	}

	return result, nil
}

// RecordDBUpdates records proxies written to the database.
func (g *GeoIPService) RecordDBUpdates(n int) {
	if n > 0 {
		g.metrics.RecordBatchResult(0, 0, 0, n)
	}
}

func (g *GeoIPService) doBatchHTTP(ctx context.Context, ips []string) ([]ipAPIResponse, int, error) {
	type reqItem struct {
		Query  string `json:"query"`
		Fields string `json:"fields"`
	}
	items := make([]reqItem, len(ips))
	for i, ip := range ips {
		items[i] = reqItem{Query: ip, Fields: geoBatchFields}
	}

	body, err := json.Marshal(items)
	if err != nil {
		return nil, -1, fmt.Errorf("marshal geoip batch: %w", err)
	}

	var lastErr error
	var lastRemaining int = -1

	for attempt := 0; attempt <= g.maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt, lastErr)
			g.logger.Info("geo batch retry",
				"attempt", attempt,
				"ips", len(ips),
				"wait_sec", delay.Seconds(),
				"error", lastErr,
			)
			select {
			case <-ctx.Done():
				return nil, -1, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.batchURL, strings.NewReader(string(body)))
		if err != nil {
			return nil, -1, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := g.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("geoip request: %w", err)
			if attempt < g.maxRetries {
				continue
			}
			return nil, -1, lastErr
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("geoip read body: %w", readErr)
			if attempt < g.maxRetries {
				continue
			}
			return nil, -1, lastErr
		}

		remaining := parseAPIHeaderInt(resp.Header.Get("X-Rl"), -1)
		lastRemaining = remaining

		if resp.StatusCode == http.StatusOK {
			var responses []ipAPIResponse
			if err := json.Unmarshal(respBody, &responses); err != nil {
				return nil, remaining, fmt.Errorf("decode geoip batch: %w", err)
			}
			return responses, remaining, nil
		}

		lastErr = fmt.Errorf("geoip api status %d", resp.StatusCode)
		if !isRetryableStatus(resp.StatusCode) || attempt >= g.maxRetries {
			return nil, remaining, lastErr
		}

		if ttl := parseRateLimitWait(resp); ttl > 0 {
			lastErr = &rateLimitWaitError{wait: ttl, err: lastErr}
		}
	}

	return nil, lastRemaining, lastErr
}

type rateLimitWaitError struct {
	wait time.Duration
	err  error
}

func (e *rateLimitWaitError) Error() string { return e.err.Error() }
func (e *rateLimitWaitError) Unwrap() error { return e.err }

func parseRateLimitWait(resp *http.Response) time.Duration {
	if ttl := parseAPIHeaderInt(resp.Header.Get("X-Ttl"), 0); ttl > 0 {
		return time.Duration(ttl) * time.Second
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if sec, err := strconv.Atoi(ra); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	return 0
}

func parseAPIHeaderInt(v string, fallback int) int {
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func retryDelay(attempt int, err error) time.Duration {
	if rw, ok := err.(*rateLimitWaitError); ok && rw.wait > 0 {
		return rw.wait + jitter(200*time.Millisecond)
	}
	base := time.Second
	for i := 1; i < attempt; i++ {
		base *= 2
	}
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	return base + jitter(500*time.Millisecond)
}

func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max)))
}
