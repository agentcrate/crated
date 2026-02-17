package ratelimit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentcrate/crated/internal/ratelimit"
)

func TestLimiter_Basic(t *testing.T) {
	lim := ratelimit.New(2)
	ctx := context.Background()

	if err := lim.Acquire(ctx); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if err := lim.Acquire(ctx); err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}

	stats := lim.Stats()
	if stats.Active != 2 {
		t.Errorf("expected 2 active, got %d", stats.Active)
	}
	if stats.Total != 2 {
		t.Errorf("expected 2 total, got %d", stats.Total)
	}

	lim.Release()
	lim.Release()

	stats = lim.Stats()
	if stats.Active != 0 {
		t.Errorf("expected 0 active after release, got %d", stats.Active)
	}
}

func TestLimiter_BlocksWhenFull(t *testing.T) {
	lim := ratelimit.New(1)
	ctx := context.Background()

	if err := lim.Acquire(ctx); err != nil {
		t.Fatalf("acquire failed: %v", err)
	}

	// Second acquire should block. Use a timeout context.
	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	err := lim.Acquire(shortCtx)
	if err == nil {
		lim.Release()
		t.Fatal("expected error from blocked acquire, got nil")
	}

	stats := lim.Stats()
	if stats.Dropped != 1 {
		t.Errorf("expected 1 dropped, got %d", stats.Dropped)
	}

	lim.Release()
}

func TestLimiter_ConcurrentAccess(t *testing.T) {
	lim := ratelimit.New(5)
	ctx := context.Background()

	var maxSeen atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := lim.Acquire(ctx); err != nil {
				t.Errorf("acquire failed: %v", err)
				return
			}
			defer lim.Release()

			current := lim.Stats().Active
			for {
				old := maxSeen.Load()
				if current <= old || maxSeen.CompareAndSwap(old, current) {
					break
				}
			}

			time.Sleep(10 * time.Millisecond)
		}()
	}

	wg.Wait()

	if max := maxSeen.Load(); max > 5 {
		t.Errorf("max concurrent exceeded limit: saw %d, limit is 5", max)
	}

	stats := lim.Stats()
	if stats.Total != 20 {
		t.Errorf("expected 20 total, got %d", stats.Total)
	}
	if stats.Active != 0 {
		t.Errorf("expected 0 active after all done, got %d", stats.Active)
	}
}

func TestLimiter_DefaultMaxConcurrency(t *testing.T) {
	lim := ratelimit.New(0)
	stats := lim.Stats()
	if stats.MaxConcurrency != ratelimit.DefaultMaxConcurrency {
		t.Errorf("expected default %d, got %d", ratelimit.DefaultMaxConcurrency, stats.MaxConcurrency)
	}
}

func TestLimiter_ContextCancellation(t *testing.T) {
	lim := ratelimit.New(1)
	ctx := context.Background()

	// Fill the limiter.
	if err := lim.Acquire(ctx); err != nil {
		t.Fatalf("acquire failed: %v", err)
	}

	// Cancel before trying to acquire.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()

	err := lim.Acquire(cancelCtx)
	if err == nil {
		lim.Release()
		t.Fatal("expected error from canceled context")
	}

	lim.Release()
}
