package checkstats

import (
	"sync"
	"time"
)

// CleanupStatus is the outcome of a cleanup run.
type CleanupStatus string

const (
	CleanupStatusIdle  CleanupStatus = "idle"
	CleanupStatusOK    CleanupStatus = "ok"
	CleanupStatusError CleanupStatus = "error"
)

// LogCleanupSnapshot is a point-in-time view of the log cleanup service.
type LogCleanupSnapshot struct {
	Enabled              bool          `json:"enabled"`
	LastRunAt            *time.Time    `json:"last_run_at,omitempty"`
	LastStatus           CleanupStatus `json:"last_status"`
	LastDurationMs       int64         `json:"last_duration_ms"`
	NextRunAt            *time.Time    `json:"next_run_at,omitempty"`
	RetentionDays        int           `json:"retention_days"`
	CompressionAfterDays int           `json:"compression_after_days"`
	LastError            string        `json:"last_error,omitempty"`
}

// ProxyCleanupSnapshot is a point-in-time view of the proxy cleanup service.
type ProxyCleanupSnapshot struct {
	Enabled              bool          `json:"enabled"`
	LastRunAt            *time.Time    `json:"last_run_at,omitempty"`
	LastStatus           CleanupStatus `json:"last_status"`
	LastDurationMs       int64         `json:"last_duration_ms"`
	NextRunAt            *time.Time    `json:"next_run_at,omitempty"`
	DeletedProxies       int           `json:"deleted_proxies"`
	MaxFailedDays        int           `json:"max_failed_days"`
	MinSuccessRate       float64       `json:"min_success_rate"`
	CleanupIntervalHours int           `json:"cleanup_interval_hours"`
	LastError            string        `json:"last_error,omitempty"`
}

// ForceCleanupSnapshot is a point-in-time view of the last force cleanup run.
type ForceCleanupSnapshot struct {
	LastRunAt      *time.Time    `json:"last_run_at,omitempty"`
	LastStatus     CleanupStatus `json:"last_status"`
	LastDurationMs int64         `json:"last_duration_ms"`
	DeletedProxies int           `json:"deleted_proxies"`
	LastError      string        `json:"last_error,omitempty"`
}

// CleanupMetrics aggregates the cleanup snapshots. It is exposed via the
// system metrics API (feature 07 wires it into GET /metrics/system).
type CleanupMetrics struct {
	Log   LogCleanupSnapshot   `json:"log"`
	Proxy ProxyCleanupSnapshot `json:"proxy"`
	Force ForceCleanupSnapshot `json:"force"`
}

type cleanupStore struct {
	mu    sync.RWMutex
	log   LogCleanupSnapshot
	proxy ProxyCleanupSnapshot
	force ForceCleanupSnapshot
}

var cleanup = &cleanupStore{
	log:   LogCleanupSnapshot{LastStatus: CleanupStatusIdle},
	proxy: ProxyCleanupSnapshot{LastStatus: CleanupStatusIdle},
	force: ForceCleanupSnapshot{LastStatus: CleanupStatusIdle},
}

// RecordLogCleanup stores the latest log cleanup run.
func RecordLogCleanup(snap LogCleanupSnapshot) {
	cleanup.mu.Lock()
	defer cleanup.mu.Unlock()
	cleanup.log = snap
}

// RecordProxyCleanup stores the latest proxy cleanup run.
func RecordProxyCleanup(snap ProxyCleanupSnapshot) {
	cleanup.mu.Lock()
	defer cleanup.mu.Unlock()
	cleanup.proxy = snap
}

// RecordForceCleanup stores the latest force cleanup run.
func RecordForceCleanup(snap ForceCleanupSnapshot) {
	cleanup.mu.Lock()
	defer cleanup.mu.Unlock()
	cleanup.force = snap
}

// GetCleanupMetrics returns a thread-safe copy of the cleanup metrics.
func GetCleanupMetrics() *CleanupMetrics {
	cleanup.mu.RLock()
	defer cleanup.mu.RUnlock()
	return &CleanupMetrics{
		Log:   cleanup.log,
		Proxy: cleanup.proxy,
		Force: cleanup.force,
	}
}
