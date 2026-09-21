package services

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alpkeskin/rota/core/internal/database"
	"github.com/alpkeskin/rota/core/internal/models"
	"github.com/alpkeskin/rota/core/internal/proxy"
	"github.com/alpkeskin/rota/core/internal/repository"
	"github.com/alpkeskin/rota/core/pkg/logger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newTunnelProxyForTest starts a minimal HTTP CONNECT proxy that tunnels bytes
// to the requested host, so pool checks run end-to-end through a proxy.
func newTunnelProxyForTest(t *testing.T) string {
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

// TestPoolCheckOneProxyBodyStrategy verifies pool sweeps apply the global
// body-validation strategy: with the default "ip" strategy a proxy answering
// with a valid status code but an HTML page is marked failed, while the
// expected IP payload stays active; the "status" strategy keeps the legacy
// status-code-only behavior.
func TestPoolCheckOneProxyBodyStrategy(t *testing.T) {
	// TLS targets: the tunnel proxy tunnels CONNECT (https) end-to-end.
	ipServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("93.184.216.34"))
	}))
	defer ipServer.Close()

	htmlServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<!DOCTYPE html><html><head><title>Login</title></head></html>"))
	}))
	defer htmlServer.Close()

	proxyAddr := newTunnelProxyForTest(t)

	// Stats/DB writes go to a dead pool; the check result itself does not
	// depend on them (the fallback status write fails fast and is logged).
	pool, err := pgxpool.New(context.Background(), "postgres://dead:dead@127.0.0.1:1/dead")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	defer pool.Close()

	ps := NewPoolService(nil, repository.NewProxyRepository(&database.DB{Pool: pool}), logger.New("error"))
	ps.SetHealthCheckBodyStrategy(func() proxy.BodyStrategy {
		return proxy.BodyStrategy{Strategy: models.StrategyIP}
	})

	upstream := &models.Proxy{ID: 1, Address: proxyAddr, Protocol: "http"}

	res := ps.checkOneProxyTimeout(context.Background(), upstream, ipServer.URL, 5*time.Second)
	if res.Status != "active" {
		t.Fatalf("ip body: status = %q, want active (error: %v)", res.Status, res.Error)
	}

	res = ps.checkOneProxyTimeout(context.Background(), upstream, htmlServer.URL, 5*time.Second)
	if res.Status != "failed" {
		t.Fatalf("html body: status = %q, want failed", res.Status)
	}
	if res.Error == nil || !strings.Contains(*res.Error, "body does not contain an IP address") {
		errStr := "nil"
		if res.Error != nil {
			errStr = *res.Error
		}
		t.Fatalf("html body: error = %q, want it to contain %q", errStr, "body does not contain an IP address")
	}

	// The status strategy keeps the legacy status-code-only behavior.
	ps.SetHealthCheckBodyStrategy(func() proxy.BodyStrategy {
		return proxy.BodyStrategy{Strategy: models.StrategyStatus}
	})
	res = ps.checkOneProxyTimeout(context.Background(), upstream, htmlServer.URL, 5*time.Second)
	if res.Status != "active" {
		t.Fatalf("status strategy: status = %q, want active (error: %v)", res.Status, res.Error)
	}
}
