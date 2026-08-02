package summarize

import (
	"context"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Bounded wraps a Summarizer so at most max calls run concurrently; callers beyond
// the limit block until a slot frees. Use it to cap the scarce resource a backend
// consumes (concurrent `claude` subprocesses, or API rate).
func Bounded(inner Summarizer, max int) Summarizer {
	if max < 1 {
		max = 1
	}
	return &bounded{inner: inner, sem: make(chan struct{}, max)}
}

type bounded struct {
	inner Summarizer
	sem   chan struct{}
}

func (b *bounded) Summarize(ctx context.Context, l model.Listing) (Summary, error) {
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()
	case <-ctx.Done():
		return Summary{}, ctx.Err()
	}
	return b.inner.Summarize(ctx, l)
}
