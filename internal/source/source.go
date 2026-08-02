// Package source turns the job boards the scraper supports into a uniform set of
// ingest adapters. Each Source knows its Apify actor, how to build that actor's
// input from a search Query, and how to normalize the actor's dataset into
// model.Listings — so cmd/gui can fan a single search out across every board
// without knowing which is LinkedIn and which is Indeed.
package source

import (
	"encoding/json"
	"os"

	"github.com/AndresI19/Job-Search-Go/internal/indeed"
	"github.com/AndresI19/Job-Search-Go/internal/linkedin"
	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/watchlist"
)

// Per-source Apify actor defaults (overridable by env). LinkedIn keeps the legacy
// APIFY_ACTOR_ID as a fallback so existing deployments don't break.
const (
	defaultLinkedInActor = "hKByXkMQaC5Qt9UMN" // curious_coder/linkedin-jobs-scraper
	defaultIndeedActor   = "qA8rz8tR61HdkfTBL" // curious_coder/indeed-scraper
)

// Source is one job board's ingest adapter.
type Source interface {
	Name() string                                    // e.g. "apify-linkedin"
	ActorID() string                                 // the Apify actor to run
	Input(q watchlist.Query, count int) any          // that actor's run input
	Normalize(raw []json.RawMessage) []model.Listing // its dataset → Listings
}

type linkedinSource struct{ actorID string }

func (linkedinSource) Name() string      { return linkedin.Source }
func (s linkedinSource) ActorID() string { return s.actorID }
func (linkedinSource) Input(q watchlist.Query, count int) any {
	// The LinkedIn actor takes a search URL built from the query's filters.
	return map[string]any{"urls": []string{q.SearchURL()}, "count": count, "scrapeCompany": true}
}
func (linkedinSource) Normalize(raw []json.RawMessage) []model.Listing {
	return linkedin.Normalize(raw)
}

type indeedSource struct{ actorID string }

func (indeedSource) Name() string      { return indeed.Source }
func (s indeedSource) ActorID() string { return s.actorID }
func (indeedSource) Input(q watchlist.Query, count int) any {
	// The Indeed actor takes discrete fields rather than a URL.
	return map[string]any{
		"query": q.Field, "location": q.Location, "country": "us",
		"postedWithinDays": clampDays(q.MaxAgeDays), "count": count,
	}
}
func (indeedSource) Normalize(raw []json.RawMessage) []model.Listing { return indeed.Normalize(raw) }

// All returns every configured scrape source, in a stable order (LinkedIn first).
// A run fans out to all of them; dedup keeps the first-seen copy of a cross-posted
// job, so the order sets which board "wins" a duplicate.
func All() []Source {
	return []Source{
		linkedinSource{actorID: envOr("APIFY_ACTOR_ID_LINKEDIN", envOr("APIFY_ACTOR_ID", defaultLinkedInActor))},
		indeedSource{actorID: envOr("APIFY_ACTOR_ID_INDEED", defaultIndeedActor)},
	}
}

// clampDays maps an arbitrary max-age to Indeed's supported windows (1/3/7/14).
func clampDays(d int) int {
	switch {
	case d <= 0:
		return 14
	case d <= 1:
		return 1
	case d <= 3:
		return 3
	case d <= 7:
		return 7
	default:
		return 14
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
