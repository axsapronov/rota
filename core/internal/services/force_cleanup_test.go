package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

type mockFailedProxyRepo struct {
	count      int
	countErr   error
	deleteN    int
	remaining  int
	initialized bool
	deleteErr  error
	batchCalls int
}

func (m *mockFailedProxyRepo) CountFailedProxies(ctx context.Context) (int, error) {
	return m.count, m.countErr
}

func (m *mockFailedProxyRepo) DeleteFailedProxiesBatch(ctx context.Context, batchSize int) (int, error) {
	m.batchCalls++
	if m.deleteErr != nil {
		return 0, m.deleteErr
	}
	if !m.initialized {
		m.initialized = true
		m.remaining = m.deleteN
	}
	if m.remaining <= 0 {
		return 0, nil
	}
	n := m.remaining
	if n > batchSize {
		n = batchSize
	}
	m.remaining -= n
	return n, nil
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

func TestRunForceCleanupAsync_deletesInBatches(t *testing.T) {
	store := &HCJobStore{jobs: make(map[string]*HCJob)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.Start(ctx, nil, nil, nil, nil)

	repo := &mockFailedProxyRepo{count: 250, deleteN: 250}
	job, err := runForceCleanupAsyncOn(store, ctx, repo)
	if err != nil {
		t.Fatalf("RunForceCleanupAsync: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		j, ok := store.Get(job.ID)
		if !ok {
			t.Fatal("job missing")
		}
		if j.Status == HCJobDone {
			if j.Progress != 250 {
				t.Fatalf("progress = %d, want 250", j.Progress)
			}
			if repo.batchCalls < 3 {
				t.Fatalf("batchCalls = %d, want at least 3", repo.batchCalls)
			}
			return
		}
		if j.Status == HCJobFailed {
			t.Fatalf("job failed: %s", j.Error)
		}
		select {
		case <-deadline:
			t.Fatalf("job stuck in %s progress=%d", j.Status, j.Progress)
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
