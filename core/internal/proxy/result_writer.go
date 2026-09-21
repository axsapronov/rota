package proxy

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/jackc/pgx/v5"
)

const (
	// hcResultQueueSize bounds the in-flight check-result buffer; on overflow
	// records are dropped (status accounting is best-effort under load) rather
	// than blocking the check path.
	hcResultQueueSize = 8192
	// hcResultMaxBatch flushes early once this many records accumulate.
	hcResultMaxBatch = 500
	// hcResultFlushInterval is the periodic flush cadence.
	hcResultFlushInterval = 2 * time.Second
)

// CheckResultKind selects the persistence semantics of one check result.
type CheckResultKind string

const (
	// KindPeriodic applies the consecutive-failure hysteresis (3 trailing
	// failures flip the proxy to 'failed', a success resets and reactivates).
	KindPeriodic CheckResultKind = "periodic"
	// KindManual applies the result immediately (success -> 'active', failure
	// -> 'failed') for user-initiated single-proxy tests.
	KindManual CheckResultKind = "manual"
	// KindPool applies the result immediately, like manual, for pool sweeps.
	KindPool CheckResultKind = "pool"
)

// CheckResultRecord is one health-check outcome queued for batched persistence.
type CheckResultRecord struct {
	ProxyID   int
	Success   bool
	Duration  int // milliseconds (informational; not persisted)
	LastError string
	Kind      CheckResultKind
	Timestamp time.Time
}

// ResultWriter batches health-check result persistence: records are enqueued
// non-blockingly and a single worker flushes them as coalesced per-proxy
// UPDATEs (one per proxy per flush window, pipelined in a pgx.Batch), the same
// pattern as UsageTracker. This turns O(checks) round-trips into
// O(distinct proxies per flush window).
type ResultWriter struct {
	repo *repository.ProxyRepository

	// Channels are created up front (never reassigned) so concurrent Record
	// calls can never race with a lazy initialization; only the writer
	// goroutine is started lazily, under the Once.
	recordCh  chan CheckResultRecord
	flushCh   chan chan struct{}
	stopCh    chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	started   atomic.Bool
	closed    atomic.Bool
	dropped   atomic.Int64
	logger    *logger.Logger
}

// NewResultWriter creates a result writer. The batch goroutine starts lazily
// on the first Record, so construction is safe in tests and wiring needs no
// lifecycle hook.
func NewResultWriter(repo *repository.ProxyRepository, log *logger.Logger) *ResultWriter {
	return &ResultWriter{
		repo:     repo,
		logger:   log,
		recordCh: make(chan CheckResultRecord, hcResultQueueSize),
		flushCh:  make(chan chan struct{}, 1),
		stopCh:   make(chan struct{}),
	}
}

// start launches the batch writer exactly once.
func (w *ResultWriter) start() {
	w.startOnce.Do(func() {
		w.started.Store(true)
		w.wg.Add(1)
		go w.batchWriter()
	})
}

// Record enqueues a check result. Non-blocking: on overflow the record is
// dropped and counted (logged at Stop).
func (w *ResultWriter) Record(rec CheckResultRecord) {
	w.start()
	if w.closed.Load() {
		return
	}
	select {
	case w.recordCh <- rec:
	default:
		w.dropped.Add(1)
	}
}

// Drain flushes all buffered records and waits for the flush to complete, so a
// job's finish state reflects every result it produced. No-op when the writer
// has not started or is closing.
func (w *ResultWriter) Drain(ctx context.Context) {
	if !w.started.Load() || w.closed.Load() {
		return
	}
	done := make(chan struct{})
	select {
	case w.flushCh <- done:
	case <-ctx.Done():
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Stop drains buffered records, performs a final flush, and stops the writer.
func (w *ResultWriter) Stop() {
	if !w.started.Load() || !w.closed.CompareAndSwap(false, true) {
		return
	}
	close(w.stopCh)
	w.wg.Wait()
	if d := w.dropped.Load(); d > 0 && w.logger != nil {
		w.logger.Warn("result writer dropped records under load", "dropped", d)
	}
}

// checkResultAgg accumulates a flush window's records for one proxy. The
// fields are exactly the parameters of updateCheckResultBatchSQL, computed so
// the coalesced UPDATE reproduces the sequential RecordHealthCheck /
// RecordManualTestResult semantics (see that SQL for the derivation).
type checkResultAgg struct {
	lastIsSuccess     bool // the window's last record was a success
	hadSuccess        bool // any success in the window (resets the fail counter)
	trailingFails     int  // consecutive failures at the tail of the window
	maxRun            int  // longest consecutive-failure run in the window
	curRun            int
	lastImmediateFail bool // last record is a manual/pool failure
	hasImmediateFail  bool // any manual/pool failure in the window
	lastError         string
	lastTS            time.Time
}

// aggregateCheckResults folds a flush window into per-proxy aggregates,
// preserving the arrival order of first appearance. Pure/deterministic so it
// can be unit-tested without a DB.
func aggregateCheckResults(batch []CheckResultRecord) (order []int, aggs map[int]*checkResultAgg) {
	aggs = make(map[int]*checkResultAgg, len(batch))
	order = make([]int, 0, len(batch))
	for i := range batch {
		r := &batch[i]
		a, ok := aggs[r.ProxyID]
		if !ok {
			a = &checkResultAgg{}
			aggs[r.ProxyID] = a
			order = append(order, r.ProxyID)
		}
		if r.Success {
			a.hadSuccess = true
			a.lastIsSuccess = true
			a.trailingFails = 0
			a.curRun = 0
		} else {
			a.lastIsSuccess = false
			a.trailingFails++
			a.curRun++
			if a.curRun > a.maxRun {
				a.maxRun = a.curRun
			}
			if r.Kind != KindPeriodic {
				a.hasImmediateFail = true
			}
		}
		a.lastImmediateFail = !r.Success && r.Kind != KindPeriodic
		if r.Success {
			a.lastError = ""
		} else {
			a.lastError = r.LastError
		}
		a.lastTS = r.Timestamp
	}
	return order, aggs
}

// updateCheckResultBatchSQL applies one proxy's flush window in a single
// relative UPDATE (composable with the concurrent usage-stat updates).
//
// Parameters: $1 lastIsSuccess, $2 hadSuccess, $3 trailingFails,
// $4 lastImmediateFail, $5 hasImmediateFail, $6 maxRun, $7 proxyID,
// $8 lastError (nil on success / empty message), $9 lastTS.
//
// Semantics (sequential-equivalent):
//   - success: failed_requests = 0, status = 'active', last_error = NULL.
//   - manual/pool failure: failed_requests + 1, status = 'failed' (immediate).
//   - periodic failure: failed_requests + 1, status flips to 'failed' only when
//     the counter reaches 3 (hysteresis); a success anywhere in the window
//     resets the counter, so only the trailing failures accumulate.
const updateCheckResultBatchSQL = `
	UPDATE proxies SET
		last_check = $9,
		last_error = CASE WHEN $1 THEN NULL ELSE $8 END,
		failed_requests = CASE
			WHEN $1 THEN 0
			WHEN $2 THEN $3
			ELSE failed_requests + $3
		END,
		status = CASE
			WHEN $1 THEN 'active'
			WHEN $4 THEN 'failed'
			WHEN $2 THEN (CASE WHEN $3 >= 3 THEN 'failed' ELSE 'active' END)
			ELSE (CASE
				WHEN $5 OR (failed_requests + $6) >= 3 THEN 'failed'
				ELSE (CASE WHEN failed_requests + $3 >= 3 THEN 'failed' ELSE status END)
			END)
		END,
		updated_at = NOW()
	WHERE id = $7
`

// batchWriter consumes records, flushing on size, the flush interval, an
// explicit Drain, or stop.
func (w *ResultWriter) batchWriter() {
	defer w.wg.Done()

	ticker := time.NewTicker(hcResultFlushInterval)
	defer ticker.Stop()

	batch := make([]CheckResultRecord, 0, hcResultMaxBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		w.flush(ctx, batch)
		cancel()
		batch = batch[:0]
	}

	for {
		select {
		case rec := <-w.recordCh:
			batch = append(batch, rec)
			if len(batch) >= hcResultMaxBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case done := <-w.flushCh:
			flush()
			close(done)
		case <-w.stopCh:
			// Drain anything still buffered, then final flush.
			for {
				select {
				case rec := <-w.recordCh:
					batch = append(batch, rec)
					if len(batch) >= hcResultMaxBatch {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// flush applies the coalesced per-proxy updates pipelined in one batch
// (one round-trip). A failed flush is logged only: persistence is best-effort
// and must never fail the health checks that produced the records.
func (w *ResultWriter) flush(ctx context.Context, batch []CheckResultRecord) {
	order, aggs := aggregateCheckResults(batch)
	// Acquire row locks in ascending proxy-id order. UsageTracker flushes the
	// same rows; a shared global lock order makes lock-order inversion (and
	// the resulting Postgres deadlocks / 15s flush timeouts) impossible.
	sort.Ints(order)

	b := &pgx.Batch{}
	for _, id := range order {
		a := aggs[id]
		var lastErr *string
		if !a.lastIsSuccess && a.lastError != "" {
			lastErr = &a.lastError
		}
		// last_check is a TIMESTAMP WITHOUT TIME ZONE; store UTC wall-clock so
		// Go-written values line up with the NOW()-based TTL filter (which is
		// evaluated in the session timezone, UTC).
		b.Queue(updateCheckResultBatchSQL,
			a.lastIsSuccess, a.hadSuccess, a.trailingFails,
			a.lastImmediateFail, a.hasImmediateFail, a.maxRun,
			id, lastErr, a.lastTS.UTC())
	}
	br := w.repo.GetDB().Pool.SendBatch(ctx, b)
	for range order {
		if _, err := br.Exec(); err != nil {
			w.logErr("failed to persist health check results", err)
		}
	}
	br.Close()
}

func (w *ResultWriter) logErr(msg string, err error) {
	if w.logger != nil {
		w.logger.Error(msg, "error", err)
	} else {
		fmt.Fprintf(os.Stderr, "result writer: %s: %v\n", msg, err)
	}
}
