package output

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

func TestWriteCSV(t *testing.T) {
	results := []model.Result{
		{
			Listing: model.Listing{
				Title: "Backend Engineer", Company: "Stripe", CompanySize: 8000,
				Industries: "Software Development", Location: "Remote, US",
				Remote: true, Posted: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
				ApplicantCount: 42, YearsExperience: 5, SalaryMin: 120000, SalaryMax: 160000,
				ApplyType: "external", URL: "https://example.com/1",
				Source: "apify-linkedin",
			},
			Verdict: model.Verdict{
				Confidence: model.LikelyReal, Score: 0.91,
				Coverage:    []string{"linkedin-internal", "greenhouse"},
				VerifiedVia: "greenhouse:stripe matched", Reasoning: "matched open req",
			},
		},
		{
			// Sparse: zero Posted time and unknown applicant count.
			Listing: model.Listing{Title: "Ghosty Role", Company: "Acme", ApplicantCount: -1},
			Verdict: model.Verdict{Confidence: model.LikelyGhost, Score: 0.12},
		},
	}

	var buf bytes.Buffer
	if err := WriteCSV(&buf, results); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	rows, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	if len(rows) != 3 { // header + 2 data rows
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if len(rows[0]) != len(csvHeader) {
		t.Fatalf("header has %d cols, want %d", len(rows[0]), len(csvHeader))
	}

	// Spot-check transformed cells in the first data row.
	got := rows[1]
	for col, want := range map[int]string{
		0:  "Backend Engineer",             // title
		2:  "8000",                         // company_size
		3:  "Software Development",         // industries
		5:  "true",                         // remote bool
		6:  "2026-06-01",                   // posted normalized to date
		7:  "5",                            // years_experience
		8:  "120000",                       // salary_min
		9:  "160000",                       // salary_max
		10: "",                             // salary_est_min empty when a real salary exists
		11: "",                             // salary_est_max empty when a real salary exists
		15: "0.91",                         // score formatted to 2dp
		16: "likely-real",                  // confidence (after score)
		17: "42",                           // applicants (after confidence)
		19: "greenhouse:stripe matched",    // verified_via (after reasoning)
		20: "linkedin-internal;greenhouse", // coverage joined (after reasoning)
	} {
		if got[col] != want {
			t.Errorf("row1 col%d = %q, want %q", col, got[col], want)
		}
	}

	// Sparse row: unknown values render empty, not placeholders.
	sparse := rows[2]
	if sparse[2] != "" {
		t.Errorf("sparse company_size = %q, want empty", sparse[2])
	}
	if sparse[6] != "" {
		t.Errorf("sparse posted = %q, want empty", sparse[6])
	}
	if sparse[7] != "" {
		t.Errorf("sparse years_experience = %q, want empty", sparse[7])
	}
	if sparse[8] != "" || sparse[9] != "" {
		t.Errorf("sparse salary = %q/%q, want empty", sparse[8], sparse[9])
	}
	if sparse[17] != "" {
		t.Errorf("sparse applicants = %q, want empty", sparse[17])
	}
}
