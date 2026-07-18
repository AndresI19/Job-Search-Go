package ats

import (
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

func reqs(titles ...string) []model.Listing {
	out := make([]model.Listing, len(titles))
	for i, t := range titles {
		out[i] = model.Listing{Title: t, JobID: t}
	}
	return out
}

func TestMatch(t *testing.T) {
	board := reqs(
		"Senior Software Engineer, Platform",
		"Product Designer",
		"Staff Data Scientist",
	)

	cases := []struct {
		name      string
		listing   string
		wantMatch bool
		wantID    string
	}{
		{"exact ignoring punctuation", "Senior Software Engineer - Platform", true, "Senior Software Engineer, Platform"},
		{"near via shared words", "Software Engineer, Platform", true, "Senior Software Engineer, Platform"},
		{"too weak falls through to the judge", "Sr Software Engineer", false, ""},
		{"unrelated title", "Account Executive", false, ""},
		{"empty title", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Match(model.Listing{Title: tc.listing}, board)
			if ok != tc.wantMatch {
				t.Fatalf("matched = %v, want %v (best = %q)", ok, tc.wantMatch, got.Title)
			}
			if ok && got.JobID != tc.wantID {
				t.Errorf("matched %q, want %q", got.JobID, tc.wantID)
			}
		})
	}
}

func TestMatchEmptyBoard(t *testing.T) {
	if _, ok := Match(model.Listing{Title: "Anything"}, nil); ok {
		t.Error("empty board should never match")
	}
}

func TestStripHTML(t *testing.T) {
	// Entity-encoded HTML, as ATS APIs return it.
	in := "&lt;p&gt;Build &amp;amp; ship.&lt;/p&gt;&lt;ul&gt;&lt;li&gt;Go&lt;/li&gt;&lt;/ul&gt;"
	if got, want := StripHTML(in), "Build & ship. Go"; got != want {
		t.Errorf("StripHTML = %q, want %q", got, want)
	}
}
