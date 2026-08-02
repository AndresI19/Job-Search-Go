package source

import (
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// A job cross-posted to both boards must collapse to one row, while genuinely
// distinct roles survive — the two ways Dedup can identify a duplicate are the
// canonical apply URL and the fuzzy company+title+location key, so both are exercised.
func TestDedupCrossPost(t *testing.T) {
	in := []model.Listing{
		// LinkedIn copy — kept (first-seen).
		{
			Source: "apify-linkedin", Title: "Backend Engineer", Company: "Acme, Inc.",
			Location:         "Boston, MA",
			ExternalApplyURL: "https://boards.greenhouse.io/acme/jobs/42?src=linkedin",
			URL:              "https://linkedin.com/jobs/view/1",
		},
		// Indeed copy of the SAME job — same apply URL modulo tracking query. Dropped.
		{
			Source: "apify-indeed", Title: "Backend Engineer", Company: "Acme Inc",
			Location:         "Boston, MA, US",
			ExternalApplyURL: "https://boards.greenhouse.io/acme/jobs/42?src=indeed&utm=x",
			URL:              "https://indeed.com/viewjob?jk=xyz",
		},
		// Different board, no apply URL, but same company/title/location → fuzzy dupe. Dropped.
		{
			Source: "apify-indeed", Title: "Backend Engineer", Company: "ACME, LLC",
			Location: "Boston, MA",
			URL:      "https://indeed.com/viewjob?jk=dup2",
		},
		// A genuinely different role at the same company — must survive.
		{
			Source: "apify-indeed", Title: "Frontend Engineer", Company: "Acme, Inc.",
			Location: "Boston, MA",
			URL:      "https://indeed.com/viewjob?jk=fe",
		},
		// Same title/company but a different city — different job, survives.
		{
			Source: "apify-linkedin", Title: "Backend Engineer", Company: "Acme, Inc.",
			Location: "Austin, TX",
			URL:      "https://linkedin.com/jobs/view/2",
		},
	}

	out, dropped := Dedup(in)
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if len(out) != 3 {
		t.Fatalf("kept %d, want 3", len(out))
	}
	// First-seen wins: the surviving Boston backend row is the LinkedIn copy.
	if out[0].Source != "apify-linkedin" || out[0].URL != "https://linkedin.com/jobs/view/1" {
		t.Errorf("kept copy = %q %q, want the LinkedIn original", out[0].Source, out[0].URL)
	}
	// The distinct roles are both present.
	var haveFE, haveAustin bool
	for _, l := range out {
		if l.Title == "Frontend Engineer" {
			haveFE = true
		}
		if l.Location == "Austin, TX" {
			haveAustin = true
		}
	}
	if !haveFE || !haveAustin {
		t.Errorf("distinct roles missing: frontend=%v austin=%v", haveFE, haveAustin)
	}
}

// An empty apply URL and an empty fuzzy key (missing company/title) must never
// collapse unrelated rows — a listing with no identifiers is always kept.
func TestDedupKeepsUnidentifiable(t *testing.T) {
	in := []model.Listing{
		{Source: "apify-indeed", URL: "a"},                  // no company/title/apply
		{Source: "apify-indeed", URL: "b"},                  // ditto — must NOT merge with the first
		{Source: "apify-indeed", Title: "Eng", URL: "c"},    // no company → empty fuzzy key
		{Source: "apify-indeed", Company: "Acme", URL: "d"}, // no title → empty fuzzy key
	}
	out, dropped := Dedup(in)
	if dropped != 0 || len(out) != 4 {
		t.Fatalf("dropped=%d kept=%d, want 0 dropped / 4 kept", dropped, len(out))
	}
}
