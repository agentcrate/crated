// Package ratelimit provides a concurrent-request limiter for LLM models.
//
// Each model gets a semaphore that caps how many requests can be in-flight
// simultaneously. This prevents overloading API providers and helps stay
// within rate limits.
//
// Usage:
//
//	lim := ratelimit.New(10) // max 10 concurrent requests
//	if err := lim.Acquire(ctx); err != nil { ... }
//	defer lim.Release()
package ratelimit

import (
	"context"
	"fmt"
	"sync/atomic"
)

// DefaultMaxConcurrency is the default max concurrent requests per model.
const DefaultMaxConcurrency = 10

// Limiter is a semaphore-based concurrent request limiter.
type Limiter struct {
	sem      chan struct{}
	capacity int
	active   atomic.Int64
	total    atomic.Int64
	dropped  atomic.Int64
}

// New creates a Limiter that allows at most n concurrent requests.
// If n <= 0, DefaultMaxConcurrency is used.
func New(n int) *Limiter {
	if n <= 0 {
		n = DefaultMaxConcurrency
	}
	return &Limiter{
		sem:      make(chan struct{}, n),
		capacity: n,
	}
}

// Acquire blocks until a slot is available or the context is canceled.
// Returns an error if the context is canceled while waiting.
func (l *Limiter) Acquire(ctx context.Context) error {
	select {
	case l.sem <- struct{}{}:
		l.active.Add(1)
		l.total.Add(1)
		return nil
	case <-ctx.Done():
		l.dropped.Add(1)
		return fmt.Errorf("rate limit: %w", ctx.Err())
	}
}

// Release returns a slot to the pool. Must be called after Acquire succeeds.
func (l *Limiter) Release() {
	l.active.Add(-1)
	<-l.sem
}

// Stats returns current limiter statistics.
type Stats struct {
	MaxConcurrency int   `json:"max_concurrency"`
	Active         int64 `json:"active"`
	Total          int64 `json:"total"`
	Dropped        int64 `json:"dropped"`
}

// Stats returns a snapshot of the limiter's statistics.
func (l *Limiter) Stats() Stats {
	return Stats{
		MaxConcurrency: l.capacity,
		Active:         l.active.Load(),
		Total:          l.total.Load(),
		Dropped:        l.dropped.Load(),
	}
}
