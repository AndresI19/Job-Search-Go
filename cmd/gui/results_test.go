package main

import (
	"testing"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

func TestPayStateOf(t *testing.T) {
	cases := []struct {
		name string
		l    model.Listing
		want string
	}{
		{"posted wins", model.Listing{SalaryMin: 150000, SalaryEstMin: 160000}, "posted"},
		{"posted from max only", model.Listing{SalaryMax: 200000}, "posted"},
		{"estimated when no posted", model.Listing{SalaryEstMin: 160000, SalaryEstMax: 200000}, "estimated"},
		{"none when neither", model.Listing{}, "none"},
	}
	for _, c := range cases {
		if got := payStateOf(c.l); got != c.want {
			t.Errorf("%s: payStateOf = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestToResultDTO(t *testing.T) {
	posted := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	r := model.Result{
		Listing: model.Listing{
			URL: "https://x/1", ExternalApplyURL: "https://apply/1",
			Title: "Backend Engineer", Company: "Stripe", Location: "New York, NY",
			Remote: true, Posted: posted, ApplicantCount: 12, YearsExperience: 5,
			SalaryMin: 190000, SalaryMax: 240000,
		},
		Verdict: model.Verdict{
			Confidence: model.LikelyReal, Score: 0.9,
			Coverage: []string{"greenhouse"}, VerifiedVia: "greenhouse matched", Reasoning: "req matched",
		},
	}

	d := toResultDTO(r, true)

	if d.PayState != "posted" {
		t.Errorf("PayState = %q, want posted", d.PayState)
	}
	if d.Confidence != "likely-real" {
		t.Errorf("Confidence = %q, want likely-real", d.Confidence)
	}
	if !d.New {
		t.Error("New = false, want true")
	}
	if d.ApplyURL != "https://apply/1" {
		t.Errorf("ApplyURL = %q", d.ApplyURL)
	}
	if d.Posted != "2026-08-01T12:00:00Z" {
		t.Errorf("Posted = %q, want RFC3339", d.Posted)
	}
	if d.SalaryMin != 190000 || d.SalaryMax != 240000 {
		t.Errorf("salary = %d..%d", d.SalaryMin, d.SalaryMax)
	}
}

// A zero Posted must serialize as empty, not a spurious epoch date.
func TestToResultDTO_NoPosted(t *testing.T) {
	d := toResultDTO(model.Result{}, false)
	if d.Posted != "" {
		t.Errorf("Posted = %q, want empty for zero time", d.Posted)
	}
	if d.New {
		t.Error("New = true, want false")
	}
}
