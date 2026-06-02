package services

import (
	"sync"
	"time"
)

// CleanupRunStatus represents the outcome of the latest cleanup run.
type CleanupRunStatus string

const (
	CleanupRunStatusIdle  CleanupRunStatus = "idle"
	CleanupRunStatusOK    CleanupRunStatus = "ok"
	CleanupRunStatusError CleanupRunStatus = "error"
)

// LogCleanupSnapshot is returned by the system metrics API.
type LogCleanupSnapshot struct {
	Enabled              bool             `json:"enabled"`
	LastRunAt            *time.Time       `json:"last_run_at,omitempty"`
	LastStatus           CleanupRunStatus `json:"last_status"`
	LastDurationMs       int64            `json:"last_duration_ms"`
	NextRunAt            *time.Time       `json:"next_run_at,omitempty"`
	RetentionDays        int              `json:"retention_days"`
	CompressionAfterDays int              `json:"compression_after_days"`
	LastError            string           `json:"last_error,omitempty"`
}

// ProxyCleanupSnapshot is returned by the system metrics API.
type ProxyCleanupSnapshot struct {
	Enabled              bool             `json:"enabled"`
	LastRunAt            *time.Time       `json:"last_run_at,omitempty"`
	LastStatus           CleanupRunStatus `json:"last_status"`
	LastDurationMs       int64            `json:"last_duration_ms"`
	NextRunAt            *time.Time       `json:"next_run_at,omitempty"`
	DeletedProxies       int64            `json:"deleted_proxies"`
	MaxFailedDays        int              `json:"max_failed_days"`
	MinSuccessRate       float64          `json:"min_success_rate"`
	CleanupIntervalHours int              `json:"cleanup_interval_hours"`
	LastError            string           `json:"last_error,omitempty"`
}

// CleanupMetricsSnapshot is returned by the system metrics API.
type CleanupMetricsSnapshot struct {
	Log   LogCleanupSnapshot   `json:"log"`
	Proxy ProxyCleanupSnapshot `json:"proxy"`
}

type cleanupMetricsStore struct {
	mu       sync.RWMutex
	snapshot CleanupMetricsSnapshot
}

var cleanupMetrics = newCleanupMetricsStore()

func newCleanupMetricsStore() *cleanupMetricsStore {
	return &cleanupMetricsStore{
		snapshot: CleanupMetricsSnapshot{
			Log: LogCleanupSnapshot{
				LastStatus: CleanupRunStatusIdle,
			},
			Proxy: ProxyCleanupSnapshot{
				LastStatus: CleanupRunStatusIdle,
			},
		},
	}
}

// CleanupMetrics returns the latest cleanup metrics snapshot.
func CleanupMetrics() CleanupMetricsSnapshot {
	return cleanupMetrics.snapshotCopy()
}

func (s *cleanupMetricsStore) snapshotCopy() CleanupMetricsSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	copy := s.snapshot
	return copy
}

// SetLogCleanupConfig updates log cleanup config status.
func SetLogCleanupConfig(enabled bool, retentionDays, compressionAfterDays, intervalHours int) {
	cleanupMetrics.mu.Lock()
	defer cleanupMetrics.mu.Unlock()

	log := &cleanupMetrics.snapshot.Log
	log.Enabled = enabled
	log.RetentionDays = retentionDays
	log.CompressionAfterDays = compressionAfterDays

	if !enabled {
		log.NextRunAt = nil
		return
	}

	if intervalHours > 0 {
		next := time.Now().Add(time.Duration(intervalHours) * time.Hour)
		log.NextRunAt = &next
	}
}

// RecordLogCleanupRun records one completed log cleanup run.
func RecordLogCleanupRun(startedAt time.Time, err error) {
	cleanupMetrics.mu.Lock()
	defer cleanupMetrics.mu.Unlock()

	now := time.Now()
	log := &cleanupMetrics.snapshot.Log
	log.LastRunAt = &now
	log.LastDurationMs = now.Sub(startedAt).Milliseconds()

	if err != nil {
		log.LastStatus = CleanupRunStatusError
		log.LastError = err.Error()
		return
	}

	log.LastStatus = CleanupRunStatusOK
	log.LastError = ""
}

// SetProxyCleanupConfig updates proxy cleanup config status.
func SetProxyCleanupConfig(enabled bool, maxFailedDays int, minSuccessRate float64, intervalHours int) {
	cleanupMetrics.mu.Lock()
	defer cleanupMetrics.mu.Unlock()

	proxy := &cleanupMetrics.snapshot.Proxy
	proxy.Enabled = enabled
	proxy.MaxFailedDays = maxFailedDays
	proxy.MinSuccessRate = minSuccessRate
	proxy.CleanupIntervalHours = intervalHours

	if !enabled {
		proxy.NextRunAt = nil
		return
	}

	if intervalHours > 0 {
		next := time.Now().Add(time.Duration(intervalHours) * time.Hour)
		proxy.NextRunAt = &next
	}
}

// RecordProxyCleanupRun records one completed proxy cleanup run.
func RecordProxyCleanupRun(startedAt time.Time, deleted int64, err error) {
	cleanupMetrics.mu.Lock()
	defer cleanupMetrics.mu.Unlock()

	now := time.Now()
	proxy := &cleanupMetrics.snapshot.Proxy
	proxy.LastRunAt = &now
	proxy.LastDurationMs = now.Sub(startedAt).Milliseconds()

	if err != nil {
		proxy.LastStatus = CleanupRunStatusError
		proxy.LastError = err.Error()
		return
	}

	proxy.LastStatus = CleanupRunStatusOK
	proxy.LastError = ""
	proxy.DeletedProxies = deleted
}
