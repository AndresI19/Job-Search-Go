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
	"os"
	"os/signal"
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

	client := apify.New(token)
	resolver := buildResolver(wl)
	now := time.Now()

	// Ingest each query, normalize, freshness-filter, and dedup across queries
	// (a listing can match more than one search).
	seen := map[string]bool{}
	var listings []model.Listing
	for _, q := range wl.Queries {
		fmt.Fprintf(os.Stderr, "ingest: %q…\n", q.Field)
		raw, err := client.Run(ctx, actorID, map[string]any{
			"urls":          []string{q.SearchURL()},
			"count":         *count,
			"scrapeCompany": true,
		})
		if err != nil {
			return fmt.Errorf("ingest %q: %w", q.Field, err)
		}
		for _, l := range linkedin.Normalize(raw) {
			if !q.Fresh(l, now) || seen[l.JobID] {
				continue
			}
			seen[l.JobID] = true
			listings = append(listings, l)
		}
	}

	fmt.Fprintf(os.Stderr, "verifying %d listings (%d workers)…\n", len(listings), *workers)
	results := pipeline.Verify(ctx, listings, resolver, jd, score.DefaultWeights(), *workers)

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

// buildResolver constructs the ATS resolver from the sources named across the
// watch-list (both, when none are named), each wrapped in a single-flight cache
// so listings that share a company fetch that board once.
func buildResolver(wl *watchlist.Watchlist) *ats.Resolver {
	want := map[string]bool{}
	for _, q := range wl.Queries {
		for _, s := range q.Sources {
			want[s] = true
		}
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
