package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

type mockFailedProxyRepo struct {
	count   int
	countErr error
	deleteN int
	deleteErr error
}

func (m *mockFailedProxyRepo) CountFailedProxies(ctx context.Context) (int, error) {
	return m.count, m.countErr
}

func (m *mockFailedProxyRepo) DeleteFailedProxies(ctx context.Context) (int, error) {
	return m.deleteN, m.deleteErr
}

func TestRunForceCleanupAsync_zeroFailedCompletesImmediately(t *testing.T) {
	store := &HCJobStore{jobs: make(map[string]*HCJob)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.Start(ctx, nil, nil, nil, nil)

	job, err := runForceCleanupAsyncOn(store, ctx, &mockFailedProxyRepo{count: 0})
	if err != nil {
		t.Fatalf("RunForceCleanupAsync: %v", err)
	}
	if job.Total != 0 {
		t.Fatalf("total = %d, want 0", job.Total)
	}

	deadline := time.After(3 * time.Second)
	for {
		j, ok := store.Get(job.ID)
		if !ok {
			t.Fatal("job missing")
		}
		if j.Status == HCJobDone {
			if j.Progress != 0 {
				t.Fatalf("progress = %d, want 0", j.Progress)
			}
			return
		}
		if j.Status == HCJobFailed {
			t.Fatalf("job failed: %s", j.Error)
		}
		select {
		case <-deadline:
			t.Fatalf("job stuck in %s", j.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestRunForceCleanupAsync_deletesFailedProxies(t *testing.T) {
	store := &HCJobStore{jobs: make(map[string]*HCJob)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.Start(ctx, nil, nil, nil, nil)

	repo := &mockFailedProxyRepo{count: 5, deleteN: 5}
	job, err := runForceCleanupAsyncOn(store, ctx, repo)
	if err != nil {
		t.Fatalf("RunForceCleanupAsync: %v", err)
	}
	if job.Total != 5 {
		t.Fatalf("total = %d, want 5", job.Total)
	}

	deadline := time.After(3 * time.Second)
	for {
		j, ok := store.Get(job.ID)
		if !ok {
			t.Fatal("job missing")
		}
		if j.Status == HCJobDone {
			if j.Progress != 5 {
				t.Fatalf("progress = %d, want 5", j.Progress)
			}
			return
		}
		if j.Status == HCJobFailed {
			t.Fatalf("job failed: %s", j.Error)
		}
		select {
		case <-deadline:
			t.Fatalf("job stuck in %s", j.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestRunForceCleanupAsync_deleteErrorMarksFailed(t *testing.T) {
	store := &HCJobStore{jobs: make(map[string]*HCJob)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.Start(ctx, nil, nil, nil, nil)

	job, err := runForceCleanupAsyncOn(store, ctx, &mockFailedProxyRepo{
		count:     2,
		deleteErr: errors.New("db down"),
	})
	if err != nil {
		t.Fatalf("RunForceCleanupAsync: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		j, ok := store.Get(job.ID)
		if !ok {
			t.Fatal("job missing")
		}
		if j.Status == HCJobFailed {
			if j.Error == "" {
				t.Fatal("expected error message")
			}
			return
		}
		if j.Status == HCJobDone {
			t.Fatal("expected failed job")
		}
		select {
		case <-deadline:
			t.Fatalf("job stuck in %s", j.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
