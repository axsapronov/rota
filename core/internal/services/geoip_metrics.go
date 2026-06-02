package services

import (
	"sync"
	"time"
)

// GeoIPMetricsSnapshot is returned by the system metrics API.
type GeoIPMetricsSnapshot struct {
	QueuePending             int     `json:"queue_pending"`
	QueuedInMemory           int     `json:"queued_in_memory"`
	BatchRequestsLastMinute  int     `json:"batch_requests_last_minute"`
	BatchRequestsLimit       int     `json:"batch_requests_limit"`
	UsagePercent1m           float64 `json:"usage_percent_1m"`
	IPsUpdatedLast10m        int     `json:"ips_updated_last_10m"`
}

type geoSample struct {
	at            time.Time
	batchRequests int
	ipsSuccess    int
	ipsFailed     int
	ipsUpdated    int
}

type geoMetrics struct {
	mu     sync.Mutex
	events []geoSample
}

func newGeoMetrics() *geoMetrics {
	return &geoMetrics{events: make([]geoSample, 0, 128)}
}

func (m *geoMetrics) record(s geoSample) {
	if s.at.IsZero() {
		s.at = time.Now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, s)
	cutoff := s.at.Add(-time.Hour)
	i := 0
	for i < len(m.events) && m.events[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		m.events = append([]geoSample(nil), m.events[i:]...)
	}
}

func (m *geoMetrics) totalsSince(since time.Time) (batchRequests, ipsSuccess, ipsFailed, ipsUpdated int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.events {
		if e.at.Before(since) {
			continue
		}
		batchRequests += e.batchRequests
		ipsSuccess += e.ipsSuccess
		ipsFailed += e.ipsFailed
		ipsUpdated += e.ipsUpdated
	}
	return batchRequests, ipsSuccess, ipsFailed, ipsUpdated
}

func (m *geoMetrics) snapshot(queuePending, queuedInMemory, batchLimit int) GeoIPMetricsSnapshot {
	now := time.Now()
	batch1m, _, _, _ := m.totalsSince(now.Add(-time.Minute))
	_, _, _, updated10mFull := m.totalsSince(now.Add(-10 * time.Minute))

	usage := 0.0
	if batchLimit > 0 {
		usage = float64(batch1m) / float64(batchLimit) * 100
		if usage > 100 {
			usage = 100
		}
	}

	return GeoIPMetricsSnapshot{
		QueuePending:            queuePending,
		QueuedInMemory:            queuedInMemory,
		BatchRequestsLastMinute:   batch1m,
		BatchRequestsLimit:        batchLimit,
		UsagePercent1m:            usage,
		IPsUpdatedLast10m:         updated10mFull,
	}
}

// RecordBatchResult records metrics for one completed batch cycle.
func (m *geoMetrics) RecordBatchResult(batchRequests, ipsSuccess, ipsFailed, ipsUpdated int) {
	m.record(geoSample{
		batchRequests: batchRequests,
		ipsSuccess:    ipsSuccess,
		ipsFailed:     ipsFailed,
		ipsUpdated:    ipsUpdated,
	})
}
