package services

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
)

// TestTrimJobResults verifies the results ring buffer: at or under the cap the
// slice is returned unchanged; beyond it only the most recent failures are kept
// (in chronological order), successes dropped.
func TestTrimJobResults(t *testing.T) {
	mk := func(n int, status string) []models.ProxyTestResult {
		out := make([]models.ProxyTestResult, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, models.ProxyTestResult{ID: i + 1, Status: status})
		}
		return out
	}

	t.Run("under cap unchanged", func(t *testing.T) {
		in := mk(50, "failed")
		got := trimJobResults(in)
		if len(got) != 50 {
			t.Fatalf("len = %d, want 50", len(got))
		}
	})

	t.Run("all failures over cap keeps newest", func(t *testing.T) {
		in := mk(150, "failed")
		got := trimJobResults(in)
		if len(got) != maxJobResults {
			t.Fatalf("len = %d, want %d", len(got), maxJobResults)
		}
		// Newest 100 of 150 = ids 51..150, chronological.
		if got[0].ID != 51 || got[len(got)-1].ID != 150 {
			t.Fatalf("kept ids = [%d..%d], want [51..150]", got[0].ID, got[len(got)-1].ID)
		}
		for i, r := range got {
			if r.ID != 51+i {
				t.Fatalf("order broken at %d: id %d, want %d", i, r.ID, 51+i)
			}
		}
	})

	t.Run("mixed keeps only failures", func(t *testing.T) {
		in := make([]models.ProxyTestResult, 0, 150)
		for i := 0; i < 150; i++ {
			status := "failed"
			if i%5 == 0 {
				status = "active"
			}
			in = append(in, models.ProxyTestResult{ID: i + 1, Status: status})
		}
		got := trimJobResults(in)
		if len(got) != maxJobResults {
			t.Fatalf("len = %d, want %d", len(got), maxJobResults)
		}
		for _, r := range got {
			if r.Status == "active" {
				t.Fatalf("success leaked into trimmed results: %+v", r)
			}
		}
	})
}

func TestHCJobStore_consumerSurvivesJobPanic(t *testing.T) {
	store := newHCJobStore(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.Start(ctx, nil)

	var panicked atomic.Bool
	done := make(chan struct{}, 1)

	if _, err := store.Create(HCJobKindProxy, 0, "", "", 1, []int{1}, func(s *HCJobStore, job *HCJob) {
		if !panicked.Swap(true) {
			panic("test panic")
		}
		s.finishDone(job.ID)
	}); err != nil {
		t.Fatalf("create first job: %v", err)
	}

	if _, err := store.Create(HCJobKindProxy, 0, "", "", 1, []int{2}, func(s *HCJobStore, job *HCJob) {
		s.finishDone(job.ID)
		close(done)
	}); err != nil {
		t.Fatalf("create second job: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("second job was not processed after first job panic")
	}

	jobs := store.ListByKind(HCJobKindProxy)
	var failed, completed int
	for _, j := range jobs {
		switch j.Status {
		case HCJobFailed:
			failed++
		case HCJobDone:
			completed++
		}
	}
	if failed != 1 {
		t.Fatalf("expected 1 failed job after panic, got %d", failed)
	}
	if completed != 1 {
		t.Fatalf("expected 1 completed job after consumer recovery, got %d", completed)
	}
}

// queueHead returns the head item of the store's priority queue (test-only).
func queueHead(t *testing.T, s *HCJobStore) hcQueueItem {
	t.Helper()
	s.queue.mu.Lock()
	defer s.queue.mu.Unlock()
	if len(s.queue.items) == 0 {
		t.Fatal("queue is empty, want the requeued job")
	}
	return s.queue.items[0]
}

func TestHCJobStore_requeueRunningOnConsumerRestart(t *testing.T) {
	store := newHCJobStore(4)
	store.jobs["stuck"] = &HCJob{
		ID:     "stuck",
		Kind:   HCJobKindPool,
		Status: HCJobRunning,
	}
	store.ctx = context.Background()

	store.requeueInterruptedJobs()

	head := queueHead(t, store)
	if head.id != "stuck" {
		t.Fatalf("requeued id = %q, want stuck", head.id)
	}
	if head.priority != hcPriorityForKind(HCJobKindPool) {
		t.Fatalf("requeued priority = %d, want %d", head.priority, hcPriorityForKind(HCJobKindPool))
	}

	j, ok := store.Get("stuck")
	if !ok || j.Status != HCJobPending {
		t.Fatalf("job status = %v, want pending", j.Status)
	}
}

// TestHCJobStore_requeueUsesJobPriority verifies that interrupted jobs of
// different kinds are requeued with the priority of their kind, so the
// restart path does not change a job's queue position class.
func TestHCJobStore_requeueUsesJobPriority(t *testing.T) {
	store := newHCJobStore(8)
	store.jobs["pool-run"] = &HCJob{ID: "pool-run", Kind: HCJobKindPool, Status: HCJobRunning}
	store.jobs["idle-run"] = &HCJob{ID: "idle-run", Kind: HCJobKindIdle, Status: HCJobRunning}
	store.jobs["pool-done"] = &HCJob{ID: "pool-done", Kind: HCJobKindPool, Status: HCJobDone}
	store.ctx = context.Background()

	store.requeueInterruptedJobs()

	store.queue.mu.Lock()
	got := make(map[string]hcPriority, len(store.queue.items))
	for _, it := range store.queue.items {
		got[it.id] = it.priority
	}
	store.queue.mu.Unlock()

	if len(got) != 2 {
		t.Fatalf("requeued %d jobs, want 2 (running only): %v", len(got), got)
	}
	if got["pool-run"] != hcPriorityHigh {
		t.Fatalf("pool-run priority = %d, want HIGH", got["pool-run"])
	}
	if got["idle-run"] != hcPriorityLow {
		t.Fatalf("idle-run priority = %d, want LOW", got["idle-run"])
	}
}

func TestHCJobStore_queueFullReturnsError(t *testing.T) {
	// queueCap=1 with the consumer NOT started, so the queue fills up.
	store := newHCJobStore(1)

	if _, err := store.Create(HCJobKindOrphan, 0, "Orphan proxies", "", 0, nil,
		func(s *HCJobStore, job *HCJob) {}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// The second create must be rejected immediately (no blocking).
	done := make(chan error, 1)
	go func() {
		_, e := store.Create(HCJobKindOrphan, 0, "Orphan proxies", "", 0, nil,
			func(s *HCJobStore, job *HCJob) {})
		done <- e
	}()

	select {
	case e := <-done:
		if e != ErrQueueFull {
			t.Fatalf("second create error = %v, want ErrQueueFull", e)
		}
	case <-time.After(time.Second):
		t.Fatal("second create blocked; it must return immediately when the queue is full")
	}
}

// TestHCPriorityQueue_Order verifies the heap choice order: priority first
// (HIGH before MEDIUM before LOW), FIFO within a level.
func TestHCPriorityQueue_Order(t *testing.T) {
	t0 := time.Now()
	q := newHCPriorityQueue(10, func() time.Time { return t0 })

	push := func(id string, p hcPriority, at time.Time) {
		if !q.enqueue(id, p, at) {
			t.Fatalf("enqueue %s: queue full", id)
		}
	}
	push("low1", hcPriorityLow, t0)
	push("med1", hcPriorityMedium, t0.Add(1*time.Minute))
	push("high1", hcPriorityHigh, t0.Add(2*time.Minute))
	push("med2", hcPriorityMedium, t0.Add(3*time.Minute))
	push("low2", hcPriorityLow, t0.Add(4*time.Minute))

	want := []struct {
		id   string
		prio hcPriority
	}{
		{"high1", hcPriorityHigh},
		{"med1", hcPriorityMedium},
		{"med2", hcPriorityMedium},
		{"low1", hcPriorityLow},
		{"low2", hcPriorityLow},
	}
	for i, w := range want {
		class := consumerMedLow
		if w.prio == hcPriorityHigh {
			class = consumerHigh
		}
		item, ok := q.popIf(context.Background(), class)
		if !ok {
			t.Fatalf("pop %d: queue drained early", i)
		}
		if item.id != w.id {
			t.Fatalf("pop %d = %q, want %q (FIFO within level / priority order broken)", i, item.id, w.id)
		}
	}
}

// TestHCPriorityQueue_Aging verifies that a LOW job that has waited longer
// than hcAgingAfter is promoted ahead of a newly enqueued MEDIUM job in the
// MEDIUM/LOW consumer, while a still-young LOW job does not.
func TestHCPriorityQueue_Aging(t *testing.T) {
	now := time.Now()
	q := newHCPriorityQueue(10, func() time.Time { return now })
	q.enqueue("old-low", hcPriorityLow, now.Add(-20*time.Minute)) // aged: 20m > 15m
	q.enqueue("new-med", hcPriorityMedium, now)

	item, ok := q.popIf(context.Background(), consumerMedLow)
	if !ok || item.id != "old-low" {
		t.Fatalf("popped %q (ok=%v), want the aged low job first", item.id, ok)
	}

	// Before the aging threshold the medium job goes first.
	q2 := newHCPriorityQueue(10, func() time.Time { return now })
	q2.enqueue("young-low", hcPriorityLow, now.Add(-10*time.Minute)) // 10m < 15m
	q2.enqueue("new-med", hcPriorityMedium, now)
	item2, ok2 := q2.popIf(context.Background(), consumerMedLow)
	if !ok2 || item2.id != "new-med" {
		t.Fatalf("popped %q (ok=%v), want the medium job before aging kicks in", item2.id, ok2)
	}
}

// TestHCPriorityQueue_Full verifies the capacity bound: enqueue is rejected
// (false) once the queue is at capacity.
func TestHCPriorityQueue_Full(t *testing.T) {
	q := newHCPriorityQueue(2, time.Now)
	if !q.enqueue("a", hcPriorityHigh, time.Now()) {
		t.Fatal("first enqueue should succeed")
	}
	if !q.enqueue("b", hcPriorityLow, time.Now()) {
		t.Fatal("second enqueue should succeed")
	}
	if q.enqueue("c", hcPriorityHigh, time.Now()) {
		t.Fatal("third enqueue should be rejected at capacity")
	}
}

// TestHCJobStore_HighStartsWhileMediumRunning verifies the two-consumer split:
// a HIGH (pool) job starts while a MEDIUM (orphan) job is still running.
func TestHCJobStore_HighStartsWhileMediumRunning(t *testing.T) {
	store := newHCJobStore(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.Start(ctx, nil)

	mediumStarted := make(chan struct{})
	releaseMedium := make(chan struct{})
	if _, err := store.Create(HCJobKindOrphan, 0, "Orphan proxies", "", 0, nil, func(s *HCJobStore, job *HCJob) {
		close(mediumStarted)
		<-releaseMedium
	}); err != nil {
		t.Fatalf("create medium job: %v", err)
	}

	select {
	case <-mediumStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("medium job did not start")
	}

	highDone := make(chan struct{})
	if _, err := store.Create(HCJobKindPool, 1, "Pool #1", "", 5, nil, func(s *HCJobStore, job *HCJob) {
		s.finishDone(job.ID)
		close(highDone)
	}); err != nil {
		t.Fatalf("create high job: %v", err)
	}

	select {
	case <-highDone:
	case <-time.After(5 * time.Second):
		t.Fatal("HIGH job did not start while the MEDIUM job was running")
	}
	close(releaseMedium)
}

// TestHCJobStore_ConsumersStopWithCtx verifies both consumers stop when the
// context is cancelled: jobs enqueued afterwards are never picked up.
func TestHCJobStore_ConsumersStopWithCtx(t *testing.T) {
	store := newHCJobStore(8)
	ctx, cancel := context.WithCancel(context.Background())
	store.Start(ctx, nil)
	cancel()

	done := make(chan struct{})
	if _, err := store.Create(HCJobKindPool, 1, "Pool #1", "", 5, nil, func(s *HCJobStore, job *HCJob) {
		close(done)
	}); err != nil {
		t.Fatalf("create high job: %v", err)
	}
	if _, err := store.Create(HCJobKindIdle, 0, "Idle orphan proxies", "", 0, nil, func(s *HCJobStore, job *HCJob) {
		close(done)
	}); err != nil {
		t.Fatalf("create low job: %v", err)
	}

	select {
	case <-done:
		t.Fatal("a consumer ran a job after its context was cancelled")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestHCJobStore_FindActiveByKind(t *testing.T) {
	store := newHCJobStore(8)
	now := time.Now()

	store.mu.Lock()
	store.jobs["done"] = &HCJob{ID: "done", Kind: HCJobKindForceCleanup, Status: HCJobDone, StartedAt: now.Add(2 * time.Minute)}
	store.jobs["running"] = &HCJob{ID: "running", Kind: HCJobKindForceCleanup, Status: HCJobRunning, StartedAt: now}
	store.jobs["other"] = &HCJob{ID: "other", Kind: HCJobKindPool, Status: HCJobRunning, StartedAt: now.Add(3 * time.Minute)}
	store.mu.Unlock()

	if j, ok := store.FindActiveByKind(HCJobKindForceCleanup); !ok || j.ID != "running" {
		t.Fatalf("FindActiveByKind = (%v, %v), want the running job", j, ok)
	}

	// A newer pending job of the same kind takes precedence.
	store.mu.Lock()
	store.jobs["pending"] = &HCJob{ID: "pending", Kind: HCJobKindForceCleanup, Status: HCJobPending, StartedAt: now.Add(4 * time.Minute)}
	store.mu.Unlock()

	if j, ok := store.FindActiveByKind(HCJobKindForceCleanup); !ok || j.ID != "pending" {
		t.Fatalf("FindActiveByKind = (%v, %v), want the newest pending job", j, ok)
	}

	if _, ok := store.FindActiveByKind(HCJobKindProxy); ok {
		t.Fatal("FindActiveByKind found a job for a kind with no active jobs")
	}
}

func TestHCJobStore_FindActiveCovering(t *testing.T) {
	store := newHCJobStore(8)
	now := time.Now()

	store.mu.Lock()
	store.jobs["first"] = &HCJob{ID: "first", Kind: HCJobKindProxy, Status: HCJobRunning,
		ProxyIDs: []int{1, 2}, StartedAt: now}
	store.jobs["second"] = &HCJob{ID: "second", Kind: HCJobKindProxy, Status: HCJobPending,
		ProxyIDs: []int{2, 3}, StartedAt: now.Add(time.Minute)}
	store.jobs["disjoint"] = &HCJob{ID: "disjoint", Kind: HCJobKindProxy, Status: HCJobRunning,
		ProxyIDs: []int{40, 50}, StartedAt: now.Add(2 * time.Minute)}
	store.jobs["done"] = &HCJob{ID: "done", Kind: HCJobKindProxy, Status: HCJobDone,
		ProxyIDs: []int{1, 2, 3}, StartedAt: now.Add(3 * time.Minute)}
	store.jobs["other-kind"] = &HCJob{ID: "other-kind", Kind: HCJobKindOrphan, Status: HCJobRunning,
		StartedAt: now.Add(4 * time.Minute)}
	store.mu.Unlock()

	// Only the first job covers {1}.
	if j, ok := store.FindActiveCovering([]int{1}); !ok || j.ID != "first" {
		t.Fatalf("FindActiveCovering([1]) = (%v, %v), want the first job", j, ok)
	}
	// Both cover {2}; the newest covering job wins.
	if j, ok := store.FindActiveCovering([]int{2}); !ok || j.ID != "second" {
		t.Fatalf("FindActiveCovering([2]) = (%v, %v), want the newest covering job", j, ok)
	}
	// No single job covers {1,3}.
	if _, ok := store.FindActiveCovering([]int{1, 3}); ok {
		t.Fatal("FindActiveCovering([1,3]) found a covering job, want none")
	}
	// Nothing covers 9.
	if _, ok := store.FindActiveCovering([]int{9}); ok {
		t.Fatal("FindActiveCovering([9]) found a covering job, want none")
	}
	// An empty request matches nothing.
	if _, ok := store.FindActiveCovering(nil); ok {
		t.Fatal("FindActiveCovering(nil) found a covering job, want none")
	}
}

func TestHCJobStore_queuePending(t *testing.T) {
	store := newHCJobStore(8)

	store.mu.Lock()
	store.jobs["pending"] = &HCJob{ID: "pending", Kind: HCJobKindOrphan, Status: HCJobPending, Total: 10}
	store.jobs["running"] = &HCJob{ID: "running", Kind: HCJobKindOrphan, Status: HCJobRunning, Total: 20, Progress: 5}
	store.jobs["done"] = &HCJob{ID: "done", Kind: HCJobKindOrphan, Status: HCJobDone, Total: 30}
	store.mu.Unlock()

	// pending -> Total (10); running -> Total - Progress (15); done -> 0.
	if got := store.QueuePending(); got != 25 {
		t.Fatalf("QueuePending = %d, want 25", got)
	}
}

func TestHCJobStore_FindActivePoolJob(t *testing.T) {
	store := newHCJobStore(8)
	now := time.Now()

	store.mu.Lock()
	store.jobs["done"] = &HCJob{ID: "done", Kind: HCJobKindPool, PoolID: 7, Status: HCJobDone, StartedAt: now.Add(3 * time.Minute)}
	store.jobs["running"] = &HCJob{ID: "running", Kind: HCJobKindPool, PoolID: 7, Status: HCJobRunning, StartedAt: now}
	store.jobs["other-pool"] = &HCJob{ID: "other-pool", Kind: HCJobKindPool, PoolID: 8, Status: HCJobRunning, StartedAt: now.Add(4 * time.Minute)}
	store.mu.Unlock()

	if j, ok := store.FindActivePoolJob(7); !ok || j.ID != "running" {
		t.Fatalf("FindActivePoolJob(7) = (%v, %v), want the running job", j, ok)
	}

	// A newer pending job for the same pool takes precedence.
	store.mu.Lock()
	store.jobs["pending"] = &HCJob{ID: "pending", Kind: HCJobKindPool, PoolID: 7, Status: HCJobPending, StartedAt: now.Add(5 * time.Minute)}
	store.mu.Unlock()

	if j, ok := store.FindActivePoolJob(7); !ok || j.ID != "pending" {
		t.Fatalf("FindActivePoolJob(7) = (%v, %v), want the newest pending job", j, ok)
	}

	// A running job for a different pool is returned for that pool.
	if j, ok := store.FindActivePoolJob(8); !ok || j.ID != "other-pool" {
		t.Fatalf("FindActivePoolJob(8) = (%v, %v), want the other pool's job", j, ok)
	}

	// No active job for pool 9.
	if _, ok := store.FindActivePoolJob(9); ok {
		t.Fatal("FindActivePoolJob(9) found a job, want none")
	}
}

// fakeOrphanChecker is a scripted orphanChecker for unit tests.
type fakeOrphanChecker struct {
	calls atomic.Int32
}

func (f *fakeOrphanChecker) CheckAllProxies(ctx context.Context) ([]models.ProxyTestResult, error) {
	f.calls.Add(1)
	return nil, nil
}

// fakeIdleChecker is a scripted idleOrphanCheckerWithProgress for unit tests.
type fakeIdleChecker struct {
	calls atomic.Int32
}

func (f *fakeIdleChecker) CheckOrphanIdleProxiesWithProgress(
	ctx context.Context,
	onProgress func(checked, active, failed int),
	immediate bool,
	jobID string,
) ([]models.ProxyTestResult, error) {
	f.calls.Add(1)
	return nil, nil
}

// TestRunPoolHealthCheckAsync_Dedup verifies the pool sweep guard: with an
// in-flight pool job, a repeat trigger for the same pool returns it instead
// of queueing a duplicate sweep.
func TestRunPoolHealthCheckAsync_Dedup(t *testing.T) {
	ensureGlobalJobStoreQueue(t)
	store := GetJobStore()

	store.mu.Lock()
	store.jobs["existing-pool"] = &HCJob{ID: "existing-pool", Kind: HCJobKindPool, PoolID: 7,
		PoolName: "p7", Status: HCJobRunning, StartedAt: time.Now()}
	store.mu.Unlock()
	t.Cleanup(func() {
		store.Update("existing-pool", func(j *HCJob) { j.Status = HCJobDone })
	})

	job, err := RunPoolHealthCheckAsync(context.Background(), nil, 7, "p7", "", 0)
	if err != nil {
		t.Fatalf("RunPoolHealthCheckAsync: %v", err)
	}
	if job.ID != "existing-pool" {
		t.Fatalf("job = %s, want the existing job (no duplicate queued)", job.ID)
	}
	if jobs := store.ListByPool(7); len(jobs) != 1 {
		t.Fatalf("pool 7 jobs = %d, want 1", len(jobs))
	}
}

// TestRunOrphanHealthCheckAsync_Dedup verifies the orphan sweep guard: a
// repeat trigger returns the in-flight orphan job instead of queueing a
// duplicate sweep.
func TestRunOrphanHealthCheckAsync_Dedup(t *testing.T) {
	ensureGlobalJobStoreQueue(t)
	store := GetJobStore()
	fake := &fakeOrphanChecker{}

	first, err := RunOrphanHealthCheckAsync(context.Background(), fake, true)
	if err != nil {
		t.Fatalf("first RunOrphanHealthCheckAsync: %v", err)
	}
	second, err := RunOrphanHealthCheckAsync(context.Background(), fake, true)
	if err != nil {
		t.Fatalf("second RunOrphanHealthCheckAsync: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("second trigger returned job %s, want the in-flight job %s", second.ID, first.ID)
	}
	active := 0
	for _, j := range store.ListByKind(HCJobKindOrphan) {
		if j.Status == HCJobPending || j.Status == HCJobRunning {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active orphan jobs = %d, want 1 (no duplicate queued)", active)
	}
	t.Cleanup(func() {
		store.Update(first.ID, func(j *HCJob) { j.Status = HCJobDone })
	})
}

// TestRunIdleOrphanHealthCheckAsync_Dedup verifies the idle sweep guard: a
// repeat trigger returns the in-flight idle job instead of queueing a
// duplicate sweep.
func TestRunIdleOrphanHealthCheckAsync_Dedup(t *testing.T) {
	ensureGlobalJobStoreQueue(t)
	store := GetJobStore()
	fake := &fakeIdleChecker{}

	first, err := RunIdleOrphanHealthCheckAsync(context.Background(), fake, true)
	if err != nil {
		t.Fatalf("first RunIdleOrphanHealthCheckAsync: %v", err)
	}
	second, err := RunIdleOrphanHealthCheckAsync(context.Background(), fake, true)
	if err != nil {
		t.Fatalf("second RunIdleOrphanHealthCheckAsync: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("second trigger returned job %s, want the in-flight job %s", second.ID, first.ID)
	}
	active := 0
	for _, j := range store.ListByKind(HCJobKindIdle) {
		if j.Status == HCJobPending || j.Status == HCJobRunning {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active idle jobs = %d, want 1 (no duplicate queued)", active)
	}
	t.Cleanup(func() {
		store.Update(first.ID, func(j *HCJob) { j.Status = HCJobDone })
	})
}

// fakeProxyIDsChecker is a scripted proxyIDsCheckerWithProgress for unit tests.
type fakeProxyIDsChecker struct {
	results []models.ProxyTestResult
	err     error
	calls   atomic.Int32
}

func (f *fakeProxyIDsChecker) CheckProxiesWithProgress(
	ctx context.Context,
	proxyIDs []int,
	onProgress func(checked, active, failed int),
	immediate bool,
	jobID string,
) ([]models.ProxyTestResult, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

// TestRunProxyHealthCheckAsync verifies the bulk "test selected" job: it is
// created with kind=proxy, the selected ids, and a default worker count; the
// runner applies the checker results and finishes the job done.
func TestRunProxyHealthCheckAsync(t *testing.T) {
	// RunProxyHealthCheckAsync enqueues on the global store; give it a queue.
	ensureGlobalJobStoreQueue(t)
	store := GetJobStore()

	fake := &fakeProxyIDsChecker{results: []models.ProxyTestResult{
		{ID: 1, Status: "active"},
		{ID: 2, Status: "active"},
		{ID: 3, Status: "failed"},
	}}

	job, err := RunProxyHealthCheckAsync(context.Background(), fake, []int{1, 2, 3}, 0)
	if err != nil {
		t.Fatalf("RunProxyHealthCheckAsync: %v", err)
	}
	if job.Kind != HCJobKindProxy {
		t.Fatalf("job kind = %s, want proxy", job.Kind)
	}
	if job.Total != 3 {
		t.Fatalf("job total = %d, want 3", job.Total)
	}
	if len(job.ProxyIDs) != 3 {
		t.Fatalf("job proxy_ids = %v, want 3 ids", job.ProxyIDs)
	}
	if job.Workers != 20 {
		t.Fatalf("job workers = %d, want default 20", job.Workers)
	}

	// Run the runner directly (deterministic; the queue consumer may also run
	// it, which is harmless for a scripted fake).
	job.run(store, job)

	got, ok := store.Get(job.ID)
	if !ok {
		t.Fatal("job not found after run")
	}
	if got.Status != HCJobDone {
		t.Fatalf("status = %s, want done (error: %s)", got.Status, got.Error)
	}
	if got.Active != 2 || got.Failed != 1 {
		t.Fatalf("active/failed = %d/%d, want 2/1", got.Active, got.Failed)
	}
	if got.Progress != 3 || got.Total != 3 {
		t.Fatalf("progress/total = %d/%d, want 3/3", got.Progress, got.Total)
	}
	if fake.calls.Load() == 0 {
		t.Fatal("checker was never called")
	}
}
