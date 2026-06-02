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
		BatchRequestsPerMinute: 15,
		BatchSize:              100,
		MaxRetries:             3,
	}
}

func TestGeoIPService_LookupBatchSingleHTTP(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]ipAPIResponse{
			{Status: "success", CountryCode: "US", Query: "8.8.8.8"},
			{Status: "success", CountryCode: "DE", Query: "1.1.1.1"},
		})
	}))
	defer srv.Close()

	log := logger.New("error")
	g := newGeoIPServiceForTest(log, testGeoIPConfig(), srv.URL)

	geos, failed, err := g.LookupBatch(context.Background(), []string{"8.8.8.8", "1.1.1.1"})
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("failed = %d, want 0", len(failed))
	}
	if len(geos) != 2 {
		t.Fatalf("results = %d, want 2", len(geos))
	}
	if calls.Load() != 1 {
		t.Fatalf("http calls = %d, want 1", calls.Load())
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

	geos, failed, err := g.LookupBatch(context.Background(), []string{"8.8.8.8"})
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("failed = %d, want 0", len(failed))
	}
	if geos["8.8.8.8"].CountryCode != "US" {
		t.Fatalf("country = %q, want US", geos["8.8.8.8"].CountryCode)
	}
	if calls.Load() != 2 {
		t.Fatalf("http calls = %d, want 2", calls.Load())
	}
}

func TestGeoIPService_BatchRateLimiterWaits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]ipAPIResponse{{
			Status: "success", CountryCode: "US", Query: "9.9.9.9",
		}})
	}))
	defer srv.Close()

	cfg := config.GeoIPConfig{
		BatchRequestsPerMinute: 15,
		BatchSize:              100,
		MaxRetries:             0,
	}
	log := logger.New("error")
	g := newGeoIPServiceForTest(log, cfg, srv.URL)

	ctx := context.Background()
	start := time.Now()
	if _, _, err := g.LookupBatch(ctx, []string{"1.1.1.1"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, _, err := g.LookupBatch(ctx, []string{"2.2.2.2"}); err != nil {
		t.Fatalf("second: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 3*time.Second {
		t.Fatalf("expected batch rate limit wait >= 3s, got %v", elapsed)
	}
}

func TestCollectGeoBatch_EmptyQueueReturnsImmediately(t *testing.T) {
	s := &SourceService{geoQueue: newGeoAddressQueue()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var batch []string
	go func() {
		batch = s.collectGeoBatch(ctx, 100)
		close(done)
	}()

	select {
	case <-done:
		if batch != nil {
			t.Fatalf("expected nil batch on empty queue, got %v", batch)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("collectGeoBatch blocked on empty queue")
	}
}

func TestGeoAddressQueue_EnqueueDedupe(t *testing.T) {
	q := newGeoAddressQueue()
	n1 := q.Enqueue([]string{"1.2.3.4:8080", "5.6.7.8:8080"})
	n2 := q.Enqueue([]string{"1.2.3.4:8080"})
	if n1 != 2 {
		t.Fatalf("first enqueue = %d, want 2", n1)
	}
	if n2 != 0 {
		t.Fatalf("duplicate enqueue = %d, want 0", n2)
	}
	if len(q.ch) != 2 {
		t.Fatalf("channel len = %d, want 2", len(q.ch))
	}
}

func TestGeoIPService_LookupBatchReturnsFailedEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ipAPIResponse{
			{Status: "success", CountryCode: "US", Query: "8.8.8.8"},
			{Status: "fail", Message: "reserved range", Query: "240.0.0.1"},
		})
	}))
	defer srv.Close()

	log := logger.New("error")
	g := newGeoIPServiceForTest(log, testGeoIPConfig(), srv.URL)

	geos, failed, err := g.LookupBatch(context.Background(), []string{"8.8.8.8", "240.0.0.1"})
	if err != nil {
		t.Fatalf("LookupBatch: %v", err)
	}
	if len(geos) != 1 {
		t.Fatalf("success geos = %d, want 1", len(geos))
	}
	if len(failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(failed))
	}
	if failed[0].IP != "240.0.0.1" || failed[0].Message != "reserved range" {
		t.Fatalf("unexpected failed entry: %+v", failed[0])
	}
}

func TestIsPermanentGeoLookupFailure(t *testing.T) {
	permanent := []string{"reserved range", "private range", "invalid query"}
	for _, msg := range permanent {
		if !isPermanentGeoLookupFailure(msg) {
			t.Fatalf("expected permanent failure for %q", msg)
		}
	}
	if isPermanentGeoLookupFailure("timeout") {
		t.Fatal("timeout must not be treated as permanent")
	}
}
