package checkstats

import (
	"sync"
	"time"
)

// GlobalHealthCheckStatus describes the latest periodic orphan health-check run.
type GlobalHealthCheckStatus string

const (
	GlobalHealthCheckStatusIdle  GlobalHealthCheckStatus = "idle"
	GlobalHealthCheckStatusOK    GlobalHealthCheckStatus = "ok"
	GlobalHealthCheckStatusError GlobalHealthCheckStatus = "error"
)

// GlobalHealthCheckSnapshot is a point-in-time view of the global (orphan)
// health-check scheduler. It is exposed via the system metrics API.
type GlobalHealthCheckSnapshot struct {
	Enabled            bool                    `json:"enabled"`
	IntervalMinutes    int                     `json:"interval_minutes"`
	IsRunning          bool                    `json:"is_running"`
	LastStartedAt      *time.Time              `json:"last_started_at,omitempty"`
	LastFinishedAt     *time.Time              `json:"last_finished_at,omitempty"`
	LastStatus         GlobalHealthCheckStatus `json:"last_status"`
	LastDurationMs     int64                   `json:"last_duration_ms"`
	LastError          string                  `json:"last_error,omitempty"`
	NextRunAt          *time.Time              `json:"next_run_at,omitempty"`
	LastCheckedProxies int                     `json:"last_checked_proxies"`
}

type globalHealthCheckStore struct {
	mu       sync.RWMutex
	snapshot GlobalHealthCheckSnapshot
}

var globalHealthCheck = &globalHealthCheckStore{
	snapshot: GlobalHealthCheckSnapshot{
		LastStatus: GlobalHealthCheckStatusIdle,
	},
}

// SetGlobalHealthCheckConfig updates the enabled state and runtime interval.
// When disabled it clears the running flag and the next-run time; when enabled
// it schedules the next run now + interval.
func SetGlobalHealthCheckConfig(enabled bool, interval time.Duration) {
	globalHealthCheck.mu.Lock()
	defer globalHealthCheck.mu.Unlock()

	snapshot := &globalHealthCheck.snapshot
	snapshot.Enabled = enabled
	if interval > 0 {
		snapshot.IntervalMinutes = int(interval / time.Minute)
	}
	if !enabled {
		snapshot.IsRunning = false
		snapshot.NextRunAt = nil
		return
	}

	next := time.Now().Add(interval)
	snapshot.NextRunAt = &next
}

// RecordGlobalHealthCheckStart marks a periodic orphan health-check run as started.
func RecordGlobalHealthCheckStart(startedAt time.Time) {
	globalHealthCheck.mu.Lock()
	defer globalHealthCheck.mu.Unlock()

	snapshot := &globalHealthCheck.snapshot
	snapshot.IsRunning = true
	snapshot.LastStartedAt = &startedAt
}

// RecordGlobalHealthCheckFinish stores the completed run status. The duration is
// measured from startedAt. When the scheduler is enabled the next run is
// scheduled now + interval.
func RecordGlobalHealthCheckFinish(startedAt time.Time, checkedProxies int, err error, interval time.Duration) {
	globalHealthCheck.mu.Lock()
	defer globalHealthCheck.mu.Unlock()

	now := time.Now()
	snapshot := &globalHealthCheck.snapshot
	snapshot.IsRunning = false
	snapshot.LastFinishedAt = &now
	snapshot.LastDurationMs = now.Sub(startedAt).Milliseconds()
	snapshot.LastCheckedProxies = checkedProxies

	if err != nil {
		snapshot.LastStatus = GlobalHealthCheckStatusError
		snapshot.LastError = err.Error()
	} else {
		snapshot.LastStatus = GlobalHealthCheckStatusOK
		snapshot.LastError = ""
	}

	if snapshot.Enabled && interval > 0 {
		next := now.Add(interval)
		snapshot.NextRunAt = &next
	}
}

// GetGlobalHealthCheckSnapshot returns a thread-safe copy of the snapshot.
func GetGlobalHealthCheckSnapshot() GlobalHealthCheckSnapshot {
	globalHealthCheck.mu.RLock()
	defer globalHealthCheck.mu.RUnlock()
	return globalHealthCheck.snapshot
}
