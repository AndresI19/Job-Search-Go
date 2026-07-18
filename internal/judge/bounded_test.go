package judge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// countingJudge records peak observed concurrency.
type countingJudge struct {
	cur, peak int32
}

func (c *countingJudge) Evaluate(ctx context.Context, in Input) (model.Verdict, error) {
	n := atomic.AddInt32(&c.cur, 1)
	for {
		p := atomic.LoadInt32(&c.peak)
		if n <= p || atomic.CompareAndSwapInt32(&c.peak, p, n) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	atomic.AddInt32(&c.cur, -1)
	return model.Verdict{Confidence: model.LikelyReal}, nil
}

func TestBoundedLimitsConcurrency(t *testing.T) {
	c := &countingJudge{}
	j := Bounded(c, 3)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = j.Evaluate(context.Background(), Input{})
		}()
	}
	wg.Wait()

	if c.peak > 3 {
		t.Fatalf("peak concurrency = %d, want <= 3", c.peak)
	}
	if c.peak == 0 {
		t.Fatal("judge never ran")
	}
}

// blockingJudge blocks until released, so we can hold the single permit.
type blockingJudge struct{ release chan struct{} }

func (b blockingJudge) Evaluate(ctx context.Context, in Input) (model.Verdict, error) {
	<-b.release
	return model.Verdict{}, nil
}

func TestBoundedRespectsContext(t *testing.T) {
	release := make(chan struct{})
	j := Bounded(blockingJudge{release}, 1)

	// Occupy the only permit.
	go func() { _, _ = j.Evaluate(context.Background(), Input{}) }()
	time.Sleep(10 * time.Millisecond)

	// A cancelled context should return promptly rather than block forever.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := j.Evaluate(ctx, Input{}); err == nil {
		t.Fatal("expected context error while permit is unavailable")
	}
	close(release)
}
