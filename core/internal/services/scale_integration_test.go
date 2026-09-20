package services

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/proxy"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

// TestScaleProxyHealthCheck is the 50k-proxy integration check (task 6.4):
// two pools with a 5k overlap plus a 15k orphan tail. It verifies the pool
// job starts while the orphan sweep is running (priority), the single HIGH
// consumer serializes the two pool jobs, every proxy is covered, and core
// memory stays flat at this scale.
//
// Gated behind ROTA_SCALE_TEST=1 (plus ROTA_TEST_DSN): the fixture inserts
// 50k rows and runs three full sweeps, taking well over a minute.
func TestScaleProxyHealthCheck(t *testing.T) {
	if os.Getenv("ROTA_SCALE_TEST") != "1" {
		t.Skip("set ROTA_SCALE_TEST=1 to run the 50k scale check")
	}
	dsn := os.Getenv("ROTA_TEST_DSN")
	if dsn == "" {
		t.Skip("ROTA_TEST_DSN not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to test DB: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test DB: %v", err)
	}
	t.Cleanup(pool.Close)

	// The shared test DB has no proxy_pools; create the minimal shape the
	// pool repository scans (poolColumns) if missing.
	const createPools = `
		CREATE TABLE IF NOT EXISTS proxy_pools (
			id               INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			name             VARCHAR(255) NOT NULL,
			description      TEXT NOT NULL DEFAULT '',
			country_code     VARCHAR(3),
			region_name      VARCHAR(100),
			city_name        VARCHAR(100),
			rotation_method  VARCHAR(30) NOT NULL DEFAULT 'roundrobin',
			stick_count      INTEGER NOT NULL DEFAULT 10,
			health_check_url TEXT NOT NULL DEFAULT 'https://api.ipify.org',
			health_check_cron VARCHAR(100) NOT NULL DEFAULT '*/30 * * * *',
			health_check_enabled BOOLEAN NOT NULL DEFAULT true,
			auto_sync        BOOLEAN NOT NULL DEFAULT true,
			sync_mode        VARCHAR(20) NOT NULL DEFAULT 'auto',
			enabled          BOOLEAN NOT NULL DEFAULT true,
			created_at       TIMESTAMP NOT NULL DEFAULT NOW(),
			updated_at       TIMESTAMP NOT NULL DEFAULT NOW()
		);
	`
	if _, err := pool.Exec(ctx, createPools); err != nil {
		t.Fatalf("create proxy_pools table: %v", err)
	}
	// The orphan sweep's NOT EXISTS subquery needs this index (present in
	// production, absent from the minimal shared test schema).
	if _, err := pool.Exec(ctx,
		`CREATE INDEX IF NOT EXISTS idx_pool_proxies_proxy_id ON pool_proxies(proxy_id)`); err != nil {
		t.Fatalf("create pool_proxies index: %v", err)
	}

	const (
		totalProxies = 50000
		poolASize    = 20000
		poolBStart   = 15000 // 5k overlap with pool A
		poolBSize    = 20000
		orphanStart  = 35000 // 15k orphan tail
		// fastProxyAddr is the local fast-proxy the whole fixture points at.
		fastProxyAddr = "127.0.0.1:41999"
	)

	// Fresh fixture: drop any previous scale rows.
	if _, err := pool.Exec(ctx, `DELETE FROM pool_proxies WHERE pool_id IN
		(SELECT id FROM proxy_pools WHERE name LIKE 'scale-%')`); err != nil {
		t.Fatalf("clean pool_proxies: %v", err)
	}
	// Take ownership of the orphan set: the global sweep (CheckAllProxies)
	// picks up every orphan proxy, so clear any pre-existing orphans (e.g.
	// garbage left behind by an interrupted earlier run) to keep the sweep
	// scoped to this fixture. The test is gated, so this is safe.
	if _, err := pool.Exec(ctx, `DELETE FROM proxies p WHERE NOT EXISTS
		(SELECT 1 FROM pool_proxies ppm WHERE ppm.proxy_id = p.id)`); err != nil {
		t.Fatalf("clean orphan proxies: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM proxy_pools WHERE name LIKE 'scale-%'`); err != nil {
		t.Fatalf("clean pools: %v", err)
	}

	// Fast proxy: a local TCP listener that acts as an HTTP proxy and answers
	// every request with an immediate 200. All 50k proxies point at it, so
	// each health check is a fast, deterministic, real HTTP exchange (no dial
	// timeouts). The test verifies orchestration at scale (priority, dedup,
	// memory), not network throughput.
	ln, err := net.Listen("tcp", fastProxyAddr)
	if err != nil {
		t.Fatalf("listen fast proxy %s: %v", fastProxyAddr, err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
				buf := make([]byte, 1024)
				c.Read(buf)                                                                            //nolint:errcheck
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")) //nolint:errcheck
			}(c)
		}
	}()
	checkURL := "http://" + fastProxyAddr + "/"

	// 50k proxies: pool A (0..19999), pool B (15000..34999), orphan tail
	// (35000..49999, idle). Every row uses the fast-proxy address. The test
	// is gated, so the shared address is safe to clean by equality.
	rows := make([]scaleRow, 0, totalProxies)
	for i := 0; i < totalProxies; i++ {
		status := "active"
		if i >= orphanStart {
			status = "idle"
		}
		rows = append(rows, scaleRow{addr: fastProxyAddr, proto: "http", status: status})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"proxies"}, []string{"address", "protocol", "status"},
		&scaleRowSource{rows: rows}); err != nil {
		t.Fatalf("insert %d proxies: %v", totalProxies, err)
	}

	var ids []int
	idRows, err := pool.Query(ctx, fmt.Sprintf(`SELECT id FROM proxies WHERE address = '%s' ORDER BY id`, fastProxyAddr))
	if err != nil {
		t.Fatalf("query proxy ids: %v", err)
	}
	for idRows.Next() {
		var id int
		if err := idRows.Scan(&id); err != nil {
			idRows.Close()
			t.Fatalf("scan proxy ids: %v", err)
		}
		ids = append(ids, id)
	}
	idRows.Close()
	if err := idRows.Err(); err != nil {
		t.Fatalf("proxy ids rows: %v", err)
	}
	if len(ids) != totalProxies {
		t.Fatalf("proxy ids = %d, want %d", len(ids), totalProxies)
	}

	insertPool := func(name string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx,
			`INSERT INTO proxy_pools (name, description, health_check_url)
			 VALUES ($1, '', $2) RETURNING id`,
			name, checkURL).Scan(&id); err != nil {
			t.Fatalf("insert pool %s: %v", name, err)
		}
		return id
	}
	poolA := insertPool("scale-a")
	poolB := insertPool("scale-b")

	members := make([]scaleMember, 0, poolASize+poolBSize)
	for i := 0; i < poolASize; i++ {
		members = append(members, scaleMember{poolA, ids[i]})
	}
	for i := poolBStart; i < poolBStart+poolBSize; i++ {
		members = append(members, scaleMember{poolB, ids[i]})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"pool_proxies"}, []string{"pool_id", "proxy_id"},
		&scaleRowSource2{rows: members}); err != nil {
		t.Fatalf("insert pool memberships: %v", err)
	}

	// TTL filters off (0) so every orphan is swept; 20 workers per job.
	hcJSON := fmt.Sprintf(`{"timeout":5,"workers":20,"url":%q,"status":200,"orphan_ttl_minutes":0,"idle_ttl_minutes":0}`, checkURL)
	if _, err := pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ('healthcheck', $1::jsonb)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, hcJSON); err != nil {
		t.Fatalf("seed healthcheck setting: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(),
			`DELETE FROM settings WHERE key = 'healthcheck'`) //nolint:errcheck
	})

	// Swap in a fresh job store with a bounded priority queue.
	prevStore := globalJobStore
	store := newHCJobStore(hcQueueCapacity)
	storeCtx, storeCancel := context.WithCancel(context.Background())
	store.Start(storeCtx, logger.New("error"))
	globalJobStore = store
	t.Cleanup(func() {
		globalJobStore = prevStore
		storeCancel()
	})

	db := &database.DB{Pool: pool}
	log := logger.New("error")
	hc := proxy.NewHealthChecker(
		repository.NewProxyRepository(db),
		repository.NewSettingsRepository(db),
		proxy.NewUsageTracker(repository.NewProxyRepository(db)),
		log,
	)
	poolSvc := NewPoolService(repository.NewPoolRepository(db), repository.NewProxyRepository(db), log)

	rss0 := vmRSSKB(t)
	// last_check is TIMESTAMP WITHOUT TIME ZONE storing UTC wall-clock, so the
	// comparison boundary must be UTC too (a local time.Now() would be encoded
	// as local wall-clock and skew the window by the UTC offset).
	t0 := time.Now().UTC()

	orphanJob, err := RunOrphanHealthCheckAsync(ctx, hc, false)
	if err != nil {
		t.Fatalf("enqueue orphan job: %v", err)
	}
	waitJobStatus(t, store, orphanJob.ID, HCJobRunning, 15*time.Second)

	poolAJob, err := RunPoolHealthCheckAsync(ctx, poolSvc, poolA, "scale-a", checkURL, 20)
	if err != nil {
		t.Fatalf("enqueue pool A job: %v", err)
	}
	poolBJob, err := RunPoolHealthCheckAsync(ctx, poolSvc, poolB, "scale-b", checkURL, 20)
	if err != nil {
		t.Fatalf("enqueue pool B job: %v", err)
	}

	// (1) Priority: the pool job runs while the orphan sweep is running.
	waitJobStatus(t, store, poolAJob.ID, HCJobRunning, 15*time.Second)
	if oj, _ := store.Snapshot(orphanJob.ID); oj.Status != HCJobRunning {
		t.Fatalf("pool A job started while orphan job was %q, want running", oj.Status)
	}
	// (2) One HIGH consumer: pool B waits in the queue while A runs.
	if bj, _ := store.Snapshot(poolBJob.ID); bj.Status != HCJobPending {
		t.Fatalf("pool B job status = %q while A runs, want pending", bj.Status)
	}

	// Observe when B actually starts running (StartedAt is the enqueue time).
	bRunStart := make(chan time.Time, 1)
	go func() {
		for {
			if bj, _ := store.Snapshot(poolBJob.ID); bj.Status == HCJobRunning {
				bRunStart <- time.Now().UTC()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	// (3) All three jobs complete.
	for _, j := range []struct {
		id   string
		name string
	}{{orphanJob.ID, "orphan"}, {poolAJob.ID, "pool A"}, {poolBJob.ID, "pool B"}} {
		waitJobStatus(t, store, j.id, HCJobDone, 3*time.Minute)
	}
	elapsed := time.Since(t0)

	orphanSnap, _ := store.Snapshot(orphanJob.ID)
	poolASnap, _ := store.Snapshot(poolAJob.ID)
	poolBSnap, _ := store.Snapshot(poolBJob.ID)
	if orphanSnap.Progress != totalProxies-orphanStart {
		t.Fatalf("orphan job checked %d, want %d", orphanSnap.Progress, totalProxies-orphanStart)
	}
	if poolASnap.Progress != poolASize {
		t.Fatalf("pool A job checked %d, want %d", poolASnap.Progress, poolASize)
	}
	if poolBSnap.Progress != poolBSize {
		t.Fatalf("pool B job checked %d, want %d", poolBSnap.Progress, poolBSize)
	}

	// (4) Full coverage: every fixture proxy was checked.
	var checked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM proxies
		WHERE address = '`+fastProxyAddr+`' AND last_check >= $1`, t0).Scan(&checked); err != nil {
		t.Fatalf("count checked: %v", err)
	}
	if checked != totalProxies {
		t.Fatalf("proxies with last_check in window = %d, want %d", checked, totalProxies)
	}

	// (5) The 5k overlap was checked by both pool jobs and its final
	// last_check comes from the later (B) job — never from the orphan sweep.
	// The boundary is nudged back past the 20ms observation granularity.
	bStart := (<-bRunStart).Add(-100 * time.Millisecond)
	var stale int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM proxies p
		JOIN pool_proxies pa ON pa.proxy_id = p.id AND pa.pool_id = $1
		JOIN pool_proxies pb ON pb.proxy_id = p.id AND pb.pool_id = $2
		WHERE p.last_check < $3`, poolA, poolB, bStart).Scan(&stale); err != nil {
		t.Fatalf("count stale overlap: %v", err)
	}
	if stale != 0 {
		t.Fatalf("%d overlap proxies have a last_check before pool B started", stale)
	}

	// (6) Memory stays flat at 50k scale (results are trimmed to 100 per
	// job; pagination batches are 1000).
	rss1 := vmRSSKB(t)
	delta := rss1 - rss0
	const maxDeltaKB = 150 * 1024
	if delta > maxDeltaKB {
		t.Fatalf("VmRSS grew by %d KB, want < %d KB", delta, maxDeltaKB)
	}

	totalChecks := orphanSnap.Progress + poolASnap.Progress + poolBSnap.Progress
	t.Logf("scale: %d proxies, %d checks in %v (%.0f checks/s), VmRSS %d -> %d KB (delta %d KB)",
		totalProxies, totalChecks, elapsed.Round(time.Millisecond),
		float64(totalChecks)/elapsed.Seconds(), rss0, rss1, delta)
}

// vmRSSKB reads the process resident set size in KB from /proc.
func vmRSSKB(t *testing.T) int64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("cannot read /proc/self/status: %v", err)
	}
	re := regexp.MustCompile(`(?m)^VmRSS:\s+(\d+) kB`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Skip("VmRSS not found in /proc/self/status")
	}
	kb, _ := strconv.ParseInt(string(m[1]), 10, 64)
	return kb
}

// waitJobStatus polls the job store until the job reaches want (or fails).
func waitJobStatus(t *testing.T, store *HCJobStore, jobID string, want HCJobStatus, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		job, ok := store.Snapshot(jobID)
		if !ok {
			t.Fatalf("job %s vanished from store", jobID)
		}
		if job.Status == want {
			return
		}
		if job.Status == HCJobFailed {
			t.Fatalf("job %s failed: %s", jobID, job.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	job, _ := store.Snapshot(jobID)
	t.Fatalf("job %s status = %q, want %q (within %s)", jobID, job.Status, want, within)
}

// scaleRow / scaleRowSource: one proxy fixture row for pgx CopyFrom.
type scaleRow struct {
	addr, proto, status string
}

type scaleRowSource struct {
	rows []scaleRow
	i    int
}

func (s *scaleRowSource) Next() bool { return s.i < len(s.rows) }
func (s *scaleRowSource) Values() ([]any, error) {
	r := s.rows[s.i]
	s.i++
	return []any{r.addr, r.proto, r.status}, nil
}
func (s *scaleRowSource) Err() error { return nil }

// scaleMember / scaleRowSource2: one pool membership fixture row.
type scaleMember struct{ poolID, proxyID int }

type scaleRowSource2 struct {
	rows []scaleMember
	i    int
}

func (s *scaleRowSource2) Next() bool { return s.i < len(s.rows) }
func (s *scaleRowSource2) Values() ([]any, error) {
	r := s.rows[s.i]
	s.i++
	return []any{r.poolID, r.proxyID}, nil
}
func (s *scaleRowSource2) Err() error { return nil }
