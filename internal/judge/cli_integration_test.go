//go:build integration

package judge

import (
	"context"
	"testing"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// TestCLIJudgeLive drives the real `claude` CLI end to end. It is excluded from
// the default suite (and CI) by the integration build tag, since it spends
// subscription usage. Run it manually:
//
//	go test -tags=integration ./internal/judge -run TestCLIJudgeLive -v
func TestCLIJudgeLive(t *testing.T) {
	j := NewCLIJudge("claude-haiku-4-5")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	v, err := j.Evaluate(ctx, Input{
		Listing: model.Listing{
			Title: "Senior Backend Engineer", Company: "Stripe",
			Location: "Remote, US", ApplicantCount: 25, ApplyType: "external",
		},
		Candidates: []model.Listing{{Title: "Backend Engineer, Payments", Location: "US"}},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	switch v.Confidence {
	case model.LikelyReal, model.Uncertain, model.LikelyGhost:
		// ok
	default:
		t.Fatalf("unexpected verdict confidence %q (full: %+v)", v.Confidence, v)
	}
	t.Logf("verdict=%s score=%.2f reasoning=%s", v.Confidence, v.Score, v.Reasoning)
}
