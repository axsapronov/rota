package checkstats

import (
	"testing"
	"time"
)

// newTestCollector builds a collector with a fixed, injectable time source so
// the rolling-window boundaries can be tested deterministically.
func newTestCollector(now time.Time) *collector {
	return &collector{
		events: make([]sample, 0, 64),
		now:    func() time.Time { return now },
	}
}

func TestBuildSnapshot_emptyWindow(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(now)

	snap := buildSnapshot(c, 5, now)

	if snap.QueuePending != 5 {
		t.Errorf("QueuePending = %d, want 5", snap.QueuePending)
	}
	if snap.ProcessedLast10m != 0 {
		t.Errorf("ProcessedLast10m = %d, want 0", snap.ProcessedLast10m)
	}
	if snap.ChecksLastMinute != 0 {
		t.Errorf("ChecksLastMinute = %d, want 0", snap.ChecksLastMinute)
	}
	if snap.SuccessPercent1m != 0 {
		t.Errorf("SuccessPercent1m = %v, want 0 for empty window", snap.SuccessPercent1m)
	}
}

func TestBuildSnapshot_rollingWindows(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(now)

	// Two checks within the last minute: 1 success, 1 failure.
	c.record(sample{at: now.Add(-30 * time.Second), checks: 1, successes: 1})
	c.record(sample{at: now.Add(-10 * time.Second), checks: 1, successes: 0})
	// Three checks within the last 10 minutes but older than one minute.
	for i := 1; i <= 3; i++ {
		c.record(sample{at: now.Add(-time.Minute - time.Duration(i)*time.Minute), checks: 1, successes: 1})
	}
	// Four checks older than 10 minutes (excluded from both windows).
	for i := 11; i <= 14; i++ {
		c.record(sample{at: now.Add(-time.Duration(i) * time.Minute), checks: 1, successes: 1})
	}

	snap := buildSnapshot(c, 7, now)

	if snap.ChecksLastMinute != 2 {
		t.Errorf("ChecksLastMinute = %d, want 2", snap.ChecksLastMinute)
	}
	if snap.SuccessPercent1m != 50 {
		t.Errorf("SuccessPercent1m = %v, want 50", snap.SuccessPercent1m)
	}
	// 10m window = 2 (last minute) + 3 (1-10 min) = 5.
	if snap.ProcessedLast10m != 5 {
		t.Errorf("ProcessedLast10m = %d, want 5", snap.ProcessedLast10m)
	}
	if snap.QueuePending != 7 {
		t.Errorf("QueuePending = %d, want 7", snap.QueuePending)
	}
}

func TestCollector_evictsOlderThanOneHour(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(now)

	// An old sample plus a fresh one; the old one must be evicted on record.
	c.record(sample{at: now.Add(-2 * time.Hour), checks: 100, successes: 100})
	c.record(sample{at: now, checks: 1, successes: 1})

	if len(c.events) != 1 {
		t.Fatalf("len(events) = %d, want 1 after one-hour eviction", len(c.events))
	}
	checks, successes := c.sumSince(now.Add(-time.Hour))
	if checks != 1 || successes != 1 {
		t.Errorf("sum = (%d, %d), want (1, 1)", checks, successes)
	}
}
