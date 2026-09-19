// Package checkstats keeps in-memory rolling statistics about proxy health
// checks and the global (orphan) health-check scheduler. It is exposed through
// the system metrics API.
//
// The rolling window counts checks performed through HealthChecker.CheckProxy
// (orphan / manual / single-proxy checks). Pool-level health checks
// (PoolService) do NOT go through CheckProxy and are therefore not counted
// here.
package checkstats

import (
	"sync"
	"time"
)

// Snapshot is a point-in-time view of the rolling health-check stats plus the
// current job-queue depth. It is exposed via the system metrics API.
type Snapshot struct {
	QueuePending     int     `json:"queue_pending"`
	ProcessedLast10m int     `json:"processed_last_10m"`
	ChecksLastMinute int     `json:"checks_last_minute"`
	SuccessPercent1m float64 `json:"success_percent_1m"`
}

// sample is one recorded health-check completion.
type sample struct {
	at        time.Time
	checks    int
	successes int
}

// collector is a 1-hour rolling window of check samples.
type collector struct {
	mu     sync.Mutex
	events []sample
	// now is the time source; injectable in tests (defaults to time.Now).
	now func() time.Time
}

var global = &collector{
	events: make([]sample, 0, 64),
	now:    time.Now,
}

// Record logs one completed proxy health check. It is called synchronously from
// the check path so the rolling window reflects check completion, not the
// (asynchronous) database write.
func Record(success bool) {
	now := time.Now()
	s := sample{at: now, checks: 1}
	if success {
		s.successes = 1
	}
	global.record(s)
}

func (c *collector) record(s sample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, s)
	cutoff := s.at.Add(-time.Hour)
	i := 0
	for i < len(c.events) && c.events[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		c.events = append([]sample(nil), c.events[i:]...)
	}
}

func (c *collector) sumSince(since time.Time) (checks, successes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.at.Before(since) {
			continue
		}
		checks += e.checks
		successes += e.successes
	}
	return checks, successes
}

// BuildSnapshot combines rolling check stats with the current job queue depth.
// Percentages are in [0, 100] and are 0 when the denominator is zero.
func BuildSnapshot(queuePending int) Snapshot {
	return buildSnapshot(global, queuePending, time.Now())
}

// buildSnapshot is the testable core of BuildSnapshot: it reads the rolling
// window from the given collector as of the given time.
func buildSnapshot(c *collector, queuePending int, now time.Time) Snapshot {
	checks10m, _ := c.sumSince(now.Add(-10 * time.Minute))
	checks1m, succ1m := c.sumSince(now.Add(-time.Minute))

	pct := 0.0
	if checks1m > 0 {
		pct = float64(succ1m) / float64(checks1m) * 100
	}

	return Snapshot{
		QueuePending:     queuePending,
		ProcessedLast10m: checks10m,
		ChecksLastMinute: checks1m,
		SuccessPercent1m: pct,
	}
}
