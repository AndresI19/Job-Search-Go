package judge

import (
	"context"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Bounded wraps a Judge so at most max evaluations run concurrently. Callers
// beyond the limit block until a slot frees — the wait queue is implicit in the
// semaphore, so no explicit polling is needed. Use it to cap the scarce
// resource a backend consumes (e.g. concurrent `claude` subprocesses).
func Bounded(inner Judge, max int) Judge {
	if max < 1 {
		max = 1
	}
	return &bounded{inner: inner, sem: make(chan struct{}, max)}
}

type bounded struct {
	inner Judge
	sem   chan struct{}
}

func (b *bounded) Evaluate(ctx context.Context, in Input) (model.Verdict, error) {
	select {
	case b.sem <- struct{}{}: // acquire a permit, or queue here
		defer func() { <-b.sem }()
	case <-ctx.Done():
		return model.Verdict{}, ctx.Err()
	}
	return b.inner.Evaluate(ctx, in)
}
