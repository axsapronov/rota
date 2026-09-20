package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/proxy"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/internal/services"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openHandlerTestDB is the env-gated test database for handler integration
// tests: proxies (migration 3 DDL) and settings (migration 4 DDL) tables,
// both truncated. Skips when ROTA_TEST_DSN is not set.
func openHandlerTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

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

	// Non-destructive DDL: test packages run in parallel against the same
	// ROTA_TEST_DSN, so the tables are created if missing and left alone
	// otherwise (tests always work with freshly inserted rows).
	const createProxies = `
		CREATE TABLE IF NOT EXISTS proxies (
			id SERIAL PRIMARY KEY,
			address VARCHAR(255) NOT NULL,
			protocol VARCHAR(20) NOT NULL DEFAULT 'http',
			username VARCHAR(255),
			password TEXT,
			status VARCHAR(20) NOT NULL DEFAULT 'idle',
			requests BIGINT NOT NULL DEFAULT 0,
			successful_requests BIGINT NOT NULL DEFAULT 0,
			failed_requests BIGINT NOT NULL DEFAULT 0,
			avg_response_time INTEGER DEFAULT 0,
			last_check TIMESTAMP,
			last_error TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
	`
	// The geo (migration 11) and tags (migration 17) columns, which
	// ProxyRepository.GetByID scans; added idempotently if missing.
	const addProxyColumns = `
		ALTER TABLE proxies
			ADD COLUMN IF NOT EXISTS country_code   VARCHAR(3),
			ADD COLUMN IF NOT EXISTS country_name   VARCHAR(100),
			ADD COLUMN IF NOT EXISTS region_name    VARCHAR(100),
			ADD COLUMN IF NOT EXISTS city_name      VARCHAR(100),
			ADD COLUMN IF NOT EXISTS latitude       DOUBLE PRECISION,
			ADD COLUMN IF NOT EXISTS longitude      DOUBLE PRECISION,
			ADD COLUMN IF NOT EXISTS isp            VARCHAR(255),
			ADD COLUMN IF NOT EXISTS geo_updated_at TIMESTAMP,
			ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';
	`
	const createSettings = `
		CREATE TABLE IF NOT EXISTS settings (
			key VARCHAR(255) PRIMARY KEY,
			value JSONB NOT NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
	`
	if _, err := pool.Exec(ctx, createProxies); err != nil {
		t.Fatalf("create proxies table: %v", err)
	}
	if _, err := pool.Exec(ctx, addProxyColumns); err != nil {
		t.Fatalf("add proxy columns: %v", err)
	}
	if _, err := pool.Exec(ctx, createSettings); err != nil {
		t.Fatalf("create settings table: %v", err)
	}
	// Only the settings table is truncated: it is touched by this package's
	// tests only (the proxies table is shared with the proxy package's tests).
	if _, err := pool.Exec(ctx, "TRUNCATE settings"); err != nil {
		t.Fatalf("truncate settings: %v", err)
	}
	// Health-check settings with a short timeout; the target URL is
	// irrelevant (a dead proxy fails before the target is reached).
	if _, err := pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES
		('healthcheck', '{"timeout": 5, "workers": 2, "url": "http://127.0.0.1:9", "status": 200, "headers": []}'::jsonb)
	`); err != nil {
		t.Fatalf("insert healthcheck settings: %v", err)
	}

	return pool
}

// TestProxyHandlerTestManualImmediate is the end-to-end regression test for
// the API path: POST /proxies/{id}/test against a dead proxy must apply
// 'failed' to the DB status immediately (the original bug: manual test
// results only took effect after 3 consecutive periodic failures).
func TestProxyHandlerTestManualImmediate(t *testing.T) {
	pool := openHandlerTestDB(t)
	ctx := context.Background()

	repo := repository.NewProxyRepository(&database.DB{Pool: pool})
	settingsRepo := repository.NewSettingsRepository(&database.DB{Pool: pool})
	tracker := proxy.NewUsageTracker(repo)
	healthChecker := proxy.NewHealthChecker(repo, settingsRepo, tracker, logger.New("error"))
	handler := NewProxyHandler(repo, healthChecker, services.NewForceCleanupService(repo, logger.New("error")), services.NewProxyCleanupService(repo, settingsRepo, logger.New("error")), logger.New("error"))

	var id int
	err := pool.QueryRow(ctx,
		`INSERT INTO proxies (address, protocol, status) VALUES ($1, 'http', 'active') RETURNING id`,
		"127.0.0.1:1",
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert dead proxy: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/proxies/{id}/test", handler.Test)

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/proxies/%d/test", id), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var result models.ProxyTestResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("result status = %q, want failed (error: %v)", result.Status, result.Error)
	}

	// The DB write is asynchronous (separate goroutine) — poll for it.
	deadline := time.Now().Add(5 * time.Second)
	var status string
	var lastError *string
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx,
			`SELECT status, last_error FROM proxies WHERE id = $1`, id,
		).Scan(&status, &lastError); err != nil {
			t.Fatalf("read proxy: %v", err)
		}
		if status == "failed" && lastError != nil && *lastError != "" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("proxy status = %q (last_error = %v), want failed with last_error within 5s", status, lastError)
}

// newTestProxyHandler builds a fully wired handler against the shared test DB.
func newTestProxyHandler(t *testing.T, pool *pgxpool.Pool) *ProxyHandler {
	t.Helper()
	repo := repository.NewProxyRepository(&database.DB{Pool: pool})
	settingsRepo := repository.NewSettingsRepository(&database.DB{Pool: pool})
	tracker := proxy.NewUsageTracker(repo)
	healthChecker := proxy.NewHealthChecker(repo, settingsRepo, tracker, logger.New("error"))
	return NewProxyHandler(repo, healthChecker,
		services.NewForceCleanupService(repo, logger.New("error")),
		services.NewProxyCleanupService(repo, settingsRepo, logger.New("error")),
		logger.New("error"))
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

// ensureGlobalJobStoreQueue makes the global job store's queue usable for
// tests that enqueue jobs. The consumer may exit when the test context is
// cancelled; the queue itself remains, so later enqueues succeed.
func ensureGlobalJobStoreQueue(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	services.GetJobStore().Start(ctx, nil)
	t.Cleanup(cancel)
}

// TestProxyHandlerListCountryFilter verifies the server-side country_code
// filter: exact match, case-insensitive input, and invalid (too long) codes
// yielding an empty result instead of an error.
func TestProxyHandlerListCountryFilter(t *testing.T) {
	pool := openHandlerTestDB(t)
	ctx := context.Background()
	handler := newTestProxyHandler(t, pool)

	const prefix = "country-filter-"
	// Clean up leftovers from an earlier interrupted run (shared test DB).
	if _, err := pool.Exec(ctx, `DELETE FROM proxies WHERE address LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("clean proxies: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM proxies WHERE address LIKE $1`, prefix+"%") //nolint:errcheck
	})

	for i, cc := range []string{"XX", "XX", "YY"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO proxies (address, protocol, status, country_code) VALUES ($1, 'http', 'active', $2)`,
			fmt.Sprintf("%s%d.invalid:8080", prefix, i), cc); err != nil {
			t.Fatalf("insert proxy %d: %v", i, err)
		}
	}

	r := chi.NewRouter()
	r.Get("/proxies", handler.List)

	getProxies := func(query string) models.ProxyListResponse {
		req := httptest.NewRequest(http.MethodGet, "/proxies"+query, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var res models.ProxyListResponse
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return res
	}

	if res := getProxies("?country_code=XX"); res.Pagination.Total != 2 {
		t.Fatalf("country_code=XX total = %d, want 2", res.Pagination.Total)
	}
	if res := getProxies("?country_code=xx"); res.Pagination.Total != 2 {
		t.Fatalf("lowercase country_code=xx total = %d, want 2", res.Pagination.Total)
	} else {
		for _, p := range res.Proxies {
			if p.CountryCode == nil || *p.CountryCode != "XX" {
				t.Fatalf("lowercase filter returned proxy with country_code = %v, want XX", p.CountryCode)
			}
		}
	}
	if res := getProxies("?country_code=TOOLONG"); res.Pagination.Total != 0 {
		t.Fatalf("invalid country_code total = %d, want 0", res.Pagination.Total)
	}
	if res := getProxies("?country_code=YY"); res.Pagination.Total != 1 {
		t.Fatalf("country_code=YY total = %d, want 1", res.Pagination.Total)
	}
}

// TestProxyHandlerListLastCheckSort verifies server-side sorting by
// last_check with NULLS LAST in both directions.
func TestProxyHandlerListLastCheckSort(t *testing.T) {
	pool := openHandlerTestDB(t)
	ctx := context.Background()
	handler := newTestProxyHandler(t, pool)

	const prefix = "nulls-last-"
	if _, err := pool.Exec(ctx, `DELETE FROM proxies WHERE address LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("clean proxies: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM proxies WHERE address LIKE $1`, prefix+"%") //nolint:errcheck
	})

	now := time.Now()
	seed := func(addr string, lastCheck *time.Time) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO proxies (address, protocol, status, last_check) VALUES ($1, 'http', 'active', $2)`,
			addr, lastCheck); err != nil {
			t.Fatalf("insert %s: %v", addr, err)
		}
	}
	seed(prefix+"a.invalid:8080", ptrTime(now.Add(-2*time.Hour)))
	seed(prefix+"b.invalid:8080", ptrTime(now))
	seed(prefix+"c.invalid:8080", nil)

	r := chi.NewRouter()
	r.Get("/proxies", handler.List)

	orderedAddresses := func(order string) []string {
		req := httptest.NewRequest(http.MethodGet,
			"/proxies?search="+prefix+"&sort=last_check&order="+order, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var res models.ProxyListResponse
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		addrs := make([]string, 0, len(res.Proxies))
		for _, p := range res.Proxies {
			addrs = append(addrs, p.Address)
		}
		return addrs
	}

	// ASC: oldest first, NULL (c) last.
	if got := orderedAddresses("asc"); strings.Join(got, ",") != prefix+"a.invalid:8080,"+prefix+"b.invalid:8080,"+prefix+"c.invalid:8080" {
		t.Fatalf("asc order = %v, want a,b,c (null last)", got)
	}
	// DESC: newest first, NULL (c) still last (explicit NULLS LAST).
	if got := orderedAddresses("desc"); strings.Join(got, ",") != prefix+"b.invalid:8080,"+prefix+"a.invalid:8080,"+prefix+"c.invalid:8080" {
		t.Fatalf("desc order = %v, want b,a,c (null last)", got)
	}
}

// TestProxyHandlerTestBulk verifies the bulk test endpoint: 202 with the job
// metadata, job status lookup by kind, and 400 on an empty id list.
func TestProxyHandlerTestBulk(t *testing.T) {
	pool := openHandlerTestDB(t)
	ctx := context.Background()
	handler := newTestProxyHandler(t, pool)
	ensureGlobalJobStoreQueue(t)

	const prefix = "bulk-test-"
	if _, err := pool.Exec(ctx, `DELETE FROM proxies WHERE address LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("clean proxies: %v", err)
	}
	var ids []int
	for i := 0; i < 2; i++ {
		var id int
		if err := pool.QueryRow(ctx,
			`INSERT INTO proxies (address, protocol, status) VALUES ($1, 'http', 'active') RETURNING id`,
			fmt.Sprintf("%s%d.invalid:8080", prefix, i),
		).Scan(&id); err != nil {
			t.Fatalf("insert proxy %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM proxies WHERE address LIKE $1`, prefix+"%") //nolint:errcheck
	})

	r := chi.NewRouter()
	r.Post("/proxies/test/bulk", handler.TestBulk)
	r.Get("/proxies/test/{job_id}", handler.TestJobStatus)

	// Empty id list → 400.
	req := httptest.NewRequest(http.MethodPost, "/proxies/test/bulk",
		strings.NewReader(`{"proxy_ids": []}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty ids: status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}

	body, _ := json.Marshal(models.BulkTestProxyRequest{ProxyIDs: ids, Workers: 2})
	req = httptest.NewRequest(http.MethodPost, "/proxies/test/bulk", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	var start struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
		Total  int    `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&start); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if start.JobID == "" || start.Status != "pending" || start.Total != 2 {
		t.Fatalf("start response = %+v, want pending job with total 2", start)
	}

	// Job status lookup must accept the proxy kind.
	req = httptest.NewRequest(http.MethodGet, "/proxies/test/"+start.JobID, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("job status: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var job services.HCJob
	if err := json.NewDecoder(rec.Body).Decode(&job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.Kind != services.HCJobKindProxy {
		t.Fatalf("job kind = %s, want proxy", job.Kind)
	}
	if len(job.ProxyIDs) != 2 || job.ProxyIDs[0] != ids[0] || job.ProxyIDs[1] != ids[1] {
		t.Fatalf("job proxy_ids = %v, want %v", job.ProxyIDs, ids)
	}
}

// TestProxyHandlerCleanupNow verifies the manual cleanup endpoint: it deletes
// failed proxies older than max_failed_days (ignoring the Enabled flag) and
// returns the deleted count.
func TestProxyHandlerCleanupNow(t *testing.T) {
	pool := openHandlerTestDB(t)
	ctx := context.Background()
	handler := newTestProxyHandler(t, pool)

	const prefix = "cleanup-now-"
	// Clean up leftovers from an earlier interrupted run (shared test DB).
	if _, err := pool.Exec(ctx, `DELETE FROM proxies WHERE address LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("clean proxies: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM proxies WHERE address LIKE $1`, prefix+"%") //nolint:errcheck
	})

	// Manual run must work with the cleanup disabled in settings.
	if _, err := pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ('proxy_cleanup',
			'{"enabled": false, "max_failed_days": 1, "min_success_rate": 0, "cleanup_interval_hours": 24}'::jsonb)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatalf("insert proxy_cleanup settings: %v", err)
	}

	// The manual delete is global; skip rather than flake if other dead
	// proxies exist in the shared test DB.
	var baseline int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM proxies
		WHERE status = 'failed' AND last_check < NOW() - INTERVAL '1 day'
	`).Scan(&baseline); err != nil {
		t.Fatalf("count baseline dead proxies: %v", err)
	}
	if baseline > 0 {
		t.Skipf("shared test DB interference: %d pre-existing dead proxies", baseline)
	}

	var oldID, freshID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO proxies (address, protocol, status, last_check)
		 VALUES ($1, 'http', 'failed', NOW() - INTERVAL '10 days') RETURNING id`,
		prefix+"old.invalid:8080",
	).Scan(&oldID); err != nil {
		t.Fatalf("insert old failed proxy: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO proxies (address, protocol, status, last_check)
		 VALUES ($1, 'http', 'failed', NOW()) RETURNING id`,
		prefix+"fresh.invalid:8080",
	).Scan(&freshID); err != nil {
		t.Fatalf("insert fresh failed proxy: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/proxies/cleanup/run", handler.CleanupNow)

	req := httptest.NewRequest(http.MethodPost, "/proxies/cleanup/run", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var res struct {
		Deleted int `json:"deleted"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if res.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", res.Deleted)
	}

	var oldRemaining, freshRemaining int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM proxies WHERE id = $1`, oldID).Scan(&oldRemaining); err != nil {
		t.Fatalf("count old: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM proxies WHERE id = $1`, freshID).Scan(&freshRemaining); err != nil {
		t.Fatalf("count fresh: %v", err)
	}
	if oldRemaining != 0 {
		t.Fatal("old failed proxy was not deleted")
	}
	if freshRemaining != 1 {
		t.Fatal("fresh failed proxy was deleted, want it kept")
	}
}
