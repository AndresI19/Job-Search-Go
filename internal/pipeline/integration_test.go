package pipeline_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/ats"
	"github.com/AndresI19/Job-Search-Go/internal/greenhouse"
	"github.com/AndresI19/Job-Search-Go/internal/judge"
	"github.com/AndresI19/Job-Search-Go/internal/lever"
	"github.com/AndresI19/Job-Search-Go/internal/linkedin"
	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/pipeline"
	"github.com/AndresI19/Job-Search-Go/internal/score"
)

// cannedJudge is a deterministic Claude stand-in: a fixed score, never errors.
type cannedJudge struct{ score float64 }

func (c cannedJudge) Evaluate(_ context.Context, _ judge.Input) (model.Verdict, error) {
	return model.Verdict{Score: c.score}, nil
}

// greenhouseServing answers with a one-requisition board for wantSlug and 404s
// everything else, over the real greenhouse client.
func greenhouseServing(t *testing.T, wantSlug, reqTitle string) *greenhouse.Client {
	t.Helper()
	board := `{"jobs":[{"id":1,"title":"` + reqTitle + `","absolute_url":"https://x/1","updated_at":"2026-06-20T00:00:00Z","location":{"name":"Remote"},"content":"role"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boards/"+wantSlug+"/jobs" {
			_, _ = w.Write([]byte(board))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return greenhouse.New(greenhouse.WithBaseURL(srv.URL), greenhouse.WithHTTPClient(srv.Client()))
}

// leverServing404 answers 404 for every handle, over the real lever client.
func leverServing404(t *testing.T) *lever.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return lever.New(lever.WithBaseURL(srv.URL), lever.WithHTTPClient(srv.Client()))
}

func TestVerifyAgainstFixtureNoNetwork(t *testing.T) {
	b, err := os.ReadFile("../../testdata/linkedin_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	listings := linkedin.Normalize(raw)
	if len(listings) == 0 {
		t.Fatal("no listings from fixture")
	}

	// Canned ATS: Greenhouse has a matching req for "Set of X" (slug setofx);
	// Lever has nothing. Canned judge: a fixed score.
	resolver := ats.NewResolver(
		ats.NewCached(greenhouseServing(t, "setofx", "Backend Software Engineer")),
		ats.NewCached(leverServing404(t)),
	)
	jd := cannedJudge{score: 0.7}

	// Determinism: two runs at different concurrency give identical verdicts.
	r1 := pipeline.Verify(context.Background(), listings, resolver, jd, score.DefaultWeights(), 8, nil)
	r2 := pipeline.Verify(context.Background(), listings, resolver, jd, score.DefaultWeights(), 1, nil)
	if len(r1) != len(listings) || len(r2) != len(listings) {
		t.Fatalf("result count = %d/%d, want %d", len(r1), len(r2), len(listings))
	}
	for i := range r1 {
		if r1[i].Listing.JobID != r2[i].Listing.JobID || r1[i].Verdict.Score != r2[i].Verdict.Score {
			t.Fatalf("nondeterministic at %d", i)
		}
	}

	// The ATS-matched listing is verified via Greenhouse and carries both arms.
	matched := findByID(r1, "4432273895")
	if matched == nil {
		t.Fatal("fixture listing 4432273895 (Set of X) missing")
	}
	if matched.Verdict.VerifiedVia != "greenhouse:setofx matched" {
		t.Errorf("VerifiedVia = %q, want greenhouse:setofx matched", matched.Verdict.VerifiedVia)
	}
	if !has(matched.Verdict.Coverage, "greenhouse") || !has(matched.Verdict.Coverage, "claude") {
		t.Errorf("coverage = %v, want both arms", matched.Verdict.Coverage)
	}

	// Every listing has Claude coverage; the no-board ones have only that.
	claudeOnly := 0
	for i := range r1 {
		if !has(r1[i].Verdict.Coverage, "claude") {
			t.Errorf("%s missing claude coverage: %v", r1[i].Listing.JobID, r1[i].Verdict.Coverage)
		}
		if len(r1[i].Verdict.Coverage) == 1 {
			claudeOnly++
		}
	}
	if claudeOnly == 0 {
		t.Error("expected some listings to have no ATS board (claude-only coverage)")
	}
}

func findByID(rs []model.Result, id string) *model.Result {
	for i := range rs {
		if rs[i].Listing.JobID == id {
			return &rs[i]
		}
	}
	return nil
}

func has(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
