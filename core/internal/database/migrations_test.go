package database

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMigrateIdempotent runs the full migration suite on a scratch database
// (dropped and recreated per run) and asserts the second run is a clean no-op,
// and that the last_check partial index exists with its predicate. Skips when
// ROTA_TEST_DSN is not set.
func TestMigrateIdempotent(t *testing.T) {
	dsn := os.Getenv("ROTA_TEST_DSN")
	if dsn == "" {
		t.Skip("ROTA_TEST_DSN not set")
	}

	ctx := context.Background()

	baseCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, baseCfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer adminPool.Close()
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	const scratchDB = "rota_test_migrations"
	if _, err := adminPool.Exec(ctx, `DROP DATABASE IF EXISTS `+scratchDB); err != nil {
		t.Fatalf("drop scratch db: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+scratchDB); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(func() {
		adminPool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+scratchDB)
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DSN (scratch): %v", err)
	}
	cfg.ConnConfig.Database = scratchDB
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect scratch db: %v", err)
	}
	defer pool.Close()

	db := &DB{Pool: pool, logger: logger.New("warn")}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second migrate (must be a no-op): %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_proxies_last_check'`).Scan(&count); err != nil {
		t.Fatalf("query index: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one idx_proxies_last_check, got %d", count)
	}

	var indexDef string
	if err := pool.QueryRow(ctx,
		`SELECT pg_get_indexdef('idx_proxies_last_check'::regclass)`).Scan(&indexDef); err != nil {
		t.Fatalf("index def: %v", err)
	}
	// Postgres normalizes the predicate to WHERE ((status)::text <> 'dead'::text).
	for _, part := range []string{"WHERE", "<>", "status", "dead"} {
		if !strings.Contains(indexDef, part) {
			t.Errorf("index predicate missing %q: %s", part, indexDef)
		}
	}
	if !strings.Contains(indexDef, "last_check") {
		t.Errorf("index does not cover last_check: %s", indexDef)
	}
}
