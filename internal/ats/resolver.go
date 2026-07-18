package ats

import (
	"context"
	"regexp"
	"strings"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Resolution is a company name successfully resolved to an ATS board.
type Resolution struct {
	Source   string          // the ATS the board lives on, e.g. "greenhouse"
	Slug     string          // the validated board token/handle
	Listings []model.Listing // the board's open requisitions, fetched while validating
}

// Resolver maps a company name to its ATS board. Companies rarely publish their
// board token, so it generates candidate slugs from the name and probes each
// source until one returns a board — generate-and-verify. The verifying fetch's
// listings are handed back so the caller need not fetch the board again.
type Resolver struct {
	sources []model.Source
}

// NewResolver returns a Resolver that probes sources in order. Wrap each source
// in Cached so the probes here and any later fetch of the same board reuse one
// request per (source, slug).
func NewResolver(sources ...model.Source) *Resolver {
	return &Resolver{sources: sources}
}

// Resolve returns the first candidate slug that yields a board, trying the
// most-likely slug first and, for each, the sources in order. ok is false when
// no candidate resolves on any source (or ctx is cancelled first).
func (r *Resolver) Resolve(ctx context.Context, company string) (Resolution, bool) {
	for _, slug := range CandidateSlugs(company) {
		for _, src := range r.sources {
			if ctx.Err() != nil {
				return Resolution{}, false
			}
			listings, err := src.Fetch(ctx, slug)
			if err != nil {
				continue // not this slug on this source
			}
			return Resolution{Source: src.Name(), Slug: slug, Listings: listings}, true
		}
	}
	return Resolution{}, false
}

var slugSplit = regexp.MustCompile(`[^a-z0-9]+`)

// legalSuffix holds trailing company-name words that ATS slugs almost never
// include, so they are stripped before generating candidates.
var legalSuffix = map[string]struct{}{
	"inc": {}, "incorporated": {}, "llc": {}, "ltd": {}, "limited": {},
	"corp": {}, "corporation": {}, "co": {}, "company": {}, "gmbh": {},
	"plc": {}, "ag": {}, "sa": {}, "nv": {}, "bv": {}, "group": {}, "holdings": {},
}

// CandidateSlugs generates likely ATS slugs for a company name, most-likely
// first: the words joined ("datadog"), hyphenated ("data-dog"), then the first
// word alone ("data"). A leading "the" and trailing legal-entity suffixes
// (Inc, LLC, …) are dropped first, since ATS slugs rarely carry them.
func CandidateSlugs(company string) []string {
	words := strings.Fields(slugSplit.ReplaceAllString(strings.ToLower(strings.TrimSpace(company)), " "))
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	for len(words) > 1 {
		if _, isSuffix := legalSuffix[words[len(words)-1]]; !isSuffix {
			break
		}
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		return nil
	}
	candidates := []string{strings.Join(words, "")}
	if len(words) > 1 {
		candidates = append(candidates, strings.Join(words, "-"), words[0])
	}
	return dedupe(candidates)
}

// dedupe removes empty and duplicate entries, preserving first-seen order.
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
