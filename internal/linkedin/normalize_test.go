package linkedin

import (
	"encoding/json"
	"os"
	"testing"
)

func loadFixture(t *testing.T) []json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("../../testdata/linkedin_sample.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return raw
}

func TestNormalizeFixture(t *testing.T) {
	raw := loadFixture(t)
	got := Normalize(raw)
	if len(got) != len(raw) {
		t.Fatalf("normalized %d, want %d (fixture rows)", len(got), len(raw))
	}

	l := got[0]
	if l.Source != Source {
		t.Errorf("Source = %q, want %q", l.Source, Source)
	}
	if l.JobID != "4432273895" {
		t.Errorf("JobID = %q", l.JobID)
	}
	if l.Title != "Backend Software Engineer" {
		t.Errorf("Title = %q", l.Title)
	}
	if l.Company != "Set of X" {
		t.Errorf("Company = %q", l.Company)
	}
	if l.ApplicantCount != 25 {
		t.Errorf("ApplicantCount = %d, want 25", l.ApplicantCount)
	}
	if l.ApplyType != "easy_apply" {
		t.Errorf("ApplyType = %q, want easy_apply (no applyUrl)", l.ApplyType)
	}
	if l.Posted.Format("2006-01-02") != "2026-06-23" {
		t.Errorf("Posted = %v", l.Posted)
	}
	if l.Description == "" || l.URL == "" {
		t.Errorf("empty description/url: %q / %q", l.Description, l.URL)
	}
}

func TestNormalizeSkipsUnusable(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"id":"1","title":"Engineer","companyName":"Acme"}`),
		json.RawMessage(`not json at all`),
		json.RawMessage(`{"id":"2","title":"   "}`), // blank title
	}
	got := Normalize(raw)
	if len(got) != 1 || got[0].JobID != "1" {
		t.Fatalf("got %d listings %+v, want only the valid one", len(got), got)
	}
}

func TestParseSalary(t *testing.T) {
	cases := []struct {
		in     string
		lo, hi int
	}{
		{"$160,000.00/yr - $220,000.00/yr", 160000, 220000},
		{"$120,000.00/yr - $140,000.00/yr", 120000, 140000},
		{"$75.00/hr", 156000, 156000}, // 75 * 2080
		{"", 0, 0},
		{"Competitive", 0, 0},
	}
	for _, c := range cases {
		if lo, hi := parseSalary(c.in); lo != c.lo || hi != c.hi {
			t.Errorf("parseSalary(%q) = %d,%d, want %d,%d", c.in, lo, hi, c.lo, c.hi)
		}
	}
}

func TestParseYears(t *testing.T) {
	cases := map[string]int{
		"5+ years of experience":     5,
		"3-5 years in backend":       3,
		"minimum of 7 years":         7,
		"no experience level stated": 0,
		"requires 25 years":          0, // out of the plausible 1-20 range
	}
	for in, want := range cases {
		if got := parseYears(in); got != want {
			t.Errorf("parseYears(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseApplicants(t *testing.T) {
	cases := map[string]int{"25": 25, "Over 200 applicants": 200, "": -1, "n/a": -1}
	for in, want := range cases {
		if got := parseApplicants(in); got != want {
			t.Errorf("parseApplicants(%q) = %d, want %d", in, got, want)
		}
	}
}
