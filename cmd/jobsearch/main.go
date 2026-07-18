// Command jobsearch ingests job listings for the searches in a watch-list,
// verifies each against the company's ATS board and then Claude, scores its
// legitimacy, and writes a ranked CSV.
//
//	APIFY_TOKEN=... jobsearch --watch watch.yaml --out results.csv
//
// The Claude judge is configured via JUDGE_* env (see internal/judge); ingest
// uses the LinkedIn scraper Actor (APIFY_ACTOR_ID overrides the default).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/apify"
	"github.com/AndresI19/Job-Search-Go/internal/ats"
	"github.com/AndresI19/Job-Search-Go/internal/greenhouse"
	"github.com/AndresI19/Job-Search-Go/internal/judge"
	"github.com/AndresI19/Job-Search-Go/internal/lever"
	"github.com/AndresI19/Job-Search-Go/internal/linkedin"
	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/output"
	"github.com/AndresI19/Job-Search-Go/internal/pipeline"
	"github.com/AndresI19/Job-Search-Go/internal/score"
	"github.com/AndresI19/Job-Search-Go/internal/watchlist"
)

// defaultLinkedInActor is the public LinkedIn scraper Actor used when
// APIFY_ACTOR_ID is unset (see cmd/capturefixture and issue #11).
const defaultLinkedInActor = "hKByXkMQaC5Qt9UMN"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	watch := flag.String("watch", "", "path to the watch-list YAML (required)")
	out := flag.String("out", "", "output CSV path (default: stdout)")
	workers := flag.Int("workers", 8, "max listings verified concurrently")
	count := flag.Int("count", 25, "listings to scrape per query (Actor minimum 10)")
	sources := flag.String("sources", "", "comma-separated ATS sources to verify against (overrides the watch-list)")
	minScore := flag.Float64("min-score", 0, "only write listings scoring at least this (0..1)")
	verbose := flag.Bool("verbose", false, "debug-level logging")
	flag.Parse()

	if *watch == "" {
		return fmt.Errorf("--watch is required")
	}
	token := os.Getenv("APIFY_TOKEN")
	if token == "" {
		return fmt.Errorf("APIFY_TOKEN is not set (copy .env.template to .env)")
	}
	actorID := envOr("APIFY_ACTOR_ID", defaultLinkedInActor)

	wl, err := watchlist.Load(*watch)
	if err != nil {
		return err
	}
	jd, err := judge.FromEnv()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	client := apify.New(token)
	resolver := buildResolver(wl, splitCSV(*sources))
	now := time.Now()

	// Ingest each query, normalize, freshness-filter, and dedup across queries
	// (a listing can match more than one search). A query that fails to ingest is
	// logged and skipped so one dead search can't sink the whole run.
	seen := map[string]bool{}
	var listings []model.Listing
	var failed int
	for _, q := range wl.Queries {
		logger.Info("ingesting query", "field", q.Field)
		raw, err := client.Run(ctx, actorID, map[string]any{
			"urls":          []string{q.SearchURL()},
			"count":         *count,
			"scrapeCompany": true,
		})
		if err != nil {
			logger.Error("ingest failed; skipping query", "field", q.Field, "err", err)
			failed++
			continue
		}
		before := len(listings)
		for _, l := range linkedin.Normalize(raw) {
			if !q.Fresh(l, now) || seen[l.JobID] {
				continue
			}
			seen[l.JobID] = true
			listings = append(listings, l)
		}
		logger.Info("ingested query", "field", q.Field, "kept", len(listings)-before)
	}
	if len(wl.Queries) > 0 && failed == len(wl.Queries) {
		return fmt.Errorf("all %d queries failed to ingest", failed)
	}

	logger.Info("verifying", "listings", len(listings), "workers", *workers)
	results := pipeline.Verify(ctx, listings, resolver, jd, score.DefaultWeights(), *workers, logger)
	results = atLeast(results, *minScore)

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	return output.WriteCSV(w, results)
}

// buildResolver constructs the ATS resolver. The source set comes from override
// (the --sources flag) when non-empty, else from the sources named across the
// watch-list, else both. Each source is wrapped in a single-flight cache so
// listings that share a company fetch that board once.
func buildResolver(wl *watchlist.Watchlist, override []string) *ats.Resolver {
	want := map[string]bool{}
	names := override
	if len(names) == 0 {
		for _, q := range wl.Queries {
			names = append(names, q.Sources...)
		}
	}
	for _, s := range names {
		want[strings.ToLower(s)] = true
	}
	if len(want) == 0 {
		want["greenhouse"], want["lever"] = true, true
	}
	var sources []model.Source
	if want["greenhouse"] {
		sources = append(sources, ats.NewCached(greenhouse.New()))
	}
	if want["lever"] {
		sources = append(sources, ats.NewCached(lever.New()))
	}
	return ats.NewResolver(sources...)
}

// atLeast keeps only results scoring at or above min (already sorted best-first).
func atLeast(results []model.Result, min float64) []model.Result {
	if min <= 0 {
		return results
	}
	kept := results[:0]
	for _, r := range results {
		if r.Verdict.Score >= min {
			kept = append(kept, r)
		}
	}
	return kept
}

// splitCSV parses a comma-separated flag into trimmed, non-empty values.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
