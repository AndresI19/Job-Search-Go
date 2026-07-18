package ats

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// countingSource is a test model.Source that records how many times Fetch ran
// and can be told to block until released, to error per query, or to return
// listings per query.
type countingSource struct {
	calls   int64
	release chan struct{} // when non-nil, Fetch blocks until it is closed
	errs    map[string]error
	out     map[string][]model.Listing
}

func (s *countingSource) Name() string { return "counting" }

func (s *countingSource) Fetch(ctx context.Context, query string) ([]model.Listing, error) {
	atomic.AddInt64(&s.calls, 1)
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := s.errs[query]; err != nil {
		return nil, err
	}
	return s.out[query], nil
}

func TestCachedMemoizesPerKey(t *testing.T) {
	src := &countingSource{out: map[string][]model.Listing{
		"acme": {{JobID: "1"}},
		"beta": {{JobID: "2"}},
	}}
	c := NewCached(src)

	for i := 0; i < 3; i++ {
		if got, _ := c.Fetch(context.Background(), "acme"); len(got) != 1 || got[0].JobID != "1" {
			t.Fatalf("acme fetch %d = %v", i, got)
		}
	}
	if _, err := c.Fetch(context.Background(), "beta"); err != nil {
		t.Fatal(err)
	}

	if n := atomic.LoadInt64(&src.calls); n != 2 {
		t.Errorf("underlying Fetch ran %d times, want 2 (one per distinct key)", n)
	}
}

func TestCachedSingleFlight(t *testing.T) {
	src := &countingSource{
		release: make(chan struct{}),
		out:     map[string][]model.Listing{"acme": {{JobID: "1"}}},
	}
	c := NewCached(src)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.Fetch(context.Background(), "acme")
		}()
	}
	// The lock+map guarantees exactly one goroutine inserts the in-flight call
	// and runs Fetch; the rest either park on its done channel or hit the cache.
	// Releasing lets the leader finish regardless of how far the others got.
	close(src.release)
	wg.Wait()

	if n := atomic.LoadInt64(&src.calls); n != 1 {
		t.Errorf("underlying Fetch ran %d times under concurrency, want 1", n)
	}
}

func TestCachedDoesNotCacheErrors(t *testing.T) {
	src := &countingSource{
		errs: map[string]error{"acme": errors.New("boom")},
		out:  map[string][]model.Listing{"acme": {{JobID: "1"}}},
	}
	c := NewCached(src)

	if _, err := c.Fetch(context.Background(), "acme"); err == nil {
		t.Fatal("first call should error")
	}
	// A cached failure would keep erroring; clear it and expect the retry to hit
	// the source again and succeed.
	delete(src.errs, "acme")
	got, err := c.Fetch(context.Background(), "acme")
	if err != nil {
		t.Fatalf("retry after error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("retry got %v", got)
	}
	if n := atomic.LoadInt64(&src.calls); n != 2 {
		t.Errorf("Fetch ran %d times, want 2 (error not cached, so retried)", n)
	}
}
