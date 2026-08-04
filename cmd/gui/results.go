package main

import (
	"net/http"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/db"
	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/profile"
	"github.com/AndresI19/Job-Search-Go/internal/report"
)

// scryStartupMax bounds the startup-tier headcount band for company tinting, taken
// from the default profile (the aggregate view has no per-request profile).
var scryStartupMax = profile.Default().Highlight.StartupMax

// resultDTO is the typed, presentation-free JSON shape for one verified listing —
// the Jobomancer data contract that replaces report.Preview's {columns, cells, fill}
// spreadsheet payload. The server sends only domain facts; the client owns all
// presentation (tier tinting, pay-provenance styling, role classification).
//
// It is a distinct wire type on purpose: model.Result is persisted as JSONB (its Go
// field names ARE the stored schema), so this DTO carries the stable, lowercase json
// keys the frontend binds to and can evolve independently of storage — the same split
// the applicator endpoint already uses with applyRow.
type resultDTO struct {
	URL          string   `json:"url"`
	ApplyURL     string   `json:"applyUrl,omitempty"`
	Title        string   `json:"title"`
	Company      string   `json:"company"`
	CompanyTier  string   `json:"companyTier,omitempty"` // f500 | software | startup | "" — drives the company-column colour
	Location     string   `json:"location"`
	Remote       bool     `json:"remote"`
	Posted       string   `json:"posted,omitempty"` // RFC3339 UTC; "" when the source gave no date
	Applicants   int      `json:"applicants"`       // -1 when unknown or bucketed
	YearsExp     int      `json:"yearsExperience"`
	SalaryMin    int      `json:"salaryMin"`
	SalaryMax    int      `json:"salaryMax"`
	SalaryEstMin int      `json:"salaryEstMin"`
	SalaryEstMax int      `json:"salaryEstMax"`
	PayState     string   `json:"payState"`   // posted | estimated | none — server-derived provenance
	Score        float64  `json:"score"`      // 0..1 legitimacy
	Confidence   string   `json:"confidence"` // likely-real | uncertain | likely-ghost
	Coverage     []string `json:"coverage"`   // which signals actually ran
	VerifiedVia  string   `json:"verifiedVia,omitempty"`
	Reasoning    string   `json:"reasoning,omitempty"`
	New          bool     `json:"new"` // added by the latest scan — the Scry grid pins these to the top
}

// payStateOf classifies a listing's pay by provenance: a posted salary supersedes an
// estimate, which supersedes nothing. The client never has to guess whether a figure
// is the employer's number or ours.
func payStateOf(l model.Listing) string {
	switch {
	case l.SalaryMin > 0 || l.SalaryMax > 0:
		return "posted"
	case l.SalaryEstMin > 0 || l.SalaryEstMax > 0:
		return "estimated"
	default:
		return "none"
	}
}

// toResultDTO maps a domain Result to the wire DTO. isNew marks a job the latest scan
// added (the always-first "New" group in the Scry grid).
func toResultDTO(r model.Result, isNew bool) resultDTO {
	l, v := r.Listing, r.Verdict
	posted := ""
	if !l.Posted.IsZero() {
		posted = l.Posted.UTC().Format(time.RFC3339)
	}
	return resultDTO{
		URL: l.URL, ApplyURL: l.ExternalApplyURL, Title: l.Title, Company: l.Company,
		CompanyTier: report.CompanyTier(l.Company, l.CompanySize, scryStartupMax, l.Industries),
		Location:    l.Location, Remote: l.Remote, Posted: posted,
		Applicants: l.ApplicantCount, YearsExp: l.YearsExperience,
		SalaryMin: l.SalaryMin, SalaryMax: l.SalaryMax,
		SalaryEstMin: l.SalaryEstMin, SalaryEstMax: l.SalaryEstMax,
		PayState:    payStateOf(l),
		Score:       v.Score,
		Confidence:  string(v.Confidence),
		Coverage:    v.Coverage,
		VerifiedVia: v.VerifiedVia, Reasoning: v.Reasoning, New: isNew,
	}
}

// results serves the persisted aggregate as typed resultDTOs — the domain-model
// contract the Scry grid renders from, instead of the pre-coloured report.Preview
// grid. Each row is flagged `new` when the latest scan added it (New-first grouping).
// Read-only; empty (not an error) without persistence, since every db method is
// nil-safe.
func (s *server) results(w http.ResponseWriter, r *http.Request) {
	all, err := s.db.Listings(r.Context(), db.Aggregate)
	if err != nil {
		httpErr(w, err)
		return
	}
	fresh, err := s.db.Listings(r.Context(), db.New)
	if err != nil {
		httpErr(w, err)
		return
	}
	newURLs := make(map[string]struct{}, len(fresh))
	for _, f := range fresh {
		newURLs[f.Listing.URL] = struct{}{}
	}
	out := make([]resultDTO, len(all))
	for i, res := range all {
		_, isNew := newURLs[res.Listing.URL]
		out[i] = toResultDTO(res, isNew)
	}
	writeJSON(w, map[string]any{"results": out, "total": len(out)})
}
