package services

import (
	"sync"
	"time"
)

// GeoIPMetricsSnapshot is returned by the system metrics API.
type GeoIPMetricsSnapshot struct {
	QueuePending       int     `json:"queue_pending"`
	ProcessedLast10m   int     `json:"processed_last_10m"`
	LookupsLastMinute  int     `json:"lookups_last_minute"`
	QueriesPerMinute   int     `json:"queries_per_minute"`
	UsagePercent1m     float64 `json:"usage_percent_1m"`
}

type geoSample struct {
	at        time.Time
	lookups   int
	processed int
}

type geoMetrics struct {
	mu     sync.Mutex
	events []geoSample
}

func newGeoMetrics() *geoMetrics {
	return &geoMetrics{events: make([]geoSample, 0, 64)}
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

func (m *geoMetrics) sumSince(since time.Time, field func(geoSample) int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, e := range m.events {
		if e.at.Before(since) {
			continue
		}
		total += field(e)
	}
	return total
}
