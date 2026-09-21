package services

import (
	"container/heap"
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

// maxJobResults bounds the per-proxy results kept in memory per job: a sweep
// of 300k proxies must not pin 300k result structs. Only the most recent
// failures are kept — they are what a user inspecting the job wants to see.
const maxJobResults = 100

// trimJobResults caps results at the most recent maxJobResults failures.
// Aggregate counters (Total/Active/Failed) are computed separately and are
// unaffected; the JSON shape of the results field is unchanged.
func trimJobResults(results []models.ProxyTestResult) []models.ProxyTestResult {
	if len(results) <= maxJobResults {
		return results
	}
	failures := make([]models.ProxyTestResult, 0, maxJobResults)
	for i := len(results) - 1; i >= 0 && len(failures) < maxJobResults; i-- {
		if results[i].Status != "active" {
			failures = append(failures, results[i])
		}
	}
	out := make([]models.ProxyTestResult, 0, len(failures))
	for i := len(failures) - 1; i >= 0; i-- {
		out = append(out, failures[i])
	}
	return out
}

// hcPriority orders health-check jobs in the queue: smaller runs first.
type hcPriority int

const (
	hcPriorityHigh   hcPriority = iota // pool checks, selected-proxy tests
	hcPriorityMedium                   // periodic orphan sweeps
	hcPriorityLow                      // idle sweeps, force cleanup
)

// hcAgingAfter is how long a LOW job may wait before it is promoted ahead of
// newly enqueued MEDIUM/LOW jobs, so a long-queued low-priority sweep is not
// starved by a steady stream of new medium-priority work.
const hcAgingAfter = 15 * time.Minute

// hcPriorityForKind maps a job kind to its queue priority.
func hcPriorityForKind(kind HCJobKind) hcPriority {
	switch kind {
	case HCJobKindPool, HCJobKindProxy:
		return hcPriorityHigh
	case HCJobKindOrphan:
		return hcPriorityMedium
	default: // HCJobKindIdle, HCJobKindForceCleanup
		return hcPriorityLow
	}
}

// hcConsumerClass identifies which priority band a consumer drains.
type hcConsumerClass int

const (
	consumerHigh   hcConsumerClass = iota // HIGH only
	consumerMedLow                        // MEDIUM and LOW
)

func (c hcConsumerClass) String() string {
	if c == consumerHigh {
		return "high"
	}
	return "medium-low"
}

// matches reports whether a (possibly aged) priority belongs to the class.
func (c hcConsumerClass) matches(p hcPriority) bool {
	if c == consumerHigh {
		return p == hcPriorityHigh
	}
	return p == hcPriorityMedium || p == hcPriorityLow
}

// hcQueueItem is one enqueued job.
type hcQueueItem struct {
	id         string
	priority   hcPriority
	enqueuedAt time.Time
	index      int // container/heap bookkeeping
}

// effectivePriority applies aging: a LOW item older than hcAgingAfter is
// treated as MEDIUM. Effective keys only ever increase (LOW -> MEDIUM), which
// preserves the min-heap invariant, so aging can be evaluated lazily in Less.
func (i hcQueueItem) effectivePriority(now time.Time) hcPriority {
	if i.priority == hcPriorityLow && now.Sub(i.enqueuedAt) > hcAgingAfter {
		return hcPriorityMedium
	}
	return i.priority
}

// hcPriorityQueue is a bounded min-heap of enqueued job ids ordered by
// (effective priority, enqueuedAt). Two consumers wait on the same cond
// variable and each takes only items of its class, so a HIGH job is never
// held behind a long MEDIUM/LOW sweep (head-of-line blocking).
type hcPriorityQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	items    []hcQueueItem
	capacity int
	now      func() time.Time
}

func newHCPriorityQueue(capacity int, now func() time.Time) *hcPriorityQueue {
	if now == nil {
		now = time.Now
	}
	q := &hcPriorityQueue{capacity: capacity, now: now}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *hcPriorityQueue) Len() int { return len(q.items) }

func (q *hcPriorityQueue) Swap(i, j int) {
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.items[i].index = i
	q.items[j].index = j
}

func (q *hcPriorityQueue) Less(i, j int) bool {
	a, b := q.items[i], q.items[j]
	pa, pb := a.effectivePriority(q.now()), b.effectivePriority(q.now())
	if pa != pb {
		return pa < pb
	}
	return a.enqueuedAt.Before(b.enqueuedAt)
}

func (q *hcPriorityQueue) Push(x any) {
	item := x.(hcQueueItem)
	item.index = len(q.items)
	q.items = append(q.items, item)
}

func (q *hcPriorityQueue) Pop() any {
	n := len(q.items)
	item := q.items[n-1]
	q.items[n-1] = hcQueueItem{}
	q.items = q.items[:n-1]
	return item
}

// enqueue adds an item without blocking. It reports false when the queue is at
// capacity (the caller maps that to ErrQueueFull / HTTP 429).
func (q *hcPriorityQueue) enqueue(id string, priority hcPriority, at time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.capacity {
		return false
	}
	heap.Push(q, hcQueueItem{id: id, priority: priority, enqueuedAt: at})
	q.cond.Broadcast()
	return true
}

// popIf blocks until an item of the consumer's class is at the head of the
// heap, then removes and returns it. It reports false when ctx is done.
func (q *hcPriorityQueue) popIf(ctx context.Context, class hcConsumerClass) (hcQueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		// Checked first, on every wake-up: once the context is done the
		// consumer takes no more items, even if one is enqueued.
		if ctx.Err() != nil {
			return hcQueueItem{}, false
		}
		if len(q.items) > 0 && class.matches(q.items[0].effectivePriority(q.now())) {
			return heap.Pop(q).(hcQueueItem), true
		}
		q.cond.Wait()
	}
}

// Injection interfaces. HealthChecker is passed as one of these and the
// concrete capabilities are detected via type assertion, so the queue layer
// stays decoupled from the proxy package.
type orphanChecker interface {
	CheckAllProxies(ctx context.Context) ([]models.ProxyTestResult, error)
}

// orphanCheckerWithProgress matches HealthChecker.CheckAllProxiesWithProgress.
// The immediate parameter is required: periodic scheduler runs use
// immediate=false (consecutive-failure hysteresis), user-initiated runs use
// immediate=true. jobID feeds the in-flight dedup registry.
type orphanCheckerWithProgress interface {
	CheckAllProxiesWithProgress(ctx context.Context, onProgress func(checked, active, failed int), immediate bool, jobID string) ([]models.ProxyTestResult, error)
}

type orphanCounter interface {
	CountOrphanProxies(ctx context.Context) (int, error)
}

type idleOrphanCounter interface {
	CountOrphanIdleProxies(ctx context.Context) (int, error)
}

type idleOrphanCheckerWithProgress interface {
	CheckOrphanIdleProxiesWithProgress(ctx context.Context, onProgress func(checked, active, failed int), immediate bool, jobID string) ([]models.ProxyTestResult, error)
}

// proxyIDsCheckerWithProgress matches HealthChecker.CheckProxiesWithProgress
// (user-initiated bulk health check of selected proxies).
type proxyIDsCheckerWithProgress interface {
	CheckProxiesWithProgress(ctx context.Context, proxyIDs []int, onProgress func(checked, active, failed int), immediate bool, jobID string) ([]models.ProxyTestResult, error)
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

	// run is the job's runner, invoked by the queue consumers.
	run func(*HCJobStore, *HCJob)
}

// HCJobStore keeps an in-memory map of recent jobs (TTL 30 min) plus a bounded
// priority queue drained by two consumers (HIGH-only and MEDIUM/LOW). The
// queue provides backpressure: when it is at capacity, new jobs are rejected
// (ErrQueueFull) rather than blocking.
type HCJobStore struct {
	mu        sync.RWMutex
	jobs      map[string]*HCJob
	queue     *hcPriorityQueue
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

// hcQueueCapacity is the total queue capacity shared by both consumers.
const hcQueueCapacity = 2048

// newHCJobStore builds a store with a bounded queue for tests. The production
// singleton uses the default queue capacity set in Start.
func newHCJobStore(queueCap int) *HCJobStore {
	return &HCJobStore{
		jobs:  make(map[string]*HCJob),
		queue: newHCPriorityQueue(queueCap, time.Now),
	}
}

// Start initializes the queue consumers once. Both consumers live for the
// given context's lifetime (wired to the service context in server.go so they
// stop on shutdown). If a queue was pre-created (test store) it is reused.
func (s *HCJobStore) Start(ctx context.Context, log *logger.Logger) {
	s.startOnce.Do(func() {
		s.ctx = ctx
		s.logger = log
		if s.queue == nil {
			s.queue = newHCPriorityQueue(hcQueueCapacity, time.Now)
		}
		go s.runConsumerLoop(consumerHigh)
		go s.runConsumerLoop(consumerMedLow)
	})
}

// runConsumerLoop runs one consumer, restarting it if it exits for any reason
// other than the context being done. On an unexpected exit, in-flight (running)
// jobs are requeued so they are not lost.
func (s *HCJobStore) runConsumerLoop(class hcConsumerClass) {
	first := true
	for {
		if s.ctx.Err() != nil {
			return
		}
		if !first {
			s.requeueInterruptedJobs()
		}
		first = false
		s.consume(s.ctx, class)
		if s.ctx.Err() != nil {
			return
		}
		if s.logger != nil {
			s.logger.Warn("health check queue consumer exited unexpectedly, restarting",
				"class", class.String(), "delay", "1s")
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

	if !s.enqueue(job.ID, hcPriorityForKind(kind)) {
		s.mu.Lock()
		delete(s.jobs, job.ID)
		s.mu.Unlock()
		return nil, ErrQueueFull
	}

	// Cleanup old jobs in background.
	go s.cleanup()
	return job, nil
}

// enqueue pushes a job id onto the priority queue without blocking. It
// reports whether the enqueue succeeded (false when the queue is nil or full).
func (s *HCJobStore) enqueue(jobID string, prio hcPriority) bool {
	if s.queue == nil {
		return false
	}
	return s.queue.enqueue(jobID, prio, time.Now())
}

// requeueInterruptedJobs puts running jobs back on the queue after a consumer
// goroutine exits, so an interrupted run is retried rather than dropped. The
// status flip to pending happens under the same lock as the collection, so a
// job is requeued at most once even if both consumers restart concurrently.
func (s *HCJobStore) requeueInterruptedJobs() {
	if s.queue == nil {
		return
	}
	s.mu.Lock()
	type requeuedJob struct {
		id   string
		kind HCJobKind
	}
	ids := make([]requeuedJob, 0)
	for id, j := range s.jobs {
		if j.Status == HCJobRunning {
			j.Status = HCJobPending
			j.UpdatedAt = time.Now()
			ids = append(ids, requeuedJob{id: id, kind: j.Kind})
		}
	}
	s.mu.Unlock()

	for _, rj := range ids {
		s.enqueue(rj.id, hcPriorityForKind(rj.kind))
	}
}

// Get returns a job by ID
func (s *HCJobStore) Get(id string) (*HCJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

// Snapshot returns a consistent copy of a job under the store lock. The live
// record is mutated by the queue consumers, so API handlers must read fields
// from this copy, not from the shared pointer Get returns.
func (s *HCJobStore) Snapshot(id string) (HCJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return HCJob{}, false
	}
	cp := *j
	return cp, true
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

// ListByPoolCopies returns copies of all jobs for a pool (newest first),
// taken under the store lock: safe to marshal, since the live records keep
// being mutated by the queue consumers.
func (s *HCJobStore) ListByPoolCopies(poolID int) []HCJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []HCJob
	for _, j := range s.jobs {
		if j.PoolID == poolID {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].StartedAt.After(out[k].StartedAt)
	})
	return out
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

// FindActiveCovering returns the most recent pending/running proxy job whose
// ProxyIDs cover every requested id, for the bulk "test selected" guard: a
// repeat request for a subset of an in-flight selection returns the existing
// job instead of queueing a duplicate sweep.
func (s *HCJobStore) FindActiveCovering(proxyIDs []int) (*HCJob, bool) {
	if len(proxyIDs) == 0 {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *HCJob
	for _, j := range s.jobs {
		if j.Kind != HCJobKindProxy {
			continue
		}
		if j.Status != HCJobPending && j.Status != HCJobRunning {
			continue
		}
		if best != nil && !j.StartedAt.After(best.StartedAt) {
			continue
		}
		if idsCover(j.ProxyIDs, proxyIDs) {
			best = j
		}
	}
	return best, best != nil
}

// idsCover reports whether have contains every id in want.
func idsCover(have, want []int) bool {
	set := make(map[int]struct{}, len(have))
	for _, id := range have {
		set[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
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

// FindActivePoolJob returns the most recent pending/running pool job for
// poolID, for the pool sweep guard: a repeat trigger for the same pool
// returns the existing job instead of queueing a duplicate sweep.
func (s *HCJobStore) FindActivePoolJob(poolID int) (*HCJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *HCJob
	for _, j := range s.jobs {
		if j.Kind != HCJobKindPool || j.PoolID != poolID {
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

// consume pulls job ids off the priority queue and runs them, taking only
// items of this consumer's class. A panic in the consumer itself is recovered
// (logged) so the runConsumerLoop restart path takes over.
func (s *HCJobStore) consume(ctx context.Context, class hcConsumerClass) {
	defer func() {
		if r := recover(); r != nil {
			if s.logger != nil {
				s.logger.Error("health check queue consumer panicked", "class", class.String(), "error", r)
			}
		}
	}()
	for {
		item, ok := s.queue.popIf(ctx, class)
		if !ok {
			return
		}
		job, found := s.Get(item.id)
		if !found || job == nil || job.run == nil {
			continue
		}
		s.Update(job.ID, func(j *HCJob) {
			j.Status = HCJobRunning
		})
		s.runJob(job)
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
	// Pool sweep guard: a repeat trigger for the same pool returns the
	// existing pending/running job instead of queueing a duplicate sweep.
	if existing, ok := store.FindActivePoolJob(poolID); ok {
		return existing, nil
	}
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
			job.ID,
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
			j.Results = trimJobResults(result.Results)
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
	// Orphan sweep guard: at most one orphan sweep in flight — a repeat
	// trigger (manual API call or scheduler tick) returns the existing
	// pending/running job instead of queueing a duplicate.
	if existing, ok := store.FindActiveByKind(HCJobKindOrphan); ok {
		return existing, nil
	}
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
			}, immediate, job.ID)
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
			j.Results = trimJobResults(results)
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
	// Idle sweep guard: same single-in-flight rule as the orphan sweep.
	if existing, ok := store.FindActiveByKind(HCJobKindIdle); ok {
		return existing, nil
	}
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
		}, immediate, job.ID)
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
			j.Results = trimJobResults(results)
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

	// Bulk guard: if an in-flight proxy sweep already covers every requested
	// id, return it instead of queueing a duplicate.
	if existing, ok := store.FindActiveCovering(proxyIDs); ok {
		return existing, nil
	}

	job, err := store.Create(HCJobKindProxy, 0, "Selected proxies", "", workers, proxyIDs, func(store *HCJobStore, job *HCJob) {
		results, runErr := hc.CheckProxiesWithProgress(context.Background(), job.ProxyIDs, func(checked, active, failed int) {
			store.Update(job.ID, func(j *HCJob) {
				j.Progress = checked
				j.Active = active
				j.Failed = failed
			})
		}, true, job.ID)
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
			j.Results = trimJobResults(results)
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
