package repository

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openForceCleanupTestDB is the env-gated test database for the force-cleanup
// repository tests: proxies (migration 3 DDL) and pool_proxies, both created
// if missing. Skips when ROTA_TEST_DSN is not set.
func openForceCleanupTestDB(t *testing.T) *pgxpool.Pool {
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
	const createPoolProxies = `
		CREATE TABLE IF NOT EXISTS pool_proxies (
			pool_id  INTEGER NOT NULL,
			proxy_id INTEGER NOT NULL,
			added_at TIMESTAMP NOT NULL DEFAULT NOW(),
			PRIMARY KEY (pool_id, proxy_id)
		);
	`
	if _, err := pool.Exec(ctx, createProxies); err != nil {
		t.Fatalf("create proxies table: %v", err)
	}
	if _, err := pool.Exec(ctx, createPoolProxies); err != nil {
		t.Fatalf("create pool_proxies table: %v", err)
	}

	return pool
}

// hasPoolProxiesFK reports whether the shared test pool_proxies table has a
// foreign key to proxies. The production migration has one (ON DELETE
// CASCADE); the minimal DDL shared with the proxy package does not.
func hasPoolProxiesFK(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE table_name = 'pool_proxies' AND constraint_type = 'FOREIGN KEY'
	`).Scan(&n); err != nil {
		t.Fatalf("check pool_proxies FK: %v", err)
	}
	return n > 0
}

// TestDeleteFailedProxiesBatch verifies the batched force-cleanup delete
// against a real database: 250 failed proxies are removed in batches of 100
// (100, 100, 50, 0), non-failed proxies survive, and pool membership rows of
// deleted proxies are cleared by the FK cascade.
func TestDeleteFailedProxiesBatch(t *testing.T) {
	pool := openForceCleanupTestDB(t)
	ctx := context.Background()
	repo := NewProxyRepository(&database.DB{Pool: pool})

	const prefix = "force-cleanup-"

	// Clean up leftovers from an earlier interrupted run (shared test DB).
	if _, err := pool.Exec(ctx,
		`DELETE FROM pool_proxies WHERE proxy_id IN (SELECT id FROM proxies WHERE address LIKE $1)`,
		prefix+"%"); err != nil {
		t.Fatalf("clean pool_proxies: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM proxies WHERE address LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("clean proxies: %v", err)
	}

	// The exact 100/100/50/0 sequence requires that no other failed proxies
	// exist (or appear) in the shared test DB; skip rather than flake.
	baseline, err := repo.CountFailedProxies(ctx)
	if err != nil {
		t.Fatalf("baseline CountFailedProxies: %v", err)
	}

	const total = 250
	ids := make([]int, 0, total)
	for i := 0; i < total; i++ {
		var id int
		err := pool.QueryRow(ctx,
			`INSERT INTO proxies (address, protocol, status) VALUES ($1, 'http', 'failed') RETURNING id`,
			fmt.Sprintf("%s%d.invalid:8080", prefix, i),
		).Scan(&id)
		if err != nil {
			t.Fatalf("insert failed proxy %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	count, err := repo.CountFailedProxies(ctx)
	if err != nil {
		t.Fatalf("CountFailedProxies: %v", err)
	}
	if count != baseline+total {
		t.Skipf("shared test DB interference: failed count = %d, want %d (baseline %d + %d); skipping exact-sequence assertions",
			count, baseline+total, baseline, total)
	}
	if count != total {
		t.Fatalf("CountFailedProxies = %d, want %d", count, total)
	}

	// Non-failed proxies that must survive the cleanup.
	for _, status := range []string{"active", "idle"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO proxies (address, protocol, status) VALUES ($1, 'http', $2)`,
			fmt.Sprintf("%ssurvive-%s.invalid:8080", prefix, status), status); err != nil {
			t.Fatalf("insert surviving %s proxy: %v", status, err)
		}
	}

	// Attach two failed proxies to a pool to verify the cascade.
	if _, err := pool.Exec(ctx,
		`INSERT INTO pool_proxies (pool_id, proxy_id) VALUES (999001, $1), (999001, $2) ON CONFLICT DO NOTHING`,
		ids[0], ids[1]); err != nil {
		t.Fatalf("insert pool membership: %v", err)
	}

	// Batched deletes: 100, 100, 50, then 0 (nothing left).
	for i, want := range []int{100, 100, 50, 0} {
		got, err := repo.DeleteFailedProxiesBatch(ctx, 100)
		if err != nil {
			t.Fatalf("DeleteFailedProxiesBatch #%d: %v", i, err)
		}
		if got != want {
			t.Fatalf("DeleteFailedProxiesBatch #%d = %d, want %d", i, got, want)
		}
	}

	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM proxies WHERE address LIKE $1`, prefix+"%.invalid%"); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining failed test proxies = %d, want 0", remaining)
	}

	var survivors int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM proxies WHERE address LIKE $1`, prefix+"survive-%"); err != nil {
		t.Fatalf("count survivors: %v", err)
	}
	if survivors != 2 {
		t.Fatalf("surviving non-failed proxies = %d, want 2", survivors)
	}

	if hasPoolProxiesFK(t, pool) {
		var memberships int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM pool_proxies WHERE proxy_id = ANY($1)`, ids[:2]); err != nil {
			t.Fatalf("count memberships: %v", err)
		}
		if memberships != 0 {
			t.Fatalf("pool_proxies rows for deleted proxies = %d, want 0 (FK cascade)", memberships)
		}
	} else {
		t.Log("pool_proxies has no FK in the shared test DB; cascade not asserted")
	}

	// Clean up the surviving rows (shared test DB).
	if _, err := pool.Exec(ctx,
		`DELETE FROM proxies WHERE address LIKE $1`, prefix+"survive-%"); err != nil {
		t.Fatalf("cleanup survivors: %v", err)
	}
}
