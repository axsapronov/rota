package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// TestCheckDedup_Ownership verifies the acquire/release contract: the first
// owner wins, a second acquire is refused with the current owner, and a
// release frees the id for a new owner. A release with a stale job id is a
// no-op.
func TestCheckDedup_Ownership(t *testing.T) {
	d := &CheckDedup{}

	if ok, owner := d.TryAcquire(1, "job-a"); !ok || owner != "" {
		t.Fatalf("first acquire = (%v, %q), want (true, \"\")", ok, owner)
	}
	if ok, owner := d.TryAcquire(1, "job-b"); ok || owner != "job-a" {
		t.Fatalf("second acquire = (%v, %q), want (false, job-a)", ok, owner)
	}

	// A stale release must not free the slot.
	d.Release(1, "job-b")
	if ok, _ := d.TryAcquire(1, "job-c"); ok {
		t.Fatal("acquire succeeded after a stale release; the slot must stay owned")
	}

	d.Release(1, "job-a")
	if ok, _ := d.TryAcquire(1, "job-c"); !ok {
		t.Fatal("acquire after the owner's release must succeed")
	}
}

// setHealthCheckWorkers rewrites the shared healthcheck settings row so the
// bulk run fans every proxy out to its own worker (workers >= proxy count).
// The previous value is restored when the test ends.
func setHealthCheckWorkers(t *testing.T, db *testDB, workers int) {
	t.Helper()
	var cur []byte
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value FROM settings WHERE key = 'healthcheck'`).Scan(&cur); err != nil {
		t.Fatalf("read healthcheck setting: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(cur, &m); err != nil {
		t.Fatalf("parse healthcheck setting: %v", err)
	}
	m["workers"] = workers
	upd, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal healthcheck setting: %v", err)
	}
	const upsert = `INSERT INTO settings (key, value) VALUES ('healthcheck', $1::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`
	if _, err := db.Pool.Exec(context.Background(), upsert, upd); err != nil {
		t.Fatalf("update healthcheck workers: %v", err)
	}
	t.Cleanup(func() {
		db.Pool.Exec(context.Background(), upsert, cur) //nolint:errcheck
	})
}

// TestInFlightDedup_UnionOfParallelCheckers runs two checkers over the same
// id set: with the shared in-flight registry the total number of network
// checks must equal |union| (each proxy checked once), not the sum of both
// selections.
//
// Run A holds every id (its checks block) before run B starts, so B's overlap
// is total and deterministic: B skips all n ids, A checks all n.
func TestInFlightDedup_UnionOfParallelCheckers(t *testing.T) {
	db := openTestDB(t)
	seedHealthCheckSetting(t, db.Pool)

	// workers must be >= n so every proxy of the batch is checked (and thus
	// in flight) concurrently.
	setHealthCheckWorkers(t, db, 20)

	const n = 10
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, insertTestProxy(t, db.Pool, fmt.Sprintf("10.9.9.%d:8080", 100+i), "active"))
	}
	t.Cleanup(func() {
		db.Pool.Exec(context.Background(), `DELETE FROM proxies WHERE id = ANY($1)`, ids) //nolint:errcheck
	})

	var (
		networkChecks atomic.Int32
		aInFlight     atomic.Int32
		wgA, wgB      sync.WaitGroup
	)
	release := make(chan struct{})

	sharedDedup := &CheckDedup{}
	newChecker := func(jobID string, trackInFlight *atomic.Int32) *HealthChecker {
		h := &HealthChecker{
			proxyRepo:    db.Repo,
			settingsRepo: repository.NewSettingsRepository(&database.DB{Pool: db.Pool}),
			tracker:      db.Tracker,
			results:      NewResultWriter(db.Repo, logger.New("error")),
			dedup:        sharedDedup,
			logger:       logger.New("error"),
		}
		h.checkFn = func(ctx context.Context, p *models.Proxy, immediate bool) (*models.ProxyTestResult, error) {
			networkChecks.Add(1)
			if trackInFlight != nil {
				trackInFlight.Add(1)
				<-release // hold the id until the test releases
			}
			return &models.ProxyTestResult{
				ID: p.ID, Address: p.Address, Status: "active", TestedAt: time.Now(),
			}, nil
		}
		return h
	}

	var rsA, rsB []models.ProxyTestResult
	wgA.Add(1)
	go func() {
		defer wgA.Done()
		rsA, _ = newChecker("job-1", &aInFlight).CheckProxiesWithProgress(context.Background(), ids, nil, true, "job-1")
	}()

	// Wait until A holds every id in flight.
	deadline := time.Now().Add(5 * time.Second)
	for aInFlight.Load() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := aInFlight.Load(); got < n {
		t.Fatalf("run A in-flight checks = %d, want %d (A must hold every id before B starts)", got, n)
	}

	// B now runs over the same ids while A holds them: every id must be
	// skipped (no duplicate network check, no result).
	wgB.Add(1)
	go func() {
		defer wgB.Done()
		rsB, _ = newChecker("job-2", nil).CheckProxiesWithProgress(context.Background(), ids, nil, true, "job-2")
	}()
	wgB.Wait()

	close(release)
	wgA.Wait()

	if got := networkChecks.Load(); got != n {
		t.Fatalf("network checks = %d, want %d (|union| of both selections)", got, n)
	}
	if len(rsA) != n {
		t.Fatalf("run A results = %d, want %d", len(rsA), n)
	}
	if len(rsB) != 0 {
		t.Fatalf("run B results = %d, want 0 (all ids were in flight in A)", len(rsB))
	}
}
