package judge

import (
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
	raw, err := parseVerdict([]byte(`{"matched":true,"confidence":0.68,"verdict":"likely-real","reasoning":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	v := raw.toModel()
	if v.Confidence != model.LikelyReal {
		t.Errorf("confidence = %q, want likely-real", v.Confidence)
	}
	if v.Score != 0.68 {
		t.Errorf("score = %v, want 0.68", v.Score)
	}
	if v.Reasoning != "ok" {
		t.Errorf("reasoning = %q", v.Reasoning)
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
