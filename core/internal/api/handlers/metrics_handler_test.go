package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alpkeskin/rota/core/internal/checkstats"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

func newTestMetricsHandler(
	hcProvider func() checkstats.Snapshot,
	globalHCProvider func() checkstats.GlobalHealthCheckSnapshot,
	geoProvider func() *GeoMetrics,
	cleanupProvider func() *checkstats.CleanupMetrics,
) *MetricsHandler {
	return NewMetricsHandler(logger.New("error"), hcProvider, globalHCProvider, geoProvider, cleanupProvider)
}

func TestCollectSystemMetrics_AllProviders(t *testing.T) {
	h := newTestMetricsHandler(
		func() checkstats.Snapshot {
			return checkstats.Snapshot{QueuePending: 3, ProcessedLast10m: 10, ChecksLastMinute: 5, SuccessPercent1m: 100}
		},
		func() checkstats.GlobalHealthCheckSnapshot {
			return checkstats.GlobalHealthCheckSnapshot{
				Enabled: true, IntervalMinutes: 30,
				LastStatus: checkstats.GlobalHealthCheckStatusOK, LastCheckedProxies: 7,
			}
		},
		func() *GeoMetrics {
			return &GeoMetrics{
				QueuePending: 42, QueuedInMemory: 42,
				BatchRequestsLastMin: 3, BatchRequestsLimit: 10,
				UsagePercent1m: 30, IPsUpdatedLast10m: 5,
			}
		},
		checkstats.GetCleanupMetrics,
	)

	m := h.collectSystemMetrics()

	if m.HealthCheck == nil || m.HealthCheck.QueuePending != 3 {
		t.Errorf("health_check section: got %+v", m.HealthCheck)
	}
	if m.GlobalHealthCheck == nil || m.GlobalHealthCheck.LastCheckedProxies != 7 {
		t.Errorf("global_health_check section: got %+v", m.GlobalHealthCheck)
	}
	if m.Geo == nil || m.Geo.QueuePending != 42 || m.Geo.UsagePercent1m != 30 {
		t.Errorf("geo section: got %+v", m.Geo)
	}
	if m.Cleanup == nil || m.Cleanup.Log.LastStatus != checkstats.CleanupStatusIdle {
		t.Errorf("cleanup section: got %+v", m.Cleanup)
	}
}

func TestGetSystemMetrics_NoProviders(t *testing.T) {
	h := newTestMetricsHandler(nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.GetSystemMetrics(rec, httptest.NewRequest("GET", "/api/v1/metrics/system", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	for _, key := range []string{"memory", "cpu", "disk", "runtime"} {
		if _, ok := body[key]; !ok {
			t.Errorf("base section %q missing from response", key)
		}
	}
	for _, key := range []string{"health_check", "global_health_check", "geo", "cleanup"} {
		if _, ok := body[key]; ok {
			t.Errorf("section %q should be omitted when its provider is nil", key)
		}
	}
}

func TestCollectSystemMetrics_PartialProviders(t *testing.T) {
	h := newTestMetricsHandler(nil, nil, nil, checkstats.GetCleanupMetrics)

	m := h.collectSystemMetrics()

	if m.Cleanup == nil {
		t.Fatal("cleanup section missing despite provider")
	}
	if m.HealthCheck != nil {
		t.Errorf("health_check should be nil, got %+v", m.HealthCheck)
	}
	if m.GlobalHealthCheck != nil {
		t.Errorf("global_health_check should be nil, got %+v", m.GlobalHealthCheck)
	}
	if m.Geo != nil {
		t.Errorf("geo should be nil, got %+v", m.Geo)
	}
}
