package proxy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newExpiredTLSServer starts an HTTPS server whose certificate has already
// expired — simulating a proxy intercepting TLS with a stale certificate.
// (It is also self-signed, so strict mode rejects it with "unknown authority"
// rather than "expired" — the rejection is what matters.)
func newExpiredTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "expired-proxy"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

// newTunnelProxy starts a minimal HTTP CONNECT proxy that tunnels bytes to the
// requested host, so the health check's TLS handshake happens end-to-end
// against the target server.
func newTunnelProxy(t *testing.T) string {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusBadRequest)
			return
		}
		target, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			target.Close()
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		client, _, err := hj.Hijack()
		if err != nil {
			target.Close()
			return
		}
		if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			target.Close()
			client.Close()
			return
		}
		go func() { io.Copy(target, client); target.Close(); client.Close() }()
		go func() { io.Copy(client, target); target.Close(); client.Close() }()
	}))
	t.Cleanup(proxy.Close)
	return strings.TrimPrefix(proxy.URL, "http://")
}

func TestHealthCheckStrictTLS(t *testing.T) {
	ts := newExpiredTLSServer(t)
	proxyAddr := newTunnelProxy(t)

	// Stats writes go to a pool that can't connect; the check result itself
	// does not depend on them (the writes fail fast and are best-effort).
	pool, err := pgxpool.New(context.Background(), "postgres://dead:dead@127.0.0.1:1/dead")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	defer pool.Close()

	h := &HealthChecker{
		tracker: NewUsageTracker(repository.NewProxyRepository(&database.DB{Pool: pool})),
		results: NewResultWriter(repository.NewProxyRepository(&database.DB{Pool: pool}), logger.New("error")),
		logger:  logger.New("error"),
	}

	tests := []struct {
		name    string
		strict  bool
		want    string
		wantErr string
	}{
		{name: "loose accepts expired certificate", strict: false, want: "active"},
		{name: "strict rejects expired certificate", strict: true, want: "failed", wantErr: "TLS/SSL error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h.setSettings(&models.HealthCheckSettings{
				Timeout:   5,
				Workers:   1,
				URL:       ts.URL,
				Status:    http.StatusOK,
				StrictTLS: tc.strict,
				Strategy:  models.StrategyStatus, // test target answers with an empty body
			})

			result, err := h.CheckProxy(context.Background(), &models.Proxy{
				ID:       1,
				Address:  proxyAddr,
				Protocol: "http",
			}, false)
			if err != nil {
				t.Fatalf("CheckProxy: %v", err)
			}
			if result.Status != tc.want {
				t.Fatalf("status = %q, want %q (error: %v)", result.Status, tc.want, result.Error)
			}
			if tc.wantErr != "" {
				if result.Error == nil || !strings.Contains(*result.Error, tc.wantErr) {
					errStr := "nil"
					if result.Error != nil {
						errStr = *result.Error
					}
					t.Fatalf("error = %q, want it to contain %q", errStr, tc.wantErr)
				}
			}
		})
	}
}

// TestCheckProxyBodyStrategy verifies the body-validation strategies: with
// the default "ip" strategy a proxy answering the check URL with a valid
// status code but junk (an HTML captive-portal page) is marked failed, while
// the expected payload (a plain IP) passes. The "status" strategy keeps the
// legacy status-code-only behavior; an empty strategy normalizes to "ip".
func TestCheckProxyBodyStrategy(t *testing.T) {
	// TLS targets: the tunnel proxy tunnels CONNECT (https) end-to-end.
	ipServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("93.184.216.34"))
	}))
	defer ipServer.Close()

	htmlServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<!DOCTYPE html><html><head><title>Login</title></head><body><form>Invalid username or password</form></body></html>"))
	}))
	defer htmlServer.Close()

	pool, err := pgxpool.New(context.Background(), "postgres://dead:dead@127.0.0.1:1/dead")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	defer pool.Close()

	h := &HealthChecker{
		tracker: NewUsageTracker(repository.NewProxyRepository(&database.DB{Pool: pool})),
		results: NewResultWriter(repository.NewProxyRepository(&database.DB{Pool: pool}), logger.New("error")),
		logger:  logger.New("error"),
	}
	proxyAddr := newTunnelProxy(t)

	ipPattern := `^\d{1,3}(\.\d{1,3}){3}$`

	tests := []struct {
		name     string
		url      string
		strategy string
		value    string
		want     string
		wantErr  string
	}{
		{name: "ip body passes ip strategy", url: ipServer.URL, strategy: models.StrategyIP, want: "active"},
		{name: "html body fails ip strategy", url: htmlServer.URL, strategy: models.StrategyIP, want: "failed", wantErr: "body does not contain an IP address"},
		{name: "empty strategy normalizes to ip", url: htmlServer.URL, strategy: "", want: "failed", wantErr: "body does not contain an IP address"},
		{name: "status strategy keeps status-code-only behavior", url: htmlServer.URL, strategy: models.StrategyStatus, want: "active"},
		{name: "regex strategy applies the pattern", url: ipServer.URL, strategy: models.StrategyRegex, value: ipPattern, want: "active"},
		{name: "regex strategy fails junk", url: htmlServer.URL, strategy: models.StrategyRegex, value: ipPattern, want: "failed", wantErr: "body does not match pattern"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h.setSettings(&models.HealthCheckSettings{
				Timeout:       5,
				Workers:       1,
				URL:           tc.url,
				Status:        http.StatusOK,
				Strategy:      tc.strategy,
				StrategyValue: tc.value,
			})
			result, err := h.CheckProxy(context.Background(), &models.Proxy{
				ID:       1,
				Address:  proxyAddr,
				Protocol: "http",
			}, false)
			if err != nil {
				t.Fatalf("CheckProxy: %v", err)
			}
			if result.Status != tc.want {
				t.Fatalf("status = %q, want %q (error: %v)", result.Status, tc.want, result.Error)
			}
			if tc.wantErr != "" {
				if result.Error == nil || !strings.Contains(*result.Error, tc.wantErr) {
					errStr := "nil"
					if result.Error != nil {
						errStr = *result.Error
					}
					t.Fatalf("error = %q, want it to contain %q", errStr, tc.wantErr)
				}
			}
		})
	}
}

// newTestHealthChecker builds a HealthChecker wired to the test DB (env-gated)
// with settings pre-cached, so CheckProxy never touches the settings repo.
func newTestHealthChecker(t *testing.T, db *testDB) *HealthChecker {
	t.Helper()
	return &HealthChecker{
		proxyRepo: db.Repo,
		tracker:   db.Tracker,
		results:   NewResultWriter(db.Repo, logger.New("error")),
		logger:    logger.New("error"),
	}
}

// newLiveProxyTarget starts a TLS target reachable through a CONNECT tunnel
// proxy and returns the proxy address and the target URL.
func newLiveProxyTarget(t *testing.T) (proxyAddr, targetURL string) {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return newTunnelProxy(t), ts.URL
}

// deadProxyAddress returns a 127.0.0.1 address whose port is closed.
func deadProxyAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for dead port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// TestCheckProxyManualImmediate is the regression test for the original bug:
// a user-initiated test must apply its result to the DB status right away —
// dead proxy -> 'failed' (with last_error), live proxy -> 'active'.
func TestCheckProxyManualImmediate(t *testing.T) {
	db := openTestDB(t)
	h := newTestHealthChecker(t, db)

	// Live: healthy target behind the tunnel -> immediate 'active' even from
	// a previously 'failed' status.
	liveAddr, targetURL := newLiveProxyTarget(t)
	liveID := insertTestProxy(t, db.Pool, "10.0.0.10:8080", "failed")
	h.setSettings(&models.HealthCheckSettings{
		Timeout: 5, Workers: 1, URL: targetURL, Status: http.StatusOK,
		Strategy: models.StrategyStatus, // test target answers with an empty body
	})

	res, err := h.CheckProxy(context.Background(), &models.Proxy{
		ID: liveID, Address: liveAddr, Protocol: "http",
	}, true)
	if err != nil {
		t.Fatalf("CheckProxy(live, immediate): %v", err)
	}
	if res.Status != "active" {
		t.Fatalf("live check status = %q, want active (error: %v)", res.Status, res.Error)
	}
	row := pollProxyRow(t, db.Pool, liveID, func(r proxyRow) bool {
		return r.Status == "active" && r.FailedRequests == 0
	})
	if row.LastError != nil {
		t.Errorf("live: last_error = %q, want nil", *row.LastError)
	}

	// Dead: closed port -> immediate 'failed' with a last_error, even from a
	// previously 'active' status.
	deadID := insertTestProxy(t, db.Pool, "10.0.0.11:8080", "active")
	res, err = h.CheckProxy(context.Background(), &models.Proxy{
		ID: deadID, Address: deadProxyAddress(t), Protocol: "http",
	}, true)
	if err != nil {
		t.Fatalf("CheckProxy(dead, immediate): %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("dead check status = %q, want failed", res.Status)
	}
	row = pollProxyRow(t, db.Pool, deadID, func(r proxyRow) bool {
		return r.Status == "failed" && r.LastError != nil && *r.LastError != ""
	})
	if row.FailedRequests != 1 {
		t.Errorf("dead: failed_requests = %d, want 1", row.FailedRequests)
	}
}

// TestCheckProxyPeriodicConsecutive verifies the periodic path (immediate=false)
// keeps the consecutive-failure accounting: 1 failure leaves the status
// unchanged, 3 consecutive failures flip it, a success resets and reactivates.
func TestCheckProxyPeriodicConsecutive(t *testing.T) {
	db := openTestDB(t)
	h := newTestHealthChecker(t, db)

	deadAddr := deadProxyAddress(t)
	id := insertTestProxy(t, db.Pool, "10.0.0.12:8080", "active")
	dead := &models.Proxy{ID: id, Address: deadAddr, Protocol: "http"}
	h.setSettings(&models.HealthCheckSettings{
		Timeout: 5, Workers: 1, URL: "http://127.0.0.1:9", Status: http.StatusOK,
		Strategy: models.StrategyStatus,
	})

	fail := func() {
		t.Helper()
		res, err := h.CheckProxy(context.Background(), dead, false)
		if err != nil {
			t.Fatalf("CheckProxy(dead, periodic): %v", err)
		}
		if res.Status != "failed" {
			t.Fatalf("check status = %q, want failed", res.Status)
		}
	}

	fail()
	pollProxyRow(t, db.Pool, id, func(r proxyRow) bool { return r.FailedRequests == 1 })
	row := readProxyRow(t, db.Pool, id)
	if row.Status != "active" {
		t.Fatalf("after 1 periodic failure: status = %q, want active", row.Status)
	}

	fail()
	fail()
	// Poll the combined end state: the last write to land flips the status.
	pollProxyRow(t, db.Pool, id, func(r proxyRow) bool {
		return r.Status == "failed" && r.FailedRequests == 3
	})

	// Success through a live target: reactivates and resets the counter.
	liveAddr, targetURL := newLiveProxyTarget(t)
	h.setSettings(&models.HealthCheckSettings{
		Timeout: 5, Workers: 1, URL: targetURL, Status: http.StatusOK,
		Strategy: models.StrategyStatus, // test target answers with an empty body
	})
	res, err := h.CheckProxy(context.Background(), &models.Proxy{
		ID: id, Address: liveAddr, Protocol: "http",
	}, false)
	if err != nil {
		t.Fatalf("CheckProxy(live, periodic): %v", err)
	}
	if res.Status != "active" {
		t.Fatalf("live check status = %q, want active (error: %v)", res.Status, res.Error)
	}
	pollProxyRow(t, db.Pool, id, func(r proxyRow) bool {
		return r.Status == "active" && r.FailedRequests == 0
	})
}

// uniqueProxyAddress returns an address unlikely to collide with rows left by
// other tests in the shared ROTA_TEST_DSN database.
func uniqueProxyAddress(t *testing.T, port int) string {
	t.Helper()
	return fmt.Sprintf("10.99.%d.%d:%d",
		mrand.Intn(254)+1, mrand.Intn(254)+1, port)
}

// TestOrphanHealthCheckFilter verifies the global health check only targets
// orphan proxies (not attached to any pool): the orphan is counted and checked,
// the pool proxy is excluded. Counts use a before/after delta so other rows in
// the shared test DB don't affect the assertion.
func TestOrphanHealthCheckFilter(t *testing.T) {
	db := openTestDB(t)
	seedHealthCheckSetting(t, db.Pool)

	settingsRepo := repository.NewSettingsRepository(&database.DB{Pool: db.Pool})
	h := &HealthChecker{
		proxyRepo:    db.Repo,
		settingsRepo: settingsRepo,
		tracker:      db.Tracker,
		results:      NewResultWriter(db.Repo, logger.New("error")),
		logger:       logger.New("error"),
	}

	ctx := context.Background()

	baseOrphan, err := h.CountOrphanProxies(ctx)
	if err != nil {
		t.Fatalf("baseline CountOrphanProxies: %v", err)
	}
	baseIdle, err := h.CountOrphanIdleProxies(ctx)
	if err != nil {
		t.Fatalf("baseline CountOrphanIdleProxies: %v", err)
	}

	orphanID := insertTestProxy(t, db.Pool, uniqueProxyAddress(t, 8080), "idle")
	poolProxyID := insertTestProxy(t, db.Pool, uniqueProxyAddress(t, 8081), "idle")
	insertTestPoolMembership(t, db.Pool, 999, poolProxyID)
	t.Cleanup(func() {
		db.Pool.Exec(context.Background(),
			`DELETE FROM proxies WHERE id IN ($1, $2)`, orphanID, poolProxyID)
	})

	afterOrphan, err := h.CountOrphanProxies(ctx)
	if err != nil {
		t.Fatalf("CountOrphanProxies: %v", err)
	}
	if afterOrphan-baseOrphan != 1 {
		t.Fatalf("orphan count delta = %d, want 1 (only the orphan)", afterOrphan-baseOrphan)
	}

	afterIdle, err := h.CountOrphanIdleProxies(ctx)
	if err != nil {
		t.Fatalf("CountOrphanIdleProxies: %v", err)
	}
	if afterIdle-baseIdle != 1 {
		t.Fatalf("idle orphan count delta = %d, want 1 (orphan is idle, pool proxy is excluded)", afterIdle-baseIdle)
	}

	// CheckAllProxiesWithProgress must check the orphan and skip the pool proxy.
	results, err := h.CheckAllProxiesWithProgress(ctx, nil, false, "")
	if err != nil {
		t.Fatalf("CheckAllProxiesWithProgress: %v", err)
	}
	foundOrphan, foundPool := false, false
	for _, r := range results {
		if r.ID == orphanID {
			foundOrphan = true
		}
		if r.ID == poolProxyID {
			foundPool = true
		}
	}
	if !foundOrphan {
		t.Fatal("orphan proxy missing from CheckAllProxiesWithProgress results")
	}
	if foundPool {
		t.Fatal("pool proxy must not be checked by CheckAllProxiesWithProgress")
	}
}

// TestBatchCheckResultsMatchSequential verifies the batched result writer is
// equivalent to the sequential RecordHealthCheck/RecordManualTestResult writes
// on the same record sequences: same final status, failed_requests and
// last_error for periodic hysteresis, immediate manual/pool semantics, and
// mixed windows.
func TestBatchCheckResultsMatchSequential(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	now := time.Now()
	rec := func(id int, success bool, kind CheckResultKind) CheckResultRecord {
		return CheckResultRecord{ProxyID: id, Success: success, Kind: kind, LastError: "boom", Timestamp: now}
	}

	type seq struct {
		name string
		init string // initial status of both proxies
		recs []CheckResultRecord
	}
	seqs := []seq{
		{"three periodic fails flip to failed", "active", []CheckResultRecord{
			rec(1, false, KindPeriodic), rec(1, false, KindPeriodic), rec(1, false, KindPeriodic),
		}},
		{"two fails then success resets", "active", []CheckResultRecord{
			rec(1, false, KindPeriodic), rec(1, false, KindPeriodic), rec(1, true, KindPeriodic),
		}},
		{"success mid window then trailing fails", "active", []CheckResultRecord{
			rec(1, false, KindPeriodic), rec(1, true, KindPeriodic),
			rec(1, false, KindPeriodic), rec(1, false, KindPeriodic), rec(1, false, KindPeriodic),
		}},
		{"single periodic fail keeps status", "active", []CheckResultRecord{
			rec(1, false, KindPeriodic),
		}},
		{"manual fail is immediate", "active", []CheckResultRecord{
			rec(1, false, KindManual),
		}},
		{"pool fail is immediate", "active", []CheckResultRecord{
			rec(1, false, KindPool),
		}},
		{"pool success reactivates", "failed", []CheckResultRecord{
			rec(1, true, KindPool),
		}},
		{"mixed manual and periodic fails", "active", []CheckResultRecord{
			rec(1, false, KindPeriodic), rec(1, false, KindManual), rec(1, false, KindPeriodic),
		}},
		{"manual success after periodic fails", "active", []CheckResultRecord{
			rec(1, false, KindPeriodic), rec(1, false, KindPeriodic), rec(1, true, KindManual),
		}},
	}

	for _, s := range seqs {
		t.Run(s.name, func(t *testing.T) {
			baseID := insertTestProxy(t, db.Pool, uniqueProxyAddress(t, 8095), s.init)
			batchID := insertTestProxy(t, db.Pool, uniqueProxyAddress(t, 8096), s.init)
			t.Cleanup(func() {
				db.Pool.Exec(context.Background(),
					`DELETE FROM proxies WHERE id IN ($1, $2)`, baseID, batchID)
			})

			// Sequential baseline.
			for i, r := range s.recs {
				switch r.Kind {
				case KindPeriodic:
					if err := db.Tracker.RecordHealthCheck(ctx, baseID, r.Success, 10, r.LastError); err != nil {
						t.Fatalf("sequential RecordHealthCheck #%d: %v", i, err)
					}
				default: // manual and pool share the immediate semantics
					if err := db.Tracker.RecordManualTestResult(ctx, baseID, r.Success, r.LastError); err != nil {
						t.Fatalf("sequential RecordManualTestResult #%d: %v", i, err)
					}
				}
			}

			// Batched path.
			w := NewResultWriter(db.Repo, logger.New("error"))
			for i := range s.recs {
				r := s.recs[i]
				w.Record(CheckResultRecord{ProxyID: batchID, Success: r.Success, Kind: r.Kind, LastError: r.LastError, Timestamp: r.Timestamp})
			}
			w.Drain(ctx)
			w.Stop()

			base := readProxyRow(t, db.Pool, baseID)
			batch := readProxyRow(t, db.Pool, batchID)
			if base.Status != batch.Status {
				t.Errorf("status: batch = %q, sequential = %q", batch.Status, base.Status)
			}
			if base.FailedRequests != batch.FailedRequests {
				t.Errorf("failed_requests: batch = %d, sequential = %d", batch.FailedRequests, base.FailedRequests)
			}
			baseErr, batchErr := "", ""
			if base.LastError != nil {
				baseErr = *base.LastError
			}
			if batch.LastError != nil {
				batchErr = *batch.LastError
			}
			if baseErr != batchErr {
				t.Errorf("last_error: batch = %q, sequential = %q", batchErr, baseErr)
			}
		})
	}
}

// TestResultWriterSurvivesClosedDB verifies the writer is resilient: records
// queued around a dead database are flushed (the flush fails and is logged),
// and neither Record nor Drain panics — so a job keeps running when the DB
// goes away mid-run.
func TestResultWriterSurvivesClosedDB(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	id := insertTestProxy(t, db.Pool, uniqueProxyAddress(t, 8097), "active")
	t.Cleanup(func() {
		db.Pool.Exec(context.Background(), `DELETE FROM proxies WHERE id = $1`, id)
	})

	pool := db.Pool
	repo := db.Repo
	pool.Close() // kill the DB before the "job" runs

	w := NewResultWriter(repo, logger.New("error"))
	defer w.Stop()

	for i := 0; i < 10; i++ {
		w.Record(CheckResultRecord{ProxyID: id, Success: false, Kind: KindPeriodic, LastError: "db down", Timestamp: time.Now()})
	}
	w.Drain(ctx) // flush must fail gracefully, not panic
	w.Record(CheckResultRecord{ProxyID: id, Success: true, Kind: KindPeriodic, Timestamp: time.Now()})
	w.Drain(ctx)
}

// TestCheckAllProxiesKeysetPagination verifies the keyset sweep over more
// than one batch (hcBatchSize = 1000): every inserted orphan is checked
// exactly once (no duplicates, no skips) across 2500 rows.
func TestCheckAllProxiesKeysetPagination(t *testing.T) {
	db := openTestDB(t)
	seedHealthCheckSetting(t, db.Pool)

	settingsRepo := repository.NewSettingsRepository(&database.DB{Pool: db.Pool})
	h := &HealthChecker{
		proxyRepo:    db.Repo,
		settingsRepo: settingsRepo,
		tracker:      db.Tracker,
		results:      NewResultWriter(db.Repo, logger.New("error")),
		logger:       logger.New("error"),
	}
	// Fast, parallel checks against a dead port.
	h.setSettings(&models.HealthCheckSettings{
		Timeout: 1, Workers: 8, URL: "http://127.0.0.1:1", Status: http.StatusOK,
	})

	ctx := context.Background()
	const total = 2500
	runTag := mrand.Intn(100000)
	prefix := fmt.Sprintf("ks-%d-", runTag)

	// One multi-row INSERT for speed.
	var sb strings.Builder
	sb.WriteString("INSERT INTO proxies (address, protocol, status) VALUES ")
	for i := 0; i < total; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "('%s%d.invalid:8080','http','idle')", prefix, i)
	}
	if _, err := db.Pool.Exec(ctx, sb.String()); err != nil {
		t.Fatalf("insert keyset fixtures: %v", err)
	}
	t.Cleanup(func() {
		db.Pool.Exec(context.Background(),
			`DELETE FROM proxies WHERE address LIKE $1`, prefix+"%")
	})

	var ids []int
	rows, err := db.Pool.Query(ctx,
		`SELECT id FROM proxies WHERE address LIKE $1 ORDER BY id`, prefix+"%")
	if err != nil {
		t.Fatalf("read fixture ids: %v", err)
	}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan fixture id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != total {
		t.Fatalf("fixture size = %d, want %d", len(ids), total)
	}

	results, err := h.CheckAllProxiesWithProgress(ctx, nil, false, "")
	if err != nil {
		t.Fatalf("CheckAllProxiesWithProgress: %v", err)
	}

	counts := make(map[int]int, len(results))
	for _, r := range results {
		counts[r.ID]++
	}
	var missing, duplicated []int
	for _, id := range ids {
		switch counts[id] {
		case 0:
			missing = append(missing, id)
		case 1:
		default:
			duplicated = append(duplicated, id)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d proxies were skipped (first: %d)", len(missing), missing[0])
	}
	if len(duplicated) > 0 {
		t.Errorf("%d proxies were checked more than once (first: %d, count: %d)",
			len(duplicated), duplicated[0], counts[duplicated[0]])
	}
}

// TestOrphanHealthCheckTTLFilter verifies the TTL cutoff: a "fresh" orphan
// (last_check inside the TTL window) is excluded from the sweep selection and
// the counts, while a stale one (last_check older than the TTL) and an
// unchecked one (last_check IS NULL) are included. ttl=0 disables the filter.
func TestOrphanHealthCheckTTLFilter(t *testing.T) {
	db := openTestDB(t)

	// Upsert (not insert-if-missing) so the TTL values are deterministic.
	upsertHealthCheckSetting := func(ttlMinutes int) {
		t.Helper()
		value := fmt.Sprintf(
			`{"timeout": 1, "workers": 2, "url": "http://127.0.0.1:1", "status": 200, "headers": [], "orphan_ttl_minutes": %d, "idle_ttl_minutes": %d}`,
			ttlMinutes, ttlMinutes,
		)
		if _, err := db.Pool.Exec(context.Background(), `
			INSERT INTO settings (key, value) VALUES ('healthcheck', $1::jsonb)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
		`, value); err != nil {
			t.Fatalf("upsert healthcheck setting: %v", err)
		}
	}
	upsertHealthCheckSetting(60)

	settingsRepo := repository.NewSettingsRepository(&database.DB{Pool: db.Pool})
	h := &HealthChecker{
		proxyRepo:    db.Repo,
		settingsRepo: settingsRepo,
		tracker:      db.Tracker,
		results:      NewResultWriter(db.Repo, logger.New("error")),
		logger:       logger.New("error"),
	}

	ctx := context.Background()
	setLastCheck := func(id int, expr string) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx,
			`UPDATE proxies SET last_check = `+expr+` WHERE id = $1`, id); err != nil {
			t.Fatalf("set last_check for %d: %v", id, err)
		}
	}

	baseOrphan, err := h.CountOrphanProxies(ctx)
	if err != nil {
		t.Fatalf("baseline CountOrphanProxies: %v", err)
	}
	baseIdle, err := h.CountOrphanIdleProxies(ctx)
	if err != nil {
		t.Fatalf("baseline CountOrphanIdleProxies: %v", err)
	}

	freshID := insertTestProxy(t, db.Pool, uniqueProxyAddress(t, 8090), "idle")
	staleID := insertTestProxy(t, db.Pool, uniqueProxyAddress(t, 8091), "idle")
	nullID := insertTestProxy(t, db.Pool, uniqueProxyAddress(t, 8092), "idle")
	setLastCheck(freshID, "NOW()")
	setLastCheck(staleID, "NOW() - INTERVAL '2 hours'")
	// nullID keeps last_check = NULL
	t.Cleanup(func() {
		db.Pool.Exec(context.Background(),
			`DELETE FROM proxies WHERE id IN ($1, $2, $3)`, freshID, staleID, nullID)
	})

	afterOrphan, err := h.CountOrphanProxies(ctx)
	if err != nil {
		t.Fatalf("CountOrphanProxies: %v", err)
	}
	if afterOrphan-baseOrphan != 2 {
		t.Fatalf("orphan count delta = %d, want 2 (stale + null; fresh excluded)", afterOrphan-baseOrphan)
	}

	afterIdle, err := h.CountOrphanIdleProxies(ctx)
	if err != nil {
		t.Fatalf("CountOrphanIdleProxies: %v", err)
	}
	if afterIdle-baseIdle != 2 {
		t.Fatalf("idle orphan count delta = %d, want 2 (stale + null; fresh excluded)", afterIdle-baseIdle)
	}

	// The sweep checks exactly the stale and null proxies.
	results, err := h.CheckOrphanIdleProxiesWithProgress(ctx, nil, false, "")
	if err != nil {
		t.Fatalf("CheckOrphanIdleProxiesWithProgress: %v", err)
	}
	checked := make(map[int]bool, len(results))
	for _, r := range results {
		checked[r.ID] = true
	}
	if !checked[staleID] || !checked[nullID] {
		t.Fatal("stale and null proxies must be checked")
	}
	if checked[freshID] {
		t.Fatal("fresh proxy (last_check inside the TTL window) must be skipped")
	}

	// The sweep refreshed last_check for the checked proxies; restore the TTL
	// states and verify ttl=0 disables the filter: the fresh proxy reappears
	// in the sweep (scoped assertion — global counts are affected by rows
	// left in the shared test DB by other tests).
	setLastCheck(freshID, "NOW()")
	setLastCheck(staleID, "NOW() - INTERVAL '2 hours'")
	setLastCheck(nullID, "NULL")
	upsertHealthCheckSetting(0)

	results, err = h.CheckOrphanIdleProxiesWithProgress(ctx, nil, false, "")
	if err != nil {
		t.Fatalf("CheckOrphanIdleProxiesWithProgress (ttl=0): %v", err)
	}
	checked = make(map[int]bool, len(results))
	for _, r := range results {
		checked[r.ID] = true
	}
	if !checked[freshID] {
		t.Fatal("fresh proxy must be checked when the TTL filter is disabled (ttl=0)")
	}
}
