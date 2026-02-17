package health_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agentcrate/crated/internal/health"
)

func TestLivenessAlwaysReturns200(t *testing.T) {
	s := health.NewServer(":0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.ListenAndServe(ctx)
	time.Sleep(50 * time.Millisecond) // let server start

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if resp["status"] != "alive" {
		t.Errorf("expected status=alive, got %q", resp["status"])
	}
}

func TestReadinessBeforeAndAfterMarkReady(t *testing.T) {
	s := health.NewServer(":0")
	handler := s.Handler()

	// Before MarkReady: should return 503.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before MarkReady: expected 503, got %d", rec.Code)
	}

	// After MarkReady: should return 200.
	s.MarkReady()

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("after MarkReady: expected 200, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if resp["status"] != "ready" {
		t.Errorf("expected status=ready, got %q", resp["status"])
	}
}

func TestMarkNotReadyReturns503(t *testing.T) {
	s := health.NewServer(":0")
	s.MarkReady()
	s.MarkNotReady()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after MarkNotReady: expected 503, got %d", rec.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	s := health.NewServer(":0")
	s.MarkReady()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var metrics map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &metrics); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}

	// Verify expected fields are present.
	requiredFields := []string{"uptime", "ready", "goroutines", "heap_alloc_mb", "heap_sys_mb", "gc_cycles"}
	for _, field := range requiredFields {
		if _, ok := metrics[field]; !ok {
			t.Errorf("missing required field %q in metrics response", field)
		}
	}

	// Verify ready state reflects MarkReady.
	if ready, ok := metrics["ready"].(bool); !ok || !ready {
		t.Errorf("expected ready=true, got %v", metrics["ready"])
	}

	// Verify goroutines is a positive number.
	if goroutines, ok := metrics["goroutines"].(float64); !ok || goroutines < 1 {
		t.Errorf("expected goroutines >= 1, got %v", metrics["goroutines"])
	}
}

func TestMetricsWhenNotReady(t *testing.T) {
	s := health.NewServer(":0")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for metrics even when not ready, got %d", rec.Code)
	}

	var metrics map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &metrics); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}

	if ready, ok := metrics["ready"].(bool); !ok || ready {
		t.Errorf("expected ready=false, got %v", metrics["ready"])
	}
}

func TestListenAndServe_ContextCancel(t *testing.T) {
	s := health.NewServer(":0")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		s.ListenAndServe(ctx)
		close(done)
	}()

	// Give the server a moment to start.
	time.Sleep(50 * time.Millisecond)
	s.MarkReady()

	// Cancel context should trigger shutdown.
	cancel()

	select {
	case <-done:
		// Server shut down cleanly.
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after context cancellation")
	}
}

func TestShutdown(t *testing.T) {
	s := health.NewServer(":0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go s.ListenAndServe(ctx)
	time.Sleep(50 * time.Millisecond)

	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}
}

func TestListenAndServe_InvalidAddress(t *testing.T) {
	// Use an invalid address that net.Listen always rejects.
	s := health.NewServer("not-a-valid-address::::")

	done := make(chan struct{})
	go func() {
		s.ListenAndServe(context.Background())
		close(done)
	}()

	select {
	case <-done:
		// Expected: Listen fails, ListenAndServe returns.
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return after bind failure")
	}
}
