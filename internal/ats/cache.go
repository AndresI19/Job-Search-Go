package ats

import (
	"context"
	"sync"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Cached wraps a model.Source so repeated and concurrent lookups of the same
// company's board reuse one fetch instead of each hitting the API. A whole batch
// of scraped listings from one company would otherwise refetch that company's
// board once per listing.
//
// It is single-flight: when several goroutines request a key that is not yet
// resolved, one performs the fetch and the rest wait on its result. Successful
// results are memoized for the process lifetime (a board does not change mid
// run); errors are not cached, so a transient failure can be retried by a later
// caller rather than being pinned for the rest of the run.
type Cached struct {
	src   model.Source
	mu    sync.Mutex
	calls map[string]*call
}

// call is a single in-flight-or-completed fetch for one key.
type call struct {
	done     chan struct{} // closed when listings/err are set
	listings []model.Listing
	err      error
}

// NewCached returns a Cached wrapping src.
func NewCached(src model.Source) *Cached {
	return &Cached{src: src, calls: make(map[string]*call)}
}

// Compile-time guarantee that a Cached is itself a model.Source, so it composes
// transparently in place of the source it wraps.
var _ model.Source = (*Cached)(nil)

// Name reports the wrapped source's name.
func (c *Cached) Name() string { return c.src.Name() }

// Fetch returns the wrapped source's listings for query, fetching at most once
// per key across concurrent callers and caching successful results.
func (c *Cached) Fetch(ctx context.Context, query string) ([]model.Listing, error) {
	c.mu.Lock()
	if cl, ok := c.calls[query]; ok {
		c.mu.Unlock()
		return cl.wait(ctx)
	}
	cl := &call{done: make(chan struct{})}
	c.calls[query] = cl
	c.mu.Unlock()

	cl.listings, cl.err = c.src.Fetch(ctx, query)
	close(cl.done)

	if cl.err != nil {
		// Don't pin a failure: drop the entry so a later caller can retry.
		c.mu.Lock()
		delete(c.calls, query)
		c.mu.Unlock()
	}
	return cl.listings, cl.err
}

// wait blocks until the leading fetch completes, or until ctx is done — a slow
// leader must not strand a follower whose own deadline has passed.
func (cl *call) wait(ctx context.Context) ([]model.Listing, error) {
	select {
	case <-cl.done:
		return cl.listings, cl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
