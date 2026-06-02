package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alpkeskin/rota/core/internal/config"
	"github.com/alpkeskin/rota/core/pkg/logger"
)

func testGeoIPConfig() config.GeoIPConfig {
	return config.GeoIPConfig{
		QueriesPerMinute:     1000,
		BatchMax:             100,
		MaxRetries:           3,
		CacheTTLHours:        24,
		NegativeCacheMinutes: 5,
	}
}

func TestGeoIPService_Retry429ThenSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode([]ipAPIResponse{{
			Status:      "success",
			Country:     "United States",
			CountryCode: "US",
			Region:      "California",
			City:        "Los Angeles",
			ISP:         "Example ISP",
			Lat:         34.05,
			Lon:         -118.24,
			Query:       "8.8.8.8",
		}})
	}))
	defer srv.Close()

	log := logger.New("error")
	g := newGeoIPServiceForTest(log, testGeoIPConfig(), srv.URL)

	geo, err := g.LookupOne(context.Background(), "8.8.8.8:8080")
	if err != nil {
		t.Fatalf("LookupOne: %v", err)
	}
	if geo.CountryCode != "US" {
		t.Fatalf("country = %q, want US", geo.CountryCode)
	}
	if calls.Load() != 2 {
		t.Fatalf("http calls = %d, want 2", calls.Load())
	}
}

func TestGeoIPService_NegativeCacheAfterFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cfg := testGeoIPConfig()
	cfg.MaxRetries = 0
	log := logger.New("error")
	g := newGeoIPServiceForTest(log, cfg, srv.URL)

	ctx := context.Background()
	_, err := g.LookupOne(ctx, "1.2.3.4:8080")
	if err == nil {
		t.Fatal("expected error on 429")
	}

	_, err = g.LookupOne(ctx, "1.2.3.4:8080")
	if err == nil {
		t.Fatal("expected negative cache skip")
	}
}

func TestGeoIPService_RateLimiterWaits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]ipAPIResponse{{
			Status: "success", CountryCode: "US", Query: "9.9.9.9",
		}})
	}))
	defer srv.Close()

	cfg := config.GeoIPConfig{
		QueriesPerMinute:     2,
		BatchMax:             1,
		MaxRetries:           0,
		CacheTTLHours:        24,
		NegativeCacheMinutes: 0,
	}
	log := logger.New("error")
	g := newGeoIPServiceForTest(log, cfg, srv.URL)

	ctx := context.Background()
	start := time.Now()
	if _, err := g.resolveIPs(ctx, []string{"1.1.1.1"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := g.resolveIPs(ctx, []string{"2.2.2.2"}); err != nil {
		t.Fatalf("second: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 25*time.Second {
		t.Fatalf("expected rate limit wait >= 25s, got %v", elapsed)
	}
}

func TestEnqueueGeoDedupe(t *testing.T) {
	log := logger.New("error")
	s := &SourceService{
		logger:     log,
		geoCh:      make(chan []string, 4),
		geoPending: make(map[string]struct{}),
	}
	n1 := s.enqueueGeo([]string{"1.2.3.4:8080", "5.6.7.8:8080"})
	n2 := s.enqueueGeo([]string{"1.2.3.4:8080"})
	if n1 != 2 {
		t.Fatalf("first enqueue = %d, want 2", n1)
	}
	if n2 != 0 {
		t.Fatalf("duplicate enqueue = %d, want 0", n2)
	}
	if len(s.geoCh) != 1 {
		t.Fatalf("geoCh len = %d, want 1", len(s.geoCh))
	}
}
