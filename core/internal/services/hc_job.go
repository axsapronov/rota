package services

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/alpkeskin/rota/core/internal/checkstats"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/gammazero/workerpool"
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

// HCJobKind describes health-check trigger type.
type HCJobKind string

const (
	HCJobKindPool   HCJobKind = "pool"
	HCJobKindProxy  HCJobKind = "proxy"
	HCJobKindOrphan HCJobKind = "orphan"
	HCJobKindIdle   HCJobKind = "idle"
)

type proxyChecker interface {
	CheckProxy(ctx context.Context, proxy *models.Proxy, immediate bool) (*models.ProxyTestResult, error)
}

type orphanChecker interface {
	CheckAllProxies(ctx context.Context) ([]models.ProxyTestResult, error)
}

type orphanCheckerWithProgress interface {
	CheckAllProxiesWithProgress(ctx context.Context, onProgress func(checked, active, failed int)) ([]models.ProxyTestResult, error)
}

type orphanCounter interface {
	CountOrphanProxies(ctx context.Context) (int, error)
}

type idleOrphanCounter interface {
	CountOrphanIdleProxies(ctx context.Context) (int, error)
}

type idleOrphanCheckerWithProgress interface {
	CheckOrphanIdleProxiesWithProgress(ctx context.Context, onProgress func(checked, active, failed int)) ([]models.ProxyTestResult, error)
}

// HCJob holds state for one async pool health-check run
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

	run func(*HCJobStore, *HCJob)
}

// HCJobStore keeps in-memory map of recent jobs (TTL 30 min)
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

// Start initializes queue worker once.
func (s *HCJobStore) Start(ctx context.Context, log *logger.Logger, proxyRepo *repository.ProxyRepository, poolSvc *PoolService, hc proxyChecker) {
	_ = proxyRepo
	_ = poolSvc
	_ = hc
	s.startOnce.Do(func() {
		s.ctx = ctx
		s.logger = log
		s.queue = make(chan string, 2048)
		go s.runConsumerLoop()
	})
}

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

// Create registers a new queued job and returns it.
func (s *HCJobStore) Create(kind HCJobKind, poolID int, poolName, checkURL string, workers int, proxyIDs []int, runFn func(*HCJobStore, *HCJob)) *HCJob {
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

	s.enqueue(job.ID)
	go s.cleanup()
	return job
}

func (s *HCJobStore) enqueue(jobID string) {
	if s.queue == nil {
		return
	}
	select {
	case s.queue <- jobID:
	default:
		go func() {
			select {
			case s.queue <- jobID:
			case <-s.ctx.Done():
			}
		}()
	}
}

// requeueInterruptedJobs puts running jobs back on the queue after the consumer goroutine exits.
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

// cleanup removes jobs older than 30 minutes
func (s *HCJobStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-30 * time.Minute)
	for id, j := range s.jobs {
		if j.StartedAt.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}

// QueuePending returns proxies not yet checked across pending and running jobs.
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

// HealthCheckMetricsSnapshot is the API shape for proxy health-check stats.
type HealthCheckMetricsSnapshot = checkstats.Snapshot

// HealthCheckMetrics returns proxy health-check queue and throughput stats.
func HealthCheckMetrics() HealthCheckMetricsSnapshot {
	return checkstats.BuildSnapshot(GetJobStore().QueuePending())
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
	for i := 0; i < len(out)-1; i++ {
		for k := i + 1; k < len(out); k++ {
			if out[k].StartedAt.After(out[i].StartedAt) {
				out[i], out[k] = out[k], out[i]
			}
		}
	}
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
	for i := 0; i < len(out)-1; i++ {
		for k := i + 1; k < len(out); k++ {
			if out[k].StartedAt.After(out[i].StartedAt) {
				out[i], out[k] = out[k], out[i]
			}
		}
	}
	return out
}

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

func (s *HCJobStore) finishDone(jobID string) {
	now := time.Now()
	s.Update(jobID, func(j *HCJob) {
		j.Status = HCJobDone
		j.FinishedAt = &now
	})
}

// RunPoolHealthCheckAsync enqueues pool health check and returns job immediately.
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

	job := store.Create(HCJobKindPool, poolID, poolName, checkURL, workers, nil, func(store *HCJobStore, job *HCJob) {
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
	store.Update(job.ID, func(j *HCJob) {
		j.Total = len(proxies)
	})
	return job, nil
}

func RunProxyHealthCheckAsync(ctx context.Context, proxyRepo *repository.ProxyRepository, hc proxyChecker, proxyIDs []int, workers int) (*HCJob, error) {
	store := GetJobStore()
	_ = ctx
	if workers <= 0 {
		workers = 20
	}
	job := store.Create(HCJobKindProxy, 0, "", "", workers, proxyIDs, func(store *HCJobStore, job *HCJob) {
		wp := workerpool.New(workers)
		var statsMu sync.Mutex
		for _, id := range proxyIDs {
			id := id
			wp.Submit(func() {
				p, err := proxyRepo.GetByID(context.Background(), id)
				if err != nil || p == nil {
					statsMu.Lock()
					job.Failed++
					job.Progress++
					statsMu.Unlock()
					store.Update(job.ID, func(j *HCJob) {
						j.Failed = job.Failed
						j.Progress = job.Progress
					})
					return
				}
				result, err := hc.CheckProxy(context.Background(), p, true)
				statsMu.Lock()
				if err != nil {
					job.Failed++
				} else if result.Status == "active" {
					job.Active++
					job.Results = append(job.Results, *result)
				} else {
					job.Failed++
					job.Results = append(job.Results, *result)
				}
				job.Progress++
				active, failed, progress := job.Active, job.Failed, job.Progress
				results := append([]models.ProxyTestResult(nil), job.Results...)
				statsMu.Unlock()
				store.Update(job.ID, func(j *HCJob) {
					j.Active = active
					j.Failed = failed
					j.Progress = progress
					j.Results = results
				})
			})
		}
		wp.StopWait()
		store.finishDone(job.ID)
	})
	store.Update(job.ID, func(j *HCJob) {
		j.Total = len(proxyIDs)
	})
	return job, nil
}

func RunOrphanHealthCheckAsync(ctx context.Context, hc orphanChecker) (*HCJob, error) {
	store := GetJobStore()
	total := 0
	if counter, ok := hc.(orphanCounter); ok {
		if count, err := counter.CountOrphanProxies(ctx); err == nil {
			total = count
		}
	}

	job := store.Create(HCJobKindOrphan, 0, "Orphan proxies", "", 0, nil, func(store *HCJobStore, job *HCJob) {
		var (
			results []models.ProxyTestResult
			err     error
		)
		if checker, ok := hc.(orphanCheckerWithProgress); ok {
			results, err = checker.CheckAllProxiesWithProgress(context.Background(), func(checked, active, failed int) {
				store.Update(job.ID, func(j *HCJob) {
					j.Progress = checked
					j.Active = active
					j.Failed = failed
				})
			})
		} else {
			results, err = hc.CheckAllProxies(context.Background())
		}
		if err != nil {
			store.finishFailed(job.ID, err)
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
	store.Update(job.ID, func(j *HCJob) {
		j.Total = total
	})
	return job, nil
}

// RunIdleOrphanHealthCheckAsync enqueues health check for orphan proxies with status idle.
func RunIdleOrphanHealthCheckAsync(ctx context.Context, hc idleOrphanCheckerWithProgress) (*HCJob, error) {
	store := GetJobStore()
	total := 0
	if counter, ok := hc.(idleOrphanCounter); ok {
		if count, err := counter.CountOrphanIdleProxies(ctx); err == nil {
			total = count
		}
	}

	job := store.Create(HCJobKindIdle, 0, "Idle orphan proxies", "", 0, nil, func(store *HCJobStore, job *HCJob) {
		results, err := hc.CheckOrphanIdleProxiesWithProgress(context.Background(), func(checked, active, failed int) {
			store.Update(job.ID, func(j *HCJob) {
				j.Progress = checked
				j.Active = active
				j.Failed = failed
			})
		})
		if err != nil {
			store.finishFailed(job.ID, err)
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
	store.Update(job.ID, func(j *HCJob) {
		j.Total = total
	})
	return job, nil
}
