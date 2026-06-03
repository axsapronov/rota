package services

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestHCJobStore_consumerSurvivesJobPanic(t *testing.T) {
	store := &HCJobStore{jobs: make(map[string]*HCJob)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.Start(ctx, nil, nil, nil, nil)

	var panicked atomic.Bool
	done := make(chan struct{}, 1)

	store.Create(HCJobKindProxy, 0, "", "", 1, []int{1}, func(s *HCJobStore, job *HCJob) {
		if !panicked.Swap(true) {
			panic("test panic")
		}
		s.finishDone(job.ID)
	})

	store.Create(HCJobKindProxy, 0, "", "", 1, []int{2}, func(s *HCJobStore, job *HCJob) {
		s.finishDone(job.ID)
		close(done)
	})

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
	store := &HCJobStore{
		jobs: map[string]*HCJob{
			"stuck": {
				ID:     "stuck",
				Kind:   HCJobKindPool,
				Status: HCJobRunning,
			},
		},
	}
	store.queue = make(chan string, 4)
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
