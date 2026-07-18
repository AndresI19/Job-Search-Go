package judge

import (
	"math"
	"strings"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

func TestBuildPromptIncludesListingAndCandidates(t *testing.T) {
	in := Input{
		Listing: model.Listing{Title: "Backend Engineer", Company: "Stripe", Location: "Remote", ApplicantCount: 25, ApplyType: "external"},
		Candidates: []model.Listing{
			{Title: "Backend Engineer (Payments)", Location: "US"},
		},
	}
	p := buildPrompt(in)
	for _, want := range []string{"Backend Engineer", "Stripe", "25", "external", "Backend Engineer (Payments)"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildPromptNoCandidates(t *testing.T) {
	p := buildPrompt(Input{Listing: model.Listing{Title: "X"}})
	if !strings.Contains(p, "No matching requisition") {
		t.Errorf("expected no-candidates note, got:\n%s", p)
	}
}

func TestParseVerdictToModel(t *testing.T) {
	// Score is derived from the verdict (direction) and confidence (distance from
	// the 0.5 midpoint) — the number must agree with the words.
	cases := []struct {
		json      string
		wantConf  model.Confidence
		wantScore float64
	}{
		{`{"matched":true,"verdict":"likely-real","confidence":0.68,"reasoning":"ok"}`, model.LikelyReal, 0.84},
		// The regression the live run exposed: a confident ghost must score LOW.
		{`{"matched":false,"verdict":"likely-ghost","confidence":0.85,"reasoning":"stale"}`, model.LikelyGhost, 0.075},
		{`{"matched":false,"verdict":"uncertain","confidence":0.9,"reasoning":"unclear"}`, model.Uncertain, 0.5},
		{`{"matched":true,"verdict":"likely-real","confidence":1.5,"reasoning":"sure"}`, model.LikelyReal, 1.0}, // clamped
	}
	for _, tc := range cases {
		raw, err := parseVerdict([]byte(tc.json))
		if err != nil {
			t.Fatalf("parseVerdict(%s): %v", tc.json, err)
		}
		v := raw.toModel()
		if v.Confidence != tc.wantConf {
			t.Errorf("%s: Confidence = %q, want %q", tc.json, v.Confidence, tc.wantConf)
		}
		if math.Abs(v.Score-tc.wantScore) > 1e-9 {
			t.Errorf("%s: Score = %v, want %v", tc.json, v.Score, tc.wantScore)
		}
	}
}

func TestExtractJSONObject(t *testing.T) {
	got := extractJSONObject("here is the result: {\"matched\":true} thanks")
	if got != `{"matched":true}` {
		t.Errorf("extractJSONObject = %q", got)
	}
}

func TestFromEnvUnknownBackend(t *testing.T) {
	t.Setenv("JUDGE_BACKEND", "bogus")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestFromEnvCLIDefault(t *testing.T) {
	t.Setenv("JUDGE_BACKEND", "")
	j, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if j == nil {
		t.Fatal("expected a judge")
	}
}
