package indeed

import (
	"encoding/json"
	"testing"
)

// The Indeed actor's field shapes are looser than LinkedIn's, so these fixtures
// deliberately mix the variants the adapter promises to tolerate: a structured
// salary object vs. a plain string vs. null; a flat company string vs. a nested
// companyDetails object; a flat formattedLocation vs. a nested location object;
// and a relative viewJobLink that must be absolutized. A live capture (deferred to
// the first admin smoke run) will later add a real testdata/indeed_sample.json, but
// these lock the mapping contract regardless of which actor build we're on.
func TestNormalizeShapeVariants(t *testing.T) {
	raw := []json.RawMessage{
		// Structured salary, flat company, flat location, external apply, jobTypes.
		json.RawMessage(`{
			"id": "ind-1",
			"title": "Senior Backend Engineer",
			"company": "Acme, Inc.",
			"formattedLocation": "Boston, MA",
			"salary": {"min": 160000, "max": 190000, "type": "yearly"},
			"jobTypes": ["Full-time"],
			"originalApplyUrl": "https://boards.greenhouse.io/acme/jobs/123?src=indeed",
			"viewJobLink": "/viewjob?jk=abc123",
			"jobDescription": "We need 5+ years of Go. Remote-first team.",
			"pubDate": 1750636800000
		}`),
		// String salary (hourly), nested companyDetails, nested location, jobType alt.
		json.RawMessage(`{
			"positionName": "Contract Platform Engineer",
			"companyDetails": {"name": "Globex LLC"},
			"location": {"city": "Austin", "state": "TX", "country": "US"},
			"salary": "$85 - $95 / hr",
			"jobType": ["Contract"],
			"url": "https://www.indeed.com/viewjob?jk=zzz",
			"descriptionHTML": "<p>Kubernetes and Terraform.</p>"
		}`),
		// No salary at all → estimator fills SalaryEst*; blank title is skipped below.
		json.RawMessage(`{"id":"ind-3","title":"Data Engineer","company":"Initech"}`),
		json.RawMessage(`{"id":"ind-4","title":"   "}`), // dropped: blank title
		json.RawMessage(`not json`),                     // dropped: undecodable
	}

	got := Normalize(raw)
	if len(got) != 3 {
		t.Fatalf("normalized %d, want 3 (two unusable rows dropped)", len(got))
	}

	// Row 1 — structured salary + flat fields.
	a := got[0]
	if a.Source != Source {
		t.Errorf("Source = %q, want %q", a.Source, Source)
	}
	if a.JobID != "ind-1" || a.Title != "Senior Backend Engineer" || a.Company != "Acme, Inc." {
		t.Errorf("row1 identity = %q/%q/%q", a.JobID, a.Title, a.Company)
	}
	if a.SalaryMin != 160000 || a.SalaryMax != 190000 {
		t.Errorf("row1 salary = %d-%d, want 160000-190000", a.SalaryMin, a.SalaryMax)
	}
	if a.EmploymentType != "Full-time" {
		t.Errorf("row1 EmploymentType = %q, want Full-time", a.EmploymentType)
	}
	if a.ApplyType != "external" || a.ExternalApplyURL == "" {
		t.Errorf("row1 apply = %q / %q, want external", a.ApplyType, a.ExternalApplyURL)
	}
	if a.URL != "https://www.indeed.com/viewjob?jk=abc123" {
		t.Errorf("row1 URL = %q (viewJobLink not absolutized)", a.URL)
	}
	if !a.Remote {
		t.Errorf("row1 Remote = false, want true (remote-first in description)")
	}
	if a.YearsExperience != 5 {
		t.Errorf("row1 YearsExperience = %d, want 5", a.YearsExperience)
	}
	if a.Posted.Format("2006-01-02") != "2025-06-23" {
		t.Errorf("row1 Posted = %v, want 2025-06-23", a.Posted)
	}

	// Row 2 — hourly string salary annualized (×2080), nested company/location.
	b := got[1]
	if b.Title != "Contract Platform Engineer" || b.Company != "Globex LLC" {
		t.Errorf("row2 identity = %q/%q", b.Title, b.Company)
	}
	if b.Location != "Austin, TX, US" {
		t.Errorf("row2 Location = %q, want Austin, TX, US", b.Location)
	}
	if b.EmploymentType != "Contract" {
		t.Errorf("row2 EmploymentType = %q, want Contract", b.EmploymentType)
	}
	// $85/hr × 2080 = 176,800 ; $95/hr × 2080 = 197,600.
	if b.SalaryMin != 176800 || b.SalaryMax != 197600 {
		t.Errorf("row2 salary = %d-%d, want 176800-197600", b.SalaryMin, b.SalaryMax)
	}
	if b.Description == "" {
		t.Errorf("row2 description empty (HTML fallback not stripped)")
	}

	// Row 3 — no salary → estimate populated instead of the concrete fields.
	c := got[2]
	if c.SalaryMin != 0 || c.SalaryMax != 0 {
		t.Errorf("row3 concrete salary = %d-%d, want 0-0", c.SalaryMin, c.SalaryMax)
	}
}
