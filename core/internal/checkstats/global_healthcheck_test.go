package checkstats

import (
	"errors"
	"testing"
	"time"
)

func TestGlobalHealthCheckSnapshot_lifecycle(t *testing.T) {
	// Disabled: not running, no next-run time.
	SetGlobalHealthCheckConfig(false, 0)
	snap := GetGlobalHealthCheckSnapshot()
	if snap.Enabled {
		t.Error("Enabled = true, want false")
	}
	if snap.IsRunning {
		t.Error("IsRunning = true, want false when disabled")
	}
	if snap.NextRunAt != nil {
		t.Error("NextRunAt != nil, want nil when disabled")
	}
	if snap.LastStatus != GlobalHealthCheckStatusIdle {
		t.Errorf("LastStatus = %q, want %q", snap.LastStatus, GlobalHealthCheckStatusIdle)
	}

	// Enabled: interval recorded and a next-run time scheduled.
	interval := 30 * time.Minute
	SetGlobalHealthCheckConfig(true, interval)
	snap = GetGlobalHealthCheckSnapshot()
	if !snap.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if snap.IntervalMinutes != 30 {
		t.Errorf("IntervalMinutes = %d, want 30", snap.IntervalMinutes)
	}
	if snap.NextRunAt == nil {
		t.Fatal("NextRunAt = nil, want set when enabled")
	}

	// Start a run.
	startedAt := time.Now()
	RecordGlobalHealthCheckStart(startedAt)
	snap = GetGlobalHealthCheckSnapshot()
	if !snap.IsRunning {
		t.Error("IsRunning = false, want true after start")
	}
	if snap.LastStartedAt == nil {
		t.Error("LastStartedAt = nil, want set")
	}

	// Finish successfully.
	RecordGlobalHealthCheckFinish(startedAt, 42, nil, interval)
	snap = GetGlobalHealthCheckSnapshot()
	if snap.IsRunning {
		t.Error("IsRunning = true, want false after finish")
	}
	if snap.LastStatus != GlobalHealthCheckStatusOK {
		t.Errorf("LastStatus = %q, want %q", snap.LastStatus, GlobalHealthCheckStatusOK)
	}
	if snap.LastCheckedProxies != 42 {
		t.Errorf("LastCheckedProxies = %d, want 42", snap.LastCheckedProxies)
	}
	if snap.LastFinishedAt == nil {
		t.Error("LastFinishedAt = nil, want set")
	}
	if snap.NextRunAt == nil {
		t.Error("NextRunAt = nil, want set when enabled")
	}

	// Finish with an error.
	startedAt2 := time.Now()
	RecordGlobalHealthCheckStart(startedAt2)
	RecordGlobalHealthCheckFinish(startedAt2, 0, errors.New("boom"), interval)
	snap = GetGlobalHealthCheckSnapshot()
	if snap.LastStatus != GlobalHealthCheckStatusError {
		t.Errorf("LastStatus = %q, want %q", snap.LastStatus, GlobalHealthCheckStatusError)
	}
	if snap.LastError != "boom" {
		t.Errorf("LastError = %q, want %q", snap.LastError, "boom")
	}
}
