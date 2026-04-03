package httpclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentcrate/crated/internal/httpclient"
)

func TestDo_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{MaxRetries: 0})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDo_RetriesOnTransientError(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("temporarily unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{MaxRetries: 3})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after retries, got %d", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 calls, got %d", got)
	}
}

func TestDo_ExhaustsRetries(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{MaxRetries: 2})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}

	// 1 initial + 2 retries = 3 total calls
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 calls, got %d", got)
	}
}

func TestDo_NoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{MaxRetries: 3})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if got := calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call (no retries for 400), got %d", got)
	}
}

func TestDo_RespectsContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so retry backoff is interrupted.
	cancel()

	client := httpclient.New(httpclient.Options{MaxRetries: 5})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestReadBody_WithinLimit(t *testing.T) {
	client := httpclient.New(httpclient.Options{MaxBodySize: 1024})
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("hello world")),
	}
	data, err := client.ReadBody(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}
}

func TestReadBody_ExceedsLimit(t *testing.T) {
	client := httpclient.New(httpclient.Options{MaxBodySize: 5})
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("this is way too long")),
	}
	_, err := client.ReadBody(resp)
	if err == nil {
		t.Fatal("expected error for oversized body")
	}
}

func TestDo_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{
		Timeout:    100 * time.Millisecond,
		MaxRetries: 0,
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestDoStream_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: hello\n\n"))
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	resp, err := client.DoStream(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") {
		t.Errorf("expected body to contain 'hello', got %q", string(body))
	}
}

func TestDoStream_ErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	_, err := client.DoStream(req)
	if err == nil {
		t.Fatal("expected error for non-200 stream response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention 500, got: %v", err)
	}
}

func TestDoStream_ConnectionError(t *testing.T) {
	client := httpclient.New(httpclient.Options{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1", nil)
	_, err := client.DoStream(req)
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
}

func TestNew_DefaultOptions(t *testing.T) {
	client := httpclient.New(httpclient.Options{})
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestDo_NetworkError_RetriesAndFails(t *testing.T) {
	// Use a port that's not listening.
	client := httpclient.New(httpclient.Options{
		Timeout:    500 * time.Millisecond,
		MaxRetries: 1,
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Fatal("expected error for network failure")
	}
	if !strings.Contains(err.Error(), "failed after") {
		t.Errorf("expected 'failed after' in error message, got: %v", err)
	}
}

func TestDo_RetryWithBody(t *testing.T) {
	// Verify that request body is replayed on retries.
	var bodies []string
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if n <= 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{MaxRetries: 2})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL,
		strings.NewReader("hello body"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 calls, got %d", got)
	}
	// Both requests should have received the same body.
	for i, b := range bodies {
		if b != "hello body" {
			t.Errorf("call %d: expected body 'hello body', got %q", i, b)
		}
	}
}

func TestDo_DisableRetriesWithNegativeOne(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer ts.Close()

	// MaxRetries=-1 should disable retries (0 retries after the initial attempt).
	client := httpclient.New(httpclient.Options{MaxRetries: -1})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
	_, err := client.Do(req)
	// With retries disabled and a retryable status, we get an error after 1 attempt.
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 call with retries disabled, got %d", got)
	}
}

func TestDoStream_ConcurrentSafe(t *testing.T) {
	// Verify that concurrent DoStream calls don't race on streamClient init.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: hello\n\n"))
	}))
	defer ts.Close()

	client := httpclient.New(httpclient.Options{})
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
			resp, err := client.DoStream(req)
			if err != nil {
				done <- err
				return
			}
			_ = resp.Body.Close()
			done <- nil
		}()
	}
	for i := 0; i < 10; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent DoStream failed: %v", err)
		}
	}
}
