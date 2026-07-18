package watchlist

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

const sampleYAML = `
defaults:
  location: remote-us
  sources: [greenhouse, lever]
  max_age_days: 21
queries:
  - field: "backend engineer"
    salary_min: 150000
    seniority: [mid, senior]
  - field: "platform engineer"
    location: remote-global
    remote: true
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "watch.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	wl, err := Load(writeTemp(t, sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(wl.Queries) != 2 {
		t.Fatalf("got %d queries, want 2", len(wl.Queries))
	}
	q0 := wl.Queries[0]
	if q0.Location != "remote-us" { // inherited from defaults
		t.Errorf("q0.Location = %q, want inherited remote-us", q0.Location)
	}
	if q0.MaxAgeDays != 21 || len(q0.Sources) != 2 {
		t.Errorf("q0 defaults not inherited: age=%d sources=%v", q0.MaxAgeDays, q0.Sources)
	}
	q1 := wl.Queries[1]
	if q1.Location != "remote-global" { // per-query override wins
		t.Errorf("q1.Location = %q, want override remote-global", q1.Location)
	}
}

func TestLoadRejectsFieldlessQuery(t *testing.T) {
	if _, err := Load(writeTemp(t, "queries:\n  - salary_min: 100000\n")); err == nil {
		t.Fatal("expected error for a query with no field")
	}
}

func TestSearchURLFacets(t *testing.T) {
	q := Query{Field: "backend engineer", Location: "remote-us", SalaryMin: 150000,
		Seniority: []string{"mid", "senior"}, Remote: true, MaxAgeDays: 21}
	u, err := url.Parse(q.SearchURL())
	if err != nil {
		t.Fatalf("bad URL: %v", err)
	}
	qs := u.Query()
	checks := map[string]string{
		"keywords": "backend engineer",
		"location": "remote-us",
		"f_TPR":    "r1814400", // 21 * 86400
		"f_WT":     "2",
		"f_SB2":    "6", // $150k floor -> $140k bucket
		"f_E":      "4", // mid+senior dedup to the mid-senior bucket
	}
	for k, want := range checks {
		if got := qs.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestSalaryBucket(t *testing.T) {
	cases := map[int]string{39000: "", 40000: "1", 150000: "6", 200000: "9", 250000: "9"}
	for in, want := range cases {
		if got := salaryBucket(in); got != want {
			t.Errorf("salaryBucket(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestExperienceCodes(t *testing.T) {
	if got := experienceCodes([]string{"mid", "senior"}); got != "4" {
		t.Errorf("mid+senior = %q, want 4 (deduped)", got)
	}
	if got := experienceCodes([]string{"entry", "director"}); got != "2,5" {
		t.Errorf("entry+director = %q, want 2,5", got)
	}
}

func TestFresh(t *testing.T) {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	q := Query{MaxAgeDays: 21}
	old := model.Listing{Posted: now.AddDate(0, 0, -30)}
	recent := model.Listing{Posted: now.AddDate(0, 0, -5)}
	unknown := model.Listing{} // zero Posted

	if q.Fresh(old, now) {
		t.Error("30-day-old listing should not be fresh under a 21-day window")
	}
	if !q.Fresh(recent, now) {
		t.Error("5-day-old listing should be fresh")
	}
	if !q.Fresh(unknown, now) {
		t.Error("unknown posting date should be kept")
	}
}
