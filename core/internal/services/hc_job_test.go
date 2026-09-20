package services

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
)

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

func TestHCJobStore_requeueRunningOnConsumerRestart(t *testing.T) {
	store := newHCJobStore(4)
	store.jobs["stuck"] = &HCJob{
		ID:     "stuck",
		Kind:   HCJobKindPool,
		Status: HCJobRunning,
	}
	store.ctx = context.Background()

	store.requeueInterruptedJobs()

	select {
	case id := <-store.queue:
		if id != "stuck" {
			t.Fatalf("requeued id = %q, want stuck", id)
		}
	case <-time.After(time.Second):
		t.Fatal("running job was not requeued")
	}

	j, ok := store.Get("stuck")
	if !ok || j.Status != HCJobPending {
		t.Fatalf("job status = %v, want pending", j.Status)
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
