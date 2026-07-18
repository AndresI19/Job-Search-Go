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
				Title: "Backend Engineer", Company: "Stripe", Location: "Remote, US",
				Remote: true, Posted: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
				ApplicantCount: 42, SalaryMin: 120000, SalaryMax: 160000,
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
		3:  "true",                         // remote bool
		4:  "2026-06-01",                   // posted normalized to date
		5:  "42",                           // applicants
		6:  "120000",                       // salary_min
		7:  "160000",                       // salary_max
		12: "0.91",                         // score formatted to 2dp
		14: "linkedin-internal;greenhouse", // coverage joined
	} {
		if got[col] != want {
			t.Errorf("row1 col%d = %q, want %q", col, got[col], want)
		}
	}

	// Sparse row: unknown values render empty, not placeholders.
	sparse := rows[2]
	if sparse[4] != "" {
		t.Errorf("sparse posted = %q, want empty", sparse[4])
	}
	if sparse[5] != "" {
		t.Errorf("sparse applicants = %q, want empty", sparse[5])
	}
	if sparse[6] != "" || sparse[7] != "" {
		t.Errorf("sparse salary = %q/%q, want empty", sparse[6], sparse[7])
	}
}
