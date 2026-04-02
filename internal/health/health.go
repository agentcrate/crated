// Package health provides HTTP liveness and readiness probes for crated.
//
// The health server exposes endpoints for container orchestrators:
//
//   - GET /healthz  — Liveness probe. Returns 200 as soon as the server starts.
//     Tells Kubernetes "the process is alive, don't restart me."
//
//   - GET /readyz   — Readiness probe. Returns 200 only after the runtime has
//     finished initializing (models connected, skills loaded, agent built).
//     Tells Kubernetes "I can accept traffic now."
//
//   - GET /metrics  — Basic runtime metrics in JSON format. Includes uptime,
//     readiness state, memory usage, and goroutine count.
//
// Usage:
//
//	hs := health.NewServer(":8080")
//	go hs.ListenAndServe(ctx)
//	// ... initialize runtime ...
//	hs.MarkReady()
//	// ... on shutdown ...
//	hs.Shutdown(ctx)
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"
)

// Server is a lightweight HTTP server that serves health check endpoints.
type Server struct {
	httpServer *http.Server
	ready      atomic.Bool
	startTime  time.Time
	logger     *slog.Logger
}

// NewServer creates a health check server on the given address (e.g., ":8080").
func NewServer(addr string) *Server {
	s := &Server{
		startTime: time.Now(),
		logger:    slog.Default().With("component", "health"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleLiveness)
	mux.HandleFunc("GET /readyz", s.handleReadiness)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return s
}

// ListenAndServe starts the health check server. It blocks until the server
// is shut down or the context is canceled. Safe to call in a goroutine.
func (s *Server) ListenAndServe(ctx context.Context) {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		s.logger.Error("failed to listen", "addr", s.httpServer.Addr, "error", err)
		return
	}
	s.logger.Info("listening", "addr", ln.Addr().String())

	// Shut down when context is canceled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
	}()

	if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		s.logger.Error("server error", "error", err)
	}
}

// Shutdown gracefully stops the health check server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// MarkReady signals that the runtime is fully initialized and ready to
// serve traffic. After this call, /readyz will return 200.
func (s *Server) MarkReady() {
	s.ready.Store(true)
	s.logger.Info("marked ready")
}

// MarkNotReady signals that the runtime is no longer ready (e.g., during
// graceful shutdown). After this call, /readyz will return 503.
func (s *Server) MarkNotReady() {
	s.ready.Store(false)
}

// Handler returns the HTTP handler for testing purposes.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// --- HTTP Handlers ---

type healthResponse struct {
	Status string `json:"status"`
	Uptime string `json:"uptime,omitempty"`
}

func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(healthResponse{
		Status: "alive",
		Uptime: time.Since(s.startTime).Truncate(time.Second).String(),
	})
}

func (s *Server) handleReadiness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !s.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(healthResponse{
			Status: "not ready",
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(healthResponse{
		Status: "ready",
		Uptime: time.Since(s.startTime).Truncate(time.Second).String(),
	})
}

// metricsResponse contains basic runtime metrics.
type metricsResponse struct {
	Uptime      string  `json:"uptime"`
	Ready       bool    `json:"ready"`
	Goroutines  int     `json:"goroutines"`
	HeapAllocMB float64 `json:"heap_alloc_mb"`
	HeapSysMB   float64 `json:"heap_sys_mb"`
	GCCycles    uint32  `json:"gc_cycles"`
}

// NOTE: The health server should be bound to localhost or an internal network.
// Do not expose the health port externally without authentication.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(metricsResponse{
		Uptime:      time.Since(s.startTime).Truncate(time.Second).String(),
		Ready:       s.ready.Load(),
		Goroutines:  runtime.NumGoroutine(),
		HeapAllocMB: float64(mem.HeapAlloc) / (1024 * 1024),
		HeapSysMB:   float64(mem.HeapSys) / (1024 * 1024),
		GCCycles:    mem.NumGC,
	})
}
