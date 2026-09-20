package services

import (
	"context"
	"errors"
	"time"

	"github.com/alpkeskin/rota/core/internal/checkstats"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// forceCleanupBatchSize is the number of failed proxies deleted per batch.
const forceCleanupBatchSize = 100

// forceCleanupMaxBatches caps the number of batches a single force cleanup may
// run (10000 × 100 = 1,000,000 proxies). It is a var so tests can lower the
// cap to exercise the iteration-cap failure path.
var forceCleanupMaxBatches = 10000

// failedProxyStore is the repository surface ForceCleanupService uses. An
// interface (instead of *repository.ProxyRepository) keeps the service
// unit-testable without a database.
type failedProxyStore interface {
	CountFailedProxies(ctx context.Context) (int, error)
	DeleteFailedProxiesBatch(ctx context.Context, batchSize int) (int, error)
}

// ForceCleanupService deletes all failed proxies in batches, tracked as a job
// on the shared health-check job queue.
type ForceCleanupService struct {
	repo failedProxyStore
	log  *logger.Logger
}

// NewForceCleanupService creates a new ForceCleanupService.
func NewForceCleanupService(repo *repository.ProxyRepository, log *logger.Logger) *ForceCleanupService {
	return &ForceCleanupService{repo: repo, log: log}
}

// StartForceCleanup enqueues a force cleanup job. It returns:
//   - the existing job and alreadyRunning=true when a force cleanup is already
//     pending/running (in-flight guard; no duplicate job is created);
//   - a new job and alreadyRunning=false when one was created;
//   - (nil, false, nil) when there are no failed proxies to clean.
//
// ErrQueueFull is returned unchanged when the job queue is at capacity.
func (s *ForceCleanupService) StartForceCleanup(ctx context.Context) (*HCJob, bool, error) {
	store := GetJobStore()
	if job, ok := store.FindActiveByKind(HCJobKindForceCleanup); ok {
		return job, true, nil
	}

	total, err := s.repo.CountFailedProxies(ctx)
	if err != nil {
		return nil, false, err
	}
	if total == 0 {
		return nil, false, nil
	}

	job, err := store.Create(HCJobKindForceCleanup, 0, "Force cleanup", "", 0, nil, s.runner)
	if err != nil {
		return nil, false, err
	}
	store.Update(job.ID, func(j *HCJob) {
		j.Total = total
	})
	return job, false, nil
}

// runner executes a force cleanup job: batch deletes until nothing is left,
// reporting progress after each batch.
func (s *ForceCleanupService) runner(store *HCJobStore, job *HCJob) {
	// Detached context: a client disconnect must not kill the cleanup.
	ctx := context.Background()
	deleted := 0
	done := false

	for i := 0; i < forceCleanupMaxBatches; i++ {
		n, err := s.repo.DeleteFailedProxiesBatch(ctx, forceCleanupBatchSize)
		if err != nil {
			s.log.Error("force cleanup: batch delete failed", "error", err)
			store.finishFailed(job.ID, err)
			s.recordSnapshot(store, job, checkstats.CleanupStatusError, err, deleted)
			return
		}
		if n == 0 {
			done = true
			break
		}
		deleted += n
		store.Update(job.ID, func(j *HCJob) {
			progress := deleted
			if progress > j.Total {
				progress = j.Total
			}
			j.Progress = progress
		})
		s.log.Info("force cleanup batch", "batch", i, "deleted", n, "total_deleted", deleted)
	}

	if !done {
		capErr := errors.New("force cleanup iteration cap reached")
		s.log.Error("force cleanup: iteration cap reached", "deleted", deleted)
		store.finishFailed(job.ID, capErr)
		s.recordSnapshot(store, job, checkstats.CleanupStatusError, capErr, deleted)
		return
	}

	store.Update(job.ID, func(j *HCJob) {
		progress := deleted
		if progress > j.Total {
			progress = j.Total
		}
		j.Progress = progress
	})
	store.finishDone(job.ID)
	s.recordSnapshot(store, job, checkstats.CleanupStatusOK, nil, deleted)
}

// recordSnapshot stores the force-cleanup outcome in the cleanup metrics.
func (s *ForceCleanupService) recordSnapshot(store *HCJobStore, job *HCJob, status checkstats.CleanupStatus, runErr error, deleted int) {
	now := time.Now()
	snap := checkstats.ForceCleanupSnapshot{
		LastRunAt:      &now,
		LastStatus:     status,
		LastDurationMs: now.Sub(job.StartedAt).Milliseconds(),
		DeletedProxies: deleted,
	}
	if runErr != nil {
		snap.LastError = runErr.Error()
	}
	checkstats.RecordForceCleanup(snap)
}
