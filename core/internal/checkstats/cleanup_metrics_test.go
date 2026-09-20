package checkstats

import (
	"testing"
	"time"
)

// Runs first in the package (file order): all three snapshots start idle.
func TestCleanupMetrics_InitialStatusesIdle(t *testing.T) {
	m := GetCleanupMetrics()
	if m.Log.LastStatus != CleanupStatusIdle {
		t.Errorf("log initial status = %s, want idle", m.Log.LastStatus)
	}
	if m.Proxy.LastStatus != CleanupStatusIdle {
		t.Errorf("proxy initial status = %s, want idle", m.Proxy.LastStatus)
	}
	if m.Force.LastStatus != CleanupStatusIdle {
		t.Errorf("force initial status = %s, want idle", m.Force.LastStatus)
	}
}

func TestCleanupMetrics_RecordRoundTrip(t *testing.T) {
	now := time.Now()
	next := now.Add(time.Hour)

	RecordLogCleanup(LogCleanupSnapshot{
		Enabled:              true,
		LastRunAt:            &now,
		LastStatus:           CleanupStatusOK,
		LastDurationMs:       42,
		NextRunAt:            &next,
		RetentionDays:        30,
		CompressionAfterDays: 7,
	})
	RecordProxyCleanup(ProxyCleanupSnapshot{
		Enabled:              true,
		LastRunAt:            &now,
		LastStatus:           CleanupStatusError,
		LastDurationMs:       7,
		NextRunAt:            &next,
		DeletedProxies:       5,
		MaxFailedDays:        7,
		MinSuccessRate:       50,
		CleanupIntervalHours: 24,
		LastError:            "boom",
	})
	RecordForceCleanup(ForceCleanupSnapshot{
		LastRunAt:      &now,
		LastStatus:     CleanupStatusOK,
		LastDurationMs: 99,
		DeletedProxies: 123,
	})

	m := GetCleanupMetrics()

	if m.Log.Enabled != true || m.Log.LastStatus != CleanupStatusOK || m.Log.LastDurationMs != 42 ||
		m.Log.RetentionDays != 30 || m.Log.CompressionAfterDays != 7 || m.Log.NextRunAt == nil ||
		m.Log.NextRunAt.Unix() != next.Unix() {
		t.Errorf("log snapshot round-trip mismatch: %+v", m.Log)
	}
	if m.Proxy.Enabled != true || m.Proxy.LastStatus != CleanupStatusError || m.Proxy.LastDurationMs != 7 ||
		m.Proxy.DeletedProxies != 5 || m.Proxy.MaxFailedDays != 7 || m.Proxy.MinSuccessRate != 50 ||
		m.Proxy.CleanupIntervalHours != 24 || m.Proxy.LastError != "boom" {
		t.Errorf("proxy snapshot round-trip mismatch: %+v", m.Proxy)
	}
	if m.Force.LastStatus != CleanupStatusOK || m.Force.LastDurationMs != 99 || m.Force.DeletedProxies != 123 {
		t.Errorf("force snapshot round-trip mismatch: %+v", m.Force)
	}
}
