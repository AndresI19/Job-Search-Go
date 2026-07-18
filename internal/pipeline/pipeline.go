// Package pipeline runs the verification chain over ingested listings. For each
// listing it resolves the company's ATS board, judges the listing against those
// requisitions with Claude, and blends the two into a scored Result. The chain
// is sequential per listing — ATS first, since its requisitions are the judge's
// candidate set — and the pipeline fans it out across listings with a bounded
// worker pool, the program's hardest-worked concurrency.
package pipeline

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/AndresI19/Job-Search-Go/internal/ats"
	"github.com/AndresI19/Job-Search-Go/internal/judge"
	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/score"
)

// Verify runs every listing through ATS → Claude → combine and returns the
// scored Results sorted best-first (highest legitimacy score). workers bounds how
// many listings are in flight at once; a value below 1 is treated as 1.
func Verify(ctx context.Context, listings []model.Listing, resolver *ats.Resolver, jd judge.Judge, w score.Weights, workers int, log *slog.Logger) []model.Result {
	if workers < 1 {
		workers = 1
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	results := make([]model.Result, len(listings))
	sem := make(chan struct{}, workers) // bounds concurrency; the only ceilings are external rate limits
	var wg sync.WaitGroup
	for i, l := range listings {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, l model.Listing) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = verifyOne(ctx, l, resolver, jd, w, log) // each goroutine owns results[i]; no lock needed
		}(i, l)
	}
	wg.Wait()

	sort.SliceStable(results, func(a, b int) bool {
		return results[a].Verdict.Score > results[b].Verdict.Score
	})
	return results
}

// verifyOne runs the sequential chain for a single listing.
func verifyOne(ctx context.Context, l model.Listing, resolver *ats.Resolver, jd judge.Judge, w score.Weights, log *slog.Logger) model.Result {
	// 1 · ATS: resolve the company's board and look for a matching requisition.
	var atsRes score.ATSResult
	var candidates []model.Listing
	atsChecked := false
	if res, ok := resolver.Resolve(ctx, l.Company); ok {
		atsChecked = true
		atsRes = score.ATSResult{Resolved: true, Source: res.Source, Slug: res.Slug}
		if _, matched := ats.Match(l, res.Listings); matched {
			atsRes.Matched = true
		}
		candidates = res.Listings
		log.Debug("ats resolved", "company", l.Company, "source", res.Source, "slug", res.Slug, "matched", atsRes.Matched)
	} else {
		log.Debug("no ats board found", "company", l.Company)
	}

	// 2 · Claude: judge the listing against the ATS candidates. ATSChecked lets the
	// prompt distinguish "checked, no match" (ghost signal) from "board not found"
	// (a coverage gap, not evidence). A judge error is degraded to no Claude
	// coverage rather than failing the whole listing.
	var verdict *model.Verdict
	if v, err := jd.Evaluate(ctx, judge.Input{Listing: l, Candidates: candidates, ATSChecked: atsChecked}); err == nil {
		verdict = &v
	} else {
		log.Warn("judge failed; scoring on ATS signal only", "company", l.Company, "title", l.Title, "err", err)
	}

	// 3 · Combine into the final coverage-aware verdict.
	return model.Result{Listing: l, Verdict: score.Combine(w, atsRes, verdict)}
}
