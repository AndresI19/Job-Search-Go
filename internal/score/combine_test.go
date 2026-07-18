package score

import (
	"math"
	"reflect"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

func verdict(score float64, reasoning string) *model.Verdict {
	return &model.Verdict{Score: score, Reasoning: reasoning}
}

func TestCombine(t *testing.T) {
	w := DefaultWeights() // ATS 0.6, Claude 0.4

	cases := []struct {
		name         string
		ats          ATSResult
		claude       *model.Verdict
		wantScore    float64
		wantConf     model.Confidence
		wantCoverage []string
		wantVia      string
	}{
		{
			name:         "match plus confident judge is likely-real",
			ats:          ATSResult{Resolved: true, Matched: true, Source: "greenhouse", Slug: "acme"},
			claude:       verdict(0.8, "reads legitimate"),
			wantScore:    0.86, // .6*.9 + .4*.8
			wantConf:     model.LikelyReal,
			wantCoverage: []string{"greenhouse", "claude"},
			wantVia:      "greenhouse:acme matched",
		},
		{
			name:         "board mismatch vs confident judge lands uncertain",
			ats:          ATSResult{Resolved: true, Matched: false, Source: "greenhouse", Slug: "acme"},
			claude:       verdict(0.8, "reads legitimate"),
			wantScore:    0.38, // .6*.1 + .4*.8
			wantConf:     model.Uncertain,
			wantCoverage: []string{"greenhouse", "claude"},
			wantVia:      "claude", // only positive signal above 0.5
		},
		{
			name:         "no board defers to the judge (weight redistributes)",
			ats:          ATSResult{Resolved: false},
			claude:       verdict(0.8, "reads legitimate"),
			wantScore:    0.8,
			wantConf:     model.LikelyReal,
			wantCoverage: []string{"claude"},
			wantVia:      "claude",
		},
		{
			name:         "mismatch plus doubtful judge is likely-ghost",
			ats:          ATSResult{Resolved: true, Matched: false, Source: "lever", Slug: "acme"},
			claude:       verdict(0.2, "thin and generic"),
			wantScore:    0.14, // .6*.1 + .4*.2
			wantConf:     model.LikelyGhost,
			wantCoverage: []string{"lever", "claude"},
			wantVia:      "",
		},
		{
			name:         "ATS-only match redistributes to full ATS weight",
			ats:          ATSResult{Resolved: true, Matched: true, Source: "greenhouse", Slug: "acme"},
			claude:       nil,
			wantScore:    0.9,
			wantConf:     model.LikelyReal,
			wantCoverage: []string{"greenhouse"},
			wantVia:      "greenhouse:acme matched",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Combine(w, tc.ats, tc.claude)
			if math.Abs(got.Score-tc.wantScore) > 1e-9 {
				t.Errorf("Score = %v, want %v", got.Score, tc.wantScore)
			}
			if got.Confidence != tc.wantConf {
				t.Errorf("Confidence = %q, want %q", got.Confidence, tc.wantConf)
			}
			if !reflect.DeepEqual(got.Coverage, tc.wantCoverage) {
				t.Errorf("Coverage = %v, want %v", got.Coverage, tc.wantCoverage)
			}
			if got.VerifiedVia != tc.wantVia {
				t.Errorf("VerifiedVia = %q, want %q", got.VerifiedVia, tc.wantVia)
			}
		})
	}
}

func TestCombineNoSignals(t *testing.T) {
	got := Combine(DefaultWeights(), ATSResult{Resolved: false}, nil)
	if got.Confidence != model.Uncertain {
		t.Errorf("Confidence = %q, want Uncertain", got.Confidence)
	}
	if got.Score != 0 || got.Coverage != nil {
		t.Errorf("empty coverage expected, got score=%v coverage=%v", got.Score, got.Coverage)
	}
}
