// Command capturefixture runs the LinkedIn ingest Actor once and saves its raw
// dataset to a JSON file, for use as an offline test fixture. Capturing a small
// real sample once lets the rest of the pipeline be built and tested without
// further Apify spend.
//
// Usage:
//
//	APIFY_TOKEN=... go run ./cmd/capturefixture \
//	  -url "https://www.linkedin.com/jobs/search/?keywords=software%20engineer" \
//	  -count 20 -out testdata/linkedin_sample.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/apify"
)

// defaultLinkedInActor is curious_coder/linkedin-jobs-scraper: public search
// URLs, pay-per-result, no account cookies (see issue #11). It is used when
// APIFY_ACTOR_ID is unset. The Actor id is public, not a secret.
const defaultLinkedInActor = "hKByXkMQaC5Qt9UMN"

func main() {
	url := flag.String("url", "", "LinkedIn jobs search URL to scrape (required)")
	count := flag.Int("count", 20, "number of jobs to scrape (Actor minimum is 10)")
	out := flag.String("out", "testdata/linkedin_sample.json", "output fixture path")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "error: -url is required")
		os.Exit(2)
	}
	token := os.Getenv("APIFY_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "error: APIFY_TOKEN is not set (copy .env.template to .env)")
		os.Exit(2)
	}
	actorID := os.Getenv("APIFY_ACTOR_ID")
	if actorID == "" {
		actorID = defaultLinkedInActor
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := apify.New(token)
	input := map[string]any{
		"urls":          []string{*url},
		"count":         *count,
		"scrapeCompany": true,
	}

	fmt.Fprintf(os.Stderr, "running actor %s (count=%d)…\n", actorID, *count)
	items, err := client.Run(ctx, actorID, input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	pretty, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, pretty, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "saved %d items → %s\n", len(items), *out)
}
