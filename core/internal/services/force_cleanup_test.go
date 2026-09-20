package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alpkeskin/rota/core/internal/checkstats"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// fakeFailedProxyStore is a scripted failedProxyStore for unit tests.
// batchErrs maps a batch index (0-based) to an error to return for that call;
// batchResults is the script of deleted counts, with the last value repeating.
type fakeFailedProxyStore struct {
	count        int
	countErr     error
	batchResults []int
	batchErrs    map[int]error
	batchCalls   int
}

func (f *fakeFailedProxyStore) CountFailedProxies(ctx context.Context) (int, error) {
	return f.count, f.countErr
}

func (f *fakeFailedProxyStore) DeleteFailedProxiesBatch(ctx context.Context, batchSize int) (int, error) {
	if err, ok := f.batchErrs[f.batchCalls]; ok {
		return 0, err
	}
	idx := f.batchCalls
	if len(f.batchResults) > 0 && idx >= len(f.batchResults) {
		idx = len(f.batchResults) - 1
	}
	f.batchCalls++
	if len(f.batchResults) == 0 {
		return 0, nil
	}
	return f.batchResults[idx], nil
}

// ensureGlobalJobStoreQueue makes the global job store's queue usable for
// StartForceCleanup tests. The consumer may exit when the test context is
// cancelled; the queue itself remains, so later enqueues succeed.
func ensureGlobalJobStoreQueue(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	GetJobStore().Start(ctx, nil)
	t.Cleanup(cancel)
}

func TestForceCleanup_Start_NoFailedProxies(t *testing.T) {
	ensureGlobalJobStoreQueue(t)

	svc := &ForceCleanupService{repo: &fakeFailedProxyStore{count: 0}, log: logger.New("error")}
	job, alreadyRunning, err := svc.StartForceCleanup(context.Background())
	if err != nil {
		t.Fatalf("StartForceCleanup error = %v, want nil", err)
	}
	if job != nil {
		t.Fatalf("job = %+v, want nil (nothing to clean)", job)
	}
	if alreadyRunning {
		t.Fatal("alreadyRunning = true, want false")
	}
}

func TestForceCleanup_Start_CountError(t *testing.T) {
	ensureGlobalJobStoreQueue(t)

	dbErr := errors.New("db down")
	svc := &ForceCleanupService{repo: &fakeFailedProxyStore{countErr: dbErr}, log: logger.New("error")}
	job, alreadyRunning, err := svc.StartForceCleanup(context.Background())
	if !errors.Is(err, dbErr) {
		t.Fatalf("error = %v, want the count error", err)
	}
	if job != nil || alreadyRunning {
		t.Fatalf("job/alreadyRunning = (%v, %v), want (nil, false)", job, alreadyRunning)
	}
}

func TestForceCleanup_Start_AlreadyRunning(t *testing.T) {
	ensureGlobalJobStoreQueue(t)
	store := GetJobStore()

	// A force cleanup is already in flight.
	existing, err := store.Create(HCJobKindForceCleanup, 0, "Force cleanup", "", 0, nil,
		func(s *HCJobStore, job *HCJob) {})
	if err != nil {
		t.Fatalf("create existing job: %v", err)
	}
	store.Update(existing.ID, func(j *HCJob) { j.Status = HCJobRunning })
	// Mark done afterwards so later tests' in-flight guard is not affected.
	t.Cleanup(func() {
		store.Update(existing.ID, func(j *HCJob) { j.Status = HCJobDone })
	})

	svc := &ForceCleanupService{repo: &fakeFailedProxyStore{count: 5}, log: logger.New("error")}
	job, alreadyRunning, err := svc.StartForceCleanup(context.Background())
	if err != nil {
		t.Fatalf("StartForceCleanup error = %v, want nil", err)
	}
	if !alreadyRunning {
		t.Fatal("alreadyRunning = false, want true")
	}
	if job == nil || job.ID != existing.ID {
		t.Fatalf("job = %+v, want the existing job %s", job, existing.ID)
	}
	if jobs := store.ListByKind(HCJobKindForceCleanup); len(jobs) != 1 {
		t.Fatalf("force_cleanup jobs = %d, want 1 (no duplicate created)", len(jobs))
	}
}

func TestForceCleanup_Start_CreatesJob(t *testing.T) {
	ensureGlobalJobStoreQueue(t)
	store := GetJobStore()

	svc := &ForceCleanupService{
		repo: &fakeFailedProxyStore{count: 250, batchResults: []int{0}},
		log:  logger.New("error"),
	}
	job, alreadyRunning, err := svc.StartForceCleanup(context.Background())
	if err != nil {
		t.Fatalf("StartForceCleanup error = %v, want nil", err)
	}
	if alreadyRunning {
		t.Fatal("alreadyRunning = true, want false")
	}
	if job == nil {
		t.Fatal("job = nil, want a new job")
	}
	if job.Kind != HCJobKindForceCleanup {
		t.Fatalf("job kind = %s, want %s", job.Kind, HCJobKindForceCleanup)
	}
	if job.Total != 250 {
		t.Fatalf("job total = %d, want 250", job.Total)
	}
	// Mark done so later tests' in-flight guard is not affected.
	t.Cleanup(func() {
		store.Update(job.ID, func(j *HCJob) { j.Status = HCJobDone })
	})
}

func TestForceCleanup_Runner_BatchesUntilEmpty(t *testing.T) {
	store := newHCJobStore(8)
	fake := &fakeFailedProxyStore{batchResults: []int{100, 100, 0}}
	svc := &ForceCleanupService{repo: fake, log: logger.New("error")}

	job, err := store.Create(HCJobKindForceCleanup, 0, "Force cleanup", "", 0, nil,
		func(s *HCJobStore, j *HCJob) {})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	store.Update(job.ID, func(j *HCJob) { j.Total = 250 })

	svc.runner(store, job)

	got, ok := store.Get(job.ID)
	if !ok {
		t.Fatal("job not found after run")
	}
	if got.Status != HCJobDone {
		t.Fatalf("status = %s, want done", got.Status)
	}
	if got.Progress != 200 {
		t.Fatalf("progress = %d, want 200", got.Progress)
	}
	if m := checkstats.GetCleanupMetrics(); m.Force.LastStatus != checkstats.CleanupStatusOK || m.Force.DeletedProxies != 200 {
		t.Fatalf("force cleanup snapshot = %+v, want ok/200", m.Force)
	}
}

func TestForceCleanup_Runner_DBError(t *testing.T) {
	store := newHCJobStore(8)
	dbErr := errors.New("db down")
	fake := &fakeFailedProxyStore{batchResults: []int{100, 100}, batchErrs: map[int]error{1: dbErr}}
	svc := &ForceCleanupService{repo: fake, log: logger.New("error")}

	job, err := store.Create(HCJobKindForceCleanup, 0, "Force cleanup", "", 0, nil,
		func(s *HCJobStore, j *HCJob) {})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	store.Update(job.ID, func(j *HCJob) { j.Total = 250 })

	svc.runner(store, job)

	got, ok := store.Get(job.ID)
	if !ok {
		t.Fatal("job not found after run")
	}
	if got.Status != HCJobFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "db down") {
		t.Fatalf("error = %q, want it to contain %q", got.Error, "db down")
	}
	if m := checkstats.GetCleanupMetrics(); m.Force.LastStatus != checkstats.CleanupStatusError || m.Force.LastError == "" {
		t.Fatalf("force cleanup snapshot = %+v, want error with last error", m.Force)
	}
}

func TestForceCleanup_Runner_IterationCap(t *testing.T) {
	old := forceCleanupMaxBatches
	forceCleanupMaxBatches = 2
	defer func() { forceCleanupMaxBatches = old }()

	store := newHCJobStore(8)
	// Never an empty batch: the cap is hit before the work is done.
	fake := &fakeFailedProxyStore{batchResults: []int{100, 100, 100}}
	svc := &ForceCleanupService{repo: fake, log: logger.New("error")}

	job, err := store.Create(HCJobKindForceCleanup, 0, "Force cleanup", "", 0, nil,
		func(s *HCJobStore, j *HCJob) {})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	store.Update(job.ID, func(j *HCJob) { j.Total = 1000 })

	svc.runner(store, job)

	got, ok := store.Get(job.ID)
	if !ok {
		t.Fatal("job not found after run")
	}
	if got.Status != HCJobFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "iteration cap") {
		t.Fatalf("error = %q, want it to mention the iteration cap", got.Error)
	}
}

func TestForceCleanup_Runner_ProgressClampedToTotal(t *testing.T) {
	store := newHCJobStore(8)
	fake := &fakeFailedProxyStore{batchResults: []int{100, 100, 0}}
	svc := &ForceCleanupService{repo: fake, log: logger.New("error")}

	job, err := store.Create(HCJobKindForceCleanup, 0, "Force cleanup", "", 0, nil,
		func(s *HCJobStore, j *HCJob) {})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	// More deleted than the counted total (proxies failed in the meantime):
	// progress must clamp to the total.
	store.Update(job.ID, func(j *HCJob) { j.Total = 150 })

	svc.runner(store, job)

	got, ok := store.Get(job.ID)
	if !ok {
		t.Fatal("job not found after run")
	}
	if got.Status != HCJobDone {
		t.Fatalf("status = %s, want done", got.Status)
	}
	if got.Progress != 150 {
		t.Fatalf("progress = %d, want 150 (clamped to total)", got.Progress)
	}
}
