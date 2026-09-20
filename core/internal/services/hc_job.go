package services

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/google/uuid"
)

// HCJobStatus represents the state of a health-check job
type HCJobStatus string

const (
	HCJobPending HCJobStatus = "pending"
	HCJobRunning HCJobStatus = "running"
	HCJobDone    HCJobStatus = "done"
	HCJobFailed  HCJobStatus = "failed"
)

// HCJobKind describes the health-check trigger type.
type HCJobKind string

const (
	HCJobKindPool   HCJobKind = "pool"
	HCJobKindProxy  HCJobKind = "proxy"
	HCJobKindOrphan HCJobKind = "orphan"
	HCJobKindIdle   HCJobKind = "idle"
	// HCJobKindForceCleanup is reserved for the force-cleanup feature; the
	// constant is declared here so job records are already typed, but no runner
	// is implemented in this change.
	HCJobKindForceCleanup HCJobKind = "force_cleanup"
)

// ErrQueueFull is returned by Create when the job queue is at capacity. The API
// layer maps it to HTTP 429 (backpressure) instead of blocking.
var ErrQueueFull = errors.New("health check queue is full")

// Injection interfaces. HealthChecker is passed as one of these and the
// concrete capabilities are detected via type assertion, so the queue layer
// stays decoupled from the proxy package.
type orphanChecker interface {
	CheckAllProxies(ctx context.Context) ([]models.ProxyTestResult, error)
}

// orphanCheckerWithProgress matches HealthChecker.CheckAllProxiesWithProgress.
// The third (immediate) parameter is required: periodic scheduler runs use
// immediate=false (consecutive-failure hysteresis), user-initiated runs use
// immediate=true.
type orphanCheckerWithProgress interface {
	CheckAllProxiesWithProgress(ctx context.Context, onProgress func(checked, active, failed int), immediate bool) ([]models.ProxyTestResult, error)
}

type orphanCounter interface {
	CountOrphanProxies(ctx context.Context) (int, error)
}

type idleOrphanCounter interface {
	CountOrphanIdleProxies(ctx context.Context) (int, error)
}

type idleOrphanCheckerWithProgress interface {
	CheckOrphanIdleProxiesWithProgress(ctx context.Context, onProgress func(checked, active, failed int), immediate bool) ([]models.ProxyTestResult, error)
}

// proxyIDsCheckerWithProgress matches HealthChecker.CheckProxiesWithProgress
// (user-initiated bulk health check of selected proxies).
type proxyIDsCheckerWithProgress interface {
	CheckProxiesWithProgress(ctx context.Context, proxyIDs []int, onProgress func(checked, active, failed int), immediate bool) ([]models.ProxyTestResult, error)
}

// HCJob holds state for one async health-check run
type HCJob struct {
	ID         string      `json:"id"`
	Kind       HCJobKind   `json:"kind"`
	PoolID     int         `json:"pool_id"`
	PoolName   string      `json:"pool_name"`
	Status     HCJobStatus `json:"status"`
	Progress   int         `json:"progress"` // checked so far
	Total      int         `json:"total"`    // total proxies
	Active     int         `json:"active"`
	Failed     int         `json:"failed"`
	CheckURL   string      `json:"check_url"`
	Workers    int         `json:"workers"`
	Error      string      `json:"error,omitempty"`
	StartedAt  time.Time   `json:"started_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
	ProxyIDs   []int       `json:"proxy_ids,omitempty"`
	// Full results (populated when done)
	Results []models.ProxyTestResult `json:"results,omitempty"`

	// run is the job's runner, invoked by the single queue consumer.
	run func(*HCJobStore, *HCJob)
}

// HCJobStore keeps an in-memory map of recent jobs (TTL 30 min) plus a single
// FIFO queue consumed by one goroutine. The queue provides backpressure: when
// it is full, new jobs are rejected (ErrQueueFull) rather than blocking.
type HCJobStore struct {
	mu        sync.RWMutex
	jobs      map[string]*HCJob
	queue     chan string
	startOnce sync.Once
	ctx       context.Context
	logger    *logger.Logger
}

var globalJobStore = &HCJobStore{
	jobs: make(map[string]*HCJob),
}

// GetJobStore returns the singleton job store
func GetJobStore() *HCJobStore {
	return globalJobStore
}

// newHCJobStore builds a store with a bounded queue for tests. The production
// singleton uses the default queue capacity set in Start.
func newHCJobStore(queueCap int) *HCJobStore {
	return &HCJobStore{
		jobs:  make(map[string]*HCJob),
		queue: make(chan string, queueCap),
	}
}

// Start initializes the queue consumer once. The consumer lives for the given
// context's lifetime (wired to the service context in server.go so it stops on
// shutdown). If a queue was pre-created (test store) it is reused.
func (s *HCJobStore) Start(ctx context.Context, log *logger.Logger) {
	s.startOnce.Do(func() {
		s.ctx = ctx
		s.logger = log
		if s.queue == nil {
			s.queue = make(chan string, 2048)
		}
		go s.runConsumerLoop()
	})
}

// runConsumerLoop runs the consumer, restarting it if it exits for any reason
// other than the context being done. On an unexpected exit, in-flight (running)
// jobs are requeued so they are not lost.
func (s *HCJobStore) runConsumerLoop() {
	first := true
	for {
		if s.ctx.Err() != nil {
			return
		}
		if !first {
			s.requeueInterruptedJobs()
		}
		first = false
		s.consume(s.ctx)
		if s.ctx.Err() != nil {
			return
		}
		if s.logger != nil {
			s.logger.Warn("health check queue consumer exited unexpectedly, restarting", "delay", "1s")
		}
		time.Sleep(time.Second)
	}
}

// Create registers a new job and enqueues it non-blockingly. When the queue is
// full the job is rolled back and ErrQueueFull is returned (the caller maps it
// to HTTP 429). A job is never created on a blocking enqueue.
func (s *HCJobStore) Create(kind HCJobKind, poolID int, poolName, checkURL string, workers int, proxyIDs []int, runFn func(*HCJobStore, *HCJob)) (*HCJob, error) {
	job := &HCJob{
		ID:        uuid.New().String(),
		Kind:      kind,
		PoolID:    poolID,
		PoolName:  poolName,
		Status:    HCJobPending,
		CheckURL:  checkURL,
		Workers:   workers,
		ProxyIDs:  proxyIDs,
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
		run:       runFn,
	}
	s.mu.Lock()
	s.jobs[job.ID] = job
	s.mu.Unlock()

	if !s.enqueue(job.ID) {
		s.mu.Lock()
		delete(s.jobs, job.ID)
		s.mu.Unlock()
		return nil, ErrQueueFull
	}

	// Cleanup old jobs in background.
	go s.cleanup()
	return job, nil
}

// enqueue pushes a job id onto the queue without blocking. It reports whether
// the enqueue succeeded (false when the queue is nil or full).
func (s *HCJobStore) enqueue(jobID string) bool {
	if s.queue == nil {
		return false
	}
	select {
	case s.queue <- jobID:
		return true
	default:
		return false
	}
}

// requeueInterruptedJobs puts running jobs back on the queue after the consumer
// goroutine exits, so an interrupted run is retried rather than dropped.
func (s *HCJobStore) requeueInterruptedJobs() {
	if s.queue == nil {
		return
	}
	s.mu.RLock()
	ids := make([]string, 0)
	for id, j := range s.jobs {
		if j.Status == HCJobRunning {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()

	for _, id := range ids {
		s.Update(id, func(j *HCJob) {
			j.Status = HCJobPending
		})
		s.enqueue(id)
	}
}

// Get returns a job by ID
func (s *HCJobStore) Get(id string) (*HCJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

// Update mutates a job (caller must hold no lock)
func (s *HCJobStore) Update(id string, fn func(*HCJob)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		fn(j)
		j.UpdatedAt = time.Now()
	}
}

// cleanup removes finished jobs that completed more than 30 minutes ago.
// Jobs that are still pending/running are never evicted (even if long-running),
// otherwise status polling on a slow health-check would return "not found"
// while the goroutine is still updating the (now deleted) record.
func (s *HCJobStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-30 * time.Minute)
	for id, j := range s.jobs {
		// Don't evict jobs that are still in flight.
		if j.Status == HCJobPending || j.Status == HCJobRunning {
			continue
		}
		// Evict based on when the job finished (fall back to last update).
		last := j.UpdatedAt
		if j.FinishedAt != nil {
			last = *j.FinishedAt
		}
		if last.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}

// QueuePending returns the number of proxies not yet checked across pending and
// running jobs: pending jobs contribute their full total, running jobs
// contribute the remaining (total - progress).
func (s *HCJobStore) QueuePending() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, j := range s.jobs {
		switch j.Status {
		case HCJobPending:
			total += j.Total
		case HCJobRunning:
			remaining := j.Total - j.Progress
			if remaining > 0 {
				total += remaining
			}
		}
	}
	return total
}

// ListByPool returns all jobs for a given pool (newest first)
func (s *HCJobStore) ListByPool(poolID int) []*HCJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*HCJob
	for _, j := range s.jobs {
		if j.PoolID == poolID {
			out = append(out, j)
		}
	}
	// sort newest first
	sort.Slice(out, func(i, k int) bool {
		return out[i].StartedAt.After(out[k].StartedAt)
	})
	return out
}

// ListByKind returns all jobs for a kind (newest first).
func (s *HCJobStore) ListByKind(kind HCJobKind) []*HCJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*HCJob
	for _, j := range s.jobs {
		if j.Kind == kind {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].StartedAt.After(out[k].StartedAt)
	})
	return out
}

// FindActiveByKind returns the most recent pending/running job of the given
// kind, for in-flight guards (e.g. rejecting a second force cleanup while one
// is already queued/running).
func (s *HCJobStore) FindActiveByKind(kind HCJobKind) (*HCJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *HCJob
	for _, j := range s.jobs {
		if j.Kind != kind {
			continue
		}
		if j.Status != HCJobPending && j.Status != HCJobRunning {
			continue
		}
		if best == nil || j.StartedAt.After(best.StartedAt) {
			best = j
		}
	}
	return best, best != nil
}

// consume pulls job ids off the queue and runs them. A panic in the consumer
// itself is recovered (logged) so the runConsumerLoop restart path takes over.
func (s *HCJobStore) consume(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			if s.logger != nil {
				s.logger.Error("health check queue consumer panicked", "error", r)
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case jobID := <-s.queue:
			job, ok := s.Get(jobID)
			if !ok || job == nil || job.run == nil {
				continue
			}
			s.Update(job.ID, func(j *HCJob) {
				j.Status = HCJobRunning
			})
			s.runJob(job)
		}
	}
}

// runJob executes a single job, converting a panic into a failed job (with the
// stack) so one bad runner cannot take down the consumer.
func (s *HCJobStore) runJob(job *HCJob) {
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("panic: %v\n%s", r, debug.Stack())
			if s.logger != nil {
				s.logger.Error("health check job panicked",
					"job_id", job.ID,
					"kind", job.Kind,
					"error", r,
				)
			}
			s.finishFailed(job.ID, err)
		}
	}()
	job.run(s, job)
}

func (s *HCJobStore) finishDone(jobID string) {
	now := time.Now()
	s.Update(jobID, func(j *HCJob) {
		j.Status = HCJobDone
		j.FinishedAt = &now
	})
}

func (s *HCJobStore) finishFailed(jobID string, err error) {
	now := time.Now()
	s.Update(jobID, func(j *HCJob) {
		j.Status = HCJobFailed
		if err != nil {
			j.Error = err.Error()
		}
		j.FinishedAt = &now
	})
}

// RunPoolHealthCheckAsync enqueues a pool health check and returns the job
// immediately. Signature unchanged; the run is now driven by the shared queue
// consumer.
func RunPoolHealthCheckAsync(
	ctx context.Context,
	poolSvc *PoolService,
	poolID int,
	poolName, checkURL string,
	workers int,
) (*HCJob, error) {
	store := GetJobStore()
	if poolName == "" {
		poolName = fmt.Sprintf("Pool #%d", poolID)
	}
	if workers <= 0 {
		workers = 20
	}

	// Get proxy count upfront so frontend can show progress %.
	proxies, _ := poolSvc.poolRepo.GetProxies(ctx, poolID)

	job, err := store.Create(HCJobKindPool, poolID, poolName, checkURL, workers, nil, func(store *HCJobStore, job *HCJob) {
		result, err := poolSvc.HealthCheckPoolWithProgress(
			context.Background(), // keep running even if request context closes
			poolID, checkURL, workers,
			func(checked, active, failed int) {
				store.Update(job.ID, func(j *HCJob) {
					j.Progress = checked
					j.Active = active
					j.Failed = failed
				})
			},
		)
		if err != nil {
			store.finishFailed(job.ID, err)
			return
		}
		store.Update(job.ID, func(j *HCJob) {
			j.Total = result.Checked
			j.Active = result.Active
			j.Failed = result.Failed
			j.Progress = result.Checked
			j.Results = result.Results
		})
		store.finishDone(job.ID)
	})
	if err != nil {
		return nil, err
	}
	store.Update(job.ID, func(j *HCJob) {
		j.Total = len(proxies)
	})
	return job, nil
}

// RunOrphanHealthCheckAsync enqueues a health check for all orphan proxies
// (not attached to any pool). immediate=true applies each result to the proxy
// status right away (user-initiated); false keeps the consecutive-failure
// accounting (periodic scheduler).
func RunOrphanHealthCheckAsync(ctx context.Context, hc orphanChecker, immediate bool) (*HCJob, error) {
	store := GetJobStore()
	total := 0
	if counter, ok := hc.(orphanCounter); ok {
		if count, err := counter.CountOrphanProxies(ctx); err == nil {
			total = count
		}
	}

	job, err := store.Create(HCJobKindOrphan, 0, "Orphan proxies", "", 0, nil, func(store *HCJobStore, job *HCJob) {
		var (
			results []models.ProxyTestResult
			runErr  error
		)
		if checker, ok := hc.(orphanCheckerWithProgress); ok {
			results, runErr = checker.CheckAllProxiesWithProgress(context.Background(), func(checked, active, failed int) {
				store.Update(job.ID, func(j *HCJob) {
					j.Progress = checked
					j.Active = active
					j.Failed = failed
				})
			}, immediate)
		} else {
			results, runErr = hc.CheckAllProxies(context.Background())
		}
		if runErr != nil {
			store.finishFailed(job.ID, runErr)
			return
		}
		active := 0
		failed := 0
		for _, r := range results {
			if r.Status == "active" {
				active++
			} else {
				failed++
			}
		}
		store.Update(job.ID, func(j *HCJob) {
			j.Total = len(results)
			j.Progress = len(results)
			j.Active = active
			j.Failed = failed
			j.Results = results
		})
		store.finishDone(job.ID)
	})
	if err != nil {
		return nil, err
	}
	store.Update(job.ID, func(j *HCJob) {
		j.Total = total
	})
	return job, nil
}

// RunIdleOrphanHealthCheckAsync enqueues a health check for orphan proxies with
// status idle. immediate follows the same semantics as RunOrphanHealthCheckAsync.
func RunIdleOrphanHealthCheckAsync(ctx context.Context, hc idleOrphanCheckerWithProgress, immediate bool) (*HCJob, error) {
	store := GetJobStore()
	total := 0
	if counter, ok := hc.(idleOrphanCounter); ok {
		if count, err := counter.CountOrphanIdleProxies(ctx); err == nil {
			total = count
		}
	}

	job, err := store.Create(HCJobKindIdle, 0, "Idle orphan proxies", "", 0, nil, func(store *HCJobStore, job *HCJob) {
		results, runErr := hc.CheckOrphanIdleProxiesWithProgress(context.Background(), func(checked, active, failed int) {
			store.Update(job.ID, func(j *HCJob) {
				j.Progress = checked
				j.Active = active
				j.Failed = failed
			})
		}, immediate)
		if runErr != nil {
			store.finishFailed(job.ID, runErr)
			return
		}
		active := 0
		failed := 0
		for _, r := range results {
			if r.Status == "active" {
				active++
			} else {
				failed++
			}
		}
		store.Update(job.ID, func(j *HCJob) {
			j.Total = len(results)
			j.Progress = len(results)
			j.Active = active
			j.Failed = failed
			j.Results = results
		})
		store.finishDone(job.ID)
	})
	if err != nil {
		return nil, err
	}
	store.Update(job.ID, func(j *HCJob) {
		j.Total = total
	})
	return job, nil
}

// RunProxyHealthCheckAsync enqueues a health check for the given proxy IDs
// (user-initiated bulk "test selected") and returns the job immediately.
func RunProxyHealthCheckAsync(
	ctx context.Context,
	hc proxyIDsCheckerWithProgress,
	proxyIDs []int,
	workers int,
) (*HCJob, error) {
	store := GetJobStore()
	if workers <= 0 {
		workers = 20
	}

	job, err := store.Create(HCJobKindProxy, 0, "Selected proxies", "", workers, proxyIDs, func(store *HCJobStore, job *HCJob) {
		results, runErr := hc.CheckProxiesWithProgress(context.Background(), job.ProxyIDs, func(checked, active, failed int) {
			store.Update(job.ID, func(j *HCJob) {
				j.Progress = checked
				j.Active = active
				j.Failed = failed
			})
		}, true)
		if runErr != nil {
			store.finishFailed(job.ID, runErr)
			return
		}
		active := 0
		failed := 0
		for _, r := range results {
			if r.Status == "active" {
				active++
			} else {
				failed++
			}
		}
		store.Update(job.ID, func(j *HCJob) {
			j.Total = len(results)
			j.Progress = len(results)
			j.Active = active
			j.Failed = failed
			j.Results = results
		})
		store.finishDone(job.ID)
	})
	if err != nil {
		return nil, err
	}
	store.Update(job.ID, func(j *HCJob) {
		j.Total = len(proxyIDs)
	})
	return job, nil
}
