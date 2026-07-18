// Package model defines the core domain types shared across ingest,
// verification, and output. Everything in the pipeline speaks in terms of a
// normalized Listing and the Verdict produced by verifying it, so no component
// is coupled to a source-specific payload format.
package model

import (
	"context"
	"time"
)

// Listing is a single normalized job posting, independent of the source it came
// from. Ingest adapters map their raw payloads into this shape so the rest of
// the pipeline never sees source-specific formats.
type Listing struct {
	Source           string // origin, e.g. "apify-linkedin" or "greenhouse"
	JobID            string // source-native identifier
	Title            string
	Company          string
	CompanyURL       string // canonical company handle — the verification join key
	Location         string
	Remote           bool
	Posted           time.Time // zero value when unknown
	ApplicantCount   int       // -1 when unknown or bucketed ("over 200")
	YearsExperience  int       // minimum years the posting asks for; 0 when not stated
	SalaryMin        int       // annual USD; 0 when the source gives no salary
	SalaryMax        int       // annual USD; 0 when the source gives no salary
	ApplyType        string    // "easy_apply", "external", or "" when unknown
	ExternalApplyURL string    // set when ApplyType == "external"
	URL              string    // canonical posting URL
	Description      string    // plain text, HTML stripped
}

// Confidence is a coarse legitimacy bucket for a verified Listing.
type Confidence string

const (
	LikelyReal  Confidence = "likely-real"
	Uncertain   Confidence = "uncertain"
	LikelyGhost Confidence = "likely-ghost"
)

// Verdict is the outcome of verifying a Listing's legitimacy. Coverage records
// which signals actually ran, so an Uncertain verdict from thin coverage stays
// distinguishable from a genuine LikelyGhost.
type Verdict struct {
	Confidence  Confidence // overall judgment
	Score       float64    // 0..1 legitimacy score
	Coverage    []string   // signals/tiers evaluated, e.g. ["linkedin-internal", "greenhouse"]
	VerifiedVia string     // strongest positive signal, e.g. "greenhouse:stripe matched"
	Reasoning   string     // human-readable explanation
}

// Result pairs a Listing with the Verdict produced by verifying it. It is the
// unit the output stage renders.
type Result struct {
	Listing Listing
	Verdict Verdict
}

// Source is anything that produces Listings for a query. Ingest adapters (e.g.
// Apify) implement it to pull candidate jobs; ATS adapters (Greenhouse, Lever)
// implement it to return a company's open requisitions for cross-referencing.
// The verification pipeline fans out across Sources concurrently behind this
// single interface.
type Source interface {
	// Name identifies the source in logs and Verdict fields.
	Name() string

	// Fetch returns listings matching query. Implementations must honor ctx
	// cancellation and deadlines, and should return whatever partial result
	// they have alongside an error rather than blocking indefinitely.
	Fetch(ctx context.Context, query string) ([]Listing, error)
}
