package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/ats"
	"github.com/AndresI19/Job-Search-Go/internal/judge"
	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/score"
)

// fakeBoard is a model.Source returning a board for known slugs, 404-like error otherwise.
type fakeBoard struct{ boards map[string][]model.Listing }

func (f fakeBoard) Name() string { return "greenhouse" }

func (f fakeBoard) Fetch(_ context.Context, slug string) ([]model.Listing, error) {
	if b, ok := f.boards[slug]; ok {
		return b, nil
	}
	return nil, errors.New("no board")
}

// fakeJudge returns a fixed score, but errors for a company named "ERR".
type fakeJudge struct{ score float64 }

func (f fakeJudge) Evaluate(_ context.Context, in judge.Input) (model.Verdict, error) {
	if in.Listing.Company == "ERR" {
		return model.Verdict{}, errors.New("judge unavailable")
	}
	return model.Verdict{Score: f.score}, nil
}

func TestVerifyRanksAndCombines(t *testing.T) {
	resolver := ats.NewResolver(fakeBoard{boards: map[string][]model.Listing{
		"acme": {{Title: "Senior Backend Engineer"}},
	}})
	jd := fakeJudge{score: 0.8}

	listings := []model.Listing{
		{Title: "Data Scientist", Company: "Unknownco"},     // no board -> claude only (0.80)
		{Title: "Senior Backend Engineer", Company: "Acme"}, // ATS match + claude (0.86)
	}
	got := Verify(context.Background(), listings, resolver, jd, score.DefaultWeights(), 4)

	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	// Sorted best-first: the ATS-matched Acme role outranks the claude-only one.
	if got[0].Listing.Company != "Acme" {
		t.Fatalf("ranked %q first, want Acme", got[0].Listing.Company)
	}
	if got[0].Verdict.Confidence != model.LikelyReal {
		t.Errorf("Acme confidence = %q, want likely-real", got[0].Verdict.Confidence)
	}
	if got[0].Verdict.VerifiedVia != "greenhouse:acme matched" {
		t.Errorf("VerifiedVia = %q", got[0].Verdict.VerifiedVia)
	}
	if got[1].Verdict.Coverage[0] != "claude" || len(got[1].Verdict.Coverage) != 1 {
		t.Errorf("no-board listing coverage = %v, want [claude]", got[1].Verdict.Coverage)
	}
}

func TestVerifyDegradesWhenBothArmsFail(t *testing.T) {
	resolver := ats.NewResolver(fakeBoard{boards: map[string][]model.Listing{}}) // resolves nothing
	jd := fakeJudge{score: 0.8}

	got := Verify(context.Background(), []model.Listing{
		{Title: "Engineer", Company: "ERR"}, // no board, and the judge errors
	}, resolver, jd, score.DefaultWeights(), 2)

	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	// No ATS coverage and no Claude coverage: an honest uncertain, not a crash.
	if got[0].Verdict.Confidence != model.Uncertain || got[0].Verdict.Coverage != nil {
		t.Errorf("degraded verdict = %+v, want uncertain with no coverage", got[0].Verdict)
	}
}
