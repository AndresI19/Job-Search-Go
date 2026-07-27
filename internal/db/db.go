// Package db persists verified job-search results in Postgres so real (admin)
// runs accumulate into one deduped aggregate across scans, and so pins/applied
// ("saved") follow a signed-in platform identity across browsers.
//
// It mirrors the quiz's degrade-gracefully contract: with no DATABASE_URL the
// whole package is a no-op (Open returns a nil *DB), and the service behaves
// exactly as it did before persistence — the mock/preview reads the static cache
// and the browser keeps saved state in localStorage. Every method is safe to call
// on a nil *DB.
//
// A result is stored as JSONB keyed by the listing URL (the natural dedup key),
// rather than one column per field: the views need only URL (dedup), last_run_id
// ("New" = the latest scan) and available (Refresh), and JSONB keeps the schema
// stable as the model evolves.
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/secret"
)

type DB struct{ pool *pgxpool.Pool }

// canonicalURL reduces a posting URL to a stable dedup key: scheme + host + path,
// dropping the query and fragment. It exists because LinkedIn appends a fresh
// refId/trackingId (and search position) to every scrape of the same posting, so
// the raw URL made one job persist as several rows. The posting's identity is in
// the path (…-<jobID>), so the path is the right key. Safe here because Listing.URL
// is always the POSTING url — an apply URL whose query is meaningful lives in
// ExternalApplyURL, which is never a dedup key. Unparseable input is returned as-is.
func canonicalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// Open connects using DATABASE_URL (env, else the mounted /etc/.secrets/database-url
// file). It returns (nil, nil) when none is configured — persistence off, not an
// error. A configured-but-unreachable DB is a real error the caller logs.
func Open(ctx context.Context) (*DB, error) {
	url := secret.Value("DATABASE_URL", "DATABASE_URL_FILE", "/etc/.secrets/database-url")
	if url == "" {
		return nil, nil
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &DB{pool: pool}, nil
}

// Enabled reports whether persistence is active. A nil *DB (no DATABASE_URL) is
// disabled, which is why every method guards on it.
func (d *DB) Enabled() bool { return d != nil && d.pool != nil }

func (d *DB) Close() {
	if d.Enabled() {
		d.pool.Close()
	}
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS runs (
  id         BIGSERIAL PRIMARY KEY,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  field      TEXT NOT NULL DEFAULT '',
  count      INT  NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS listings (
  id          BIGSERIAL PRIMARY KEY,
  url         TEXT UNIQUE NOT NULL,   -- dedup key
  result      JSONB NOT NULL,         -- a marshalled model.Result
  first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
  first_run   BIGINT,
  last_run_id BIGINT,
  available   BOOL NOT NULL DEFAULT true
);
-- Saved is keyed by URL, not a listing FK, so a pin survives even if the listing
-- is not (yet) in the aggregate (e.g. pinned off the mock preview).
CREATE TABLE IF NOT EXISTS saved (
  user_id    TEXT NOT NULL,
  url        TEXT NOT NULL,
  pinned     BOOL NOT NULL DEFAULT false,
  applied    BOOL NOT NULL DEFAULT false,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, url)
);`

// Migrate creates the tables if absent. Idempotent; safe to run every boot.
func (d *DB) Migrate(ctx context.Context) error {
	if !d.Enabled() {
		return nil
	}
	_, err := d.pool.Exec(ctx, schemaSQL)
	return err
}

// StartRun records a scan and returns its id, which stamps the rows it touches
// (so "New" can select the latest run's listings).
func (d *DB) StartRun(ctx context.Context, field string, count int) (int64, error) {
	if !d.Enabled() {
		return 0, nil
	}
	var id int64
	err := d.pool.QueryRow(ctx, `INSERT INTO runs(field, count) VALUES ($1, $2) RETURNING id`, field, count).Scan(&id)
	return id, err
}

// UpsertResults writes a run's results, deduping on URL: a new URL is inserted
// (first_seen/first_run set); a seen URL refreshes its result, last_seen and
// last_run_id, and is re-marked available (it came back in this scan). Best-effort
// per the caller — a persistence failure must not fail the run.
func (d *DB) UpsertResults(ctx context.Context, runID int64, results []model.Result) error {
	if !d.Enabled() {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range results {
		// Dedup on the CANONICAL posting URL (path only). LinkedIn tags each scrape of
		// the same posting with a fresh refId/trackingId query, so the raw URL made one
		// job land as several rows. Canonicalise the stored URL too, so the row's own
		// data and the displayed link are the clean posting URL.
		r.Listing.URL = canonicalURL(r.Listing.URL)
		if r.Listing.URL == "" {
			continue // unkeyable — can't dedup
		}
		payload, err := json.Marshal(r)
		if err != nil {
			continue
		}
		batch.Queue(`
			INSERT INTO listings (url, result, first_run, last_run_id, last_seen, available)
			VALUES ($1, $2, $3, $3, now(), true)
			ON CONFLICT (url) DO UPDATE SET
				result = EXCLUDED.result,
				last_run_id = EXCLUDED.last_run_id,
				last_seen = now(),
				available = true`,
			r.Listing.URL, payload, runID)
	}
	if batch.Len() == 0 {
		return nil
	}
	return d.pool.SendBatch(ctx, batch).Close()
}

// View selects which slice of the aggregate to return.
type View string

const (
	Aggregate View = "aggregate" // every available listing, all runs
	New       View = "new"       // only what the latest run added/refreshed
)

// Listings returns the persisted results for a view, newest-seen first, excluding
// soft-deleted (unavailable) rows. Reconstructs model.Result from the stored JSONB.
func (d *DB) Listings(ctx context.Context, view View) ([]model.Result, error) {
	if !d.Enabled() {
		return nil, nil
	}
	q := `SELECT result FROM listings WHERE available`
	if view == New {
		// The latest run only. No runs yet ⇒ MAX is NULL ⇒ no rows, which is correct.
		q += ` AND last_run_id = (SELECT MAX(id) FROM runs)`
	}
	q += ` ORDER BY last_seen DESC`
	rows, err := d.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Result
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r model.Result
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("decode stored result: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Candidate is a listing the availability sweep can re-check: its dedup URL and
// the URL a browser would actually open (external apply link when present).
type Candidate struct{ URL, ApplyURL string }

// AvailabilityCandidates lists currently-available rows for the Refresh sweep.
func (d *DB) AvailabilityCandidates(ctx context.Context) ([]Candidate, error) {
	if !d.Enabled() {
		return nil, nil
	}
	// -> yields the nested JSON object, ->> its field as text; an empty/absent
	// ExternalApplyURL falls back to the canonical URL.
	rows, err := d.pool.Query(ctx, `
		SELECT url, COALESCE(NULLIF(result->'Listing'->>'ExternalApplyURL', ''), url)
		FROM listings WHERE available`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.URL, &c.ApplyURL); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkUnavailable soft-deletes rows by URL (Refresh found them dead). Soft, so a
// Saved reference to the URL still resolves.
func (d *DB) MarkUnavailable(ctx context.Context, urls []string) (int64, error) {
	if !d.Enabled() || len(urls) == 0 {
		return 0, nil
	}
	tag, err := d.pool.Exec(ctx, `UPDATE listings SET available = false WHERE url = ANY($1)`, urls)
	return tag.RowsAffected(), err
}

// SavedFlags is a user's pin/applied state for one URL.
type SavedFlags struct {
	Pinned  bool `json:"pinned"`
	Applied bool `json:"applied"`
}

// Saved returns a user's saved flags keyed by URL.
func (d *DB) Saved(ctx context.Context, userID string) (map[string]SavedFlags, error) {
	if !d.Enabled() || userID == "" {
		return map[string]SavedFlags{}, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT url, pinned, applied FROM saved WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SavedFlags{}
	for rows.Next() {
		var url string
		var f SavedFlags
		if err := rows.Scan(&url, &f.Pinned, &f.Applied); err != nil {
			return nil, err
		}
		out[url] = f
	}
	return out, rows.Err()
}

// SetSaved upserts a user's flags for a URL; a row with both flags false is
// deleted, so unpinning-and-unapplying leaves no residue.
func (d *DB) SetSaved(ctx context.Context, userID, url string, f SavedFlags) error {
	if !d.Enabled() || userID == "" || url == "" {
		return nil
	}
	if !f.Pinned && !f.Applied {
		_, err := d.pool.Exec(ctx, `DELETE FROM saved WHERE user_id = $1 AND url = $2`, userID, url)
		return err
	}
	_, err := d.pool.Exec(ctx, `
		INSERT INTO saved (user_id, url, pinned, applied, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, url) DO UPDATE SET
			pinned = EXCLUDED.pinned, applied = EXCLUDED.applied, updated_at = now()`,
		userID, url, f.Pinned, f.Applied)
	return err
}
