package checkstats

import (
	"sync"
	"time"
)

// Snapshot is exposed via the system metrics API.
type Snapshot struct {
	QueuePending     int     `json:"queue_pending"`
	ProcessedLast10m int     `json:"processed_last_10m"`
	ChecksLastMinute int     `json:"checks_last_minute"`
	SuccessPercent1m float64 `json:"success_percent_1m"`
}

type sample struct {
	at        time.Time
	checks    int
	successes int
}

type collector struct {
	mu     sync.Mutex
	events []sample
}

var global = &collector{events: make([]sample, 0, 64)}

// Record logs one completed proxy health check.
func Record(success bool) {
	s := sample{at: time.Now(), checks: 1}
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
func BuildSnapshot(queuePending int) Snapshot {
	now := time.Now()
	checks10m, _ := global.sumSince(now.Add(-10 * time.Minute))
	checks1m, succ1m := global.sumSince(now.Add(-time.Minute))

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
