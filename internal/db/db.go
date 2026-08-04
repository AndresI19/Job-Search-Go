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
);
-- Applicator summaries: one apply-ready summary per listing (keyed by canonical
-- URL, not per-user), so a batch launch reuses them and re-launches are incremental.
CREATE TABLE IF NOT EXISTS job_summaries (
  url        TEXT PRIMARY KEY,
  required   TEXT NOT NULL DEFAULT '',
  preferred  TEXT NOT NULL DEFAULT '',
  role       TEXT NOT NULL DEFAULT '',
  company    TEXT NOT NULL DEFAULT '',
  employment TEXT NOT NULL DEFAULT '',
  pay_note   TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
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
		// Genuinely NEW: listings FIRST discovered in the latest run — the jobs this
		// search added to the aggregate, not the ones it merely re-saw. Keys on
		// first_run (set once on insert, never touched on conflict), not last_run_id
		// (which every re-seen listing also carries). No runs yet ⇒ MAX is NULL ⇒ no
		// rows, which is correct.
		q += ` AND first_run = (SELECT MAX(id) FROM runs)`
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
	// Never retire a manifested (applied) job — its listing must stay so the Manifested
	// stage keeps it. Everything else is fair game.
	tag, err := d.pool.Exec(ctx, `
		UPDATE listings SET available = false
		WHERE url = ANY($1) AND url NOT IN (SELECT url FROM saved WHERE applied)`, urls)
	return tag.RowsAffected(), err
}

// MarkAvailable un-retires listings — restoring a job from Trash back into Scry and the
// shortlist.
func (d *DB) MarkAvailable(ctx context.Context, urls []string) (int64, error) {
	if !d.Enabled() || len(urls) == 0 {
		return 0, nil
	}
	tag, err := d.pool.Exec(ctx, `UPDATE listings SET available = true WHERE url = ANY($1)`, urls)
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

// SavedListing is one of a user's saved jobs joined back to its stored row: the
// reconstructed Result, the user's flags, and whether the listing is still
// available. Unlike Listings, this deliberately does NOT filter on `available`
// — a job the Refresh sweep soft-deleted (filled/expired) must still render in
// the user's Saved/Applied tabs, only marked "no longer listed" (Available=false).
type SavedListing struct {
	Result    model.Result
	Pinned    bool
	Applied   bool
	Available bool
}

// SavedListings returns a signed-in user's saved jobs WITH their row data, so the
// Saved/Applied tabs survive a refresh and follow the identity across browsers —
// the client no longer depends on a localStorage snapshot from the run that saved
// them. Joins saved→listings on the canonical URL; pins whose URL was never
// persisted to listings (e.g. saved off the mock preview) have no stored row and
// are skipped here — those remain the browser's own localStorage snapshot.
func (d *DB) SavedListings(ctx context.Context, userID string) ([]SavedListing, error) {
	if !d.Enabled() || userID == "" {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `
		SELECT l.result, s.pinned, s.applied, l.available
		FROM saved s
		JOIN listings l ON l.url = s.url
		WHERE s.user_id = $1 AND (s.pinned OR s.applied)
		ORDER BY l.last_seen DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedListing
	for rows.Next() {
		var raw []byte
		var sl SavedListing
		if err := rows.Scan(&raw, &sl.Pinned, &sl.Applied, &sl.Available); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &sl.Result); err != nil {
			return nil, fmt.Errorf("decode saved result: %w", err)
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

// SavedNotApplied returns the user's saved-but-not-yet-applied listings (pinned
// AND NOT applied) with their row data — the set the Applicator summarizes. Same
// join as SavedListings, narrowed to the apply backlog.
func (d *DB) SavedNotApplied(ctx context.Context, userID string) ([]SavedListing, error) {
	if !d.Enabled() || userID == "" {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `
		SELECT l.result, s.pinned, s.applied, l.available
		FROM saved s
		JOIN listings l ON l.url = s.url
		WHERE s.user_id = $1 AND s.pinned AND NOT s.applied AND l.available
		ORDER BY l.last_seen DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedListing
	for rows.Next() {
		var raw []byte
		var sl SavedListing
		if err := rows.Scan(&raw, &sl.Pinned, &sl.Applied, &sl.Available); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &sl.Result); err != nil {
			return nil, fmt.Errorf("decode saved result: %w", err)
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

// SummariesFor returns the cached Applicator summaries for the given URLs, keyed by
// URL. Missing URLs are simply absent — the caller summarizes those and upserts.
func (d *DB) SummariesFor(ctx context.Context, urls []string) (map[string]model.JobSummary, error) {
	out := map[string]model.JobSummary{}
	if !d.Enabled() || len(urls) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `
		SELECT url, required, preferred, role, company, employment, pay_note
		FROM job_summaries WHERE url = ANY($1)`, urls)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var url string
		var s model.JobSummary
		if err := rows.Scan(&url, &s.Required, &s.Preferred, &s.Role, &s.Company, &s.Employment, &s.PayNote); err != nil {
			return nil, err
		}
		out[url] = s
	}
	return out, rows.Err()
}

// UpsertSummary stores (or refreshes) one listing's Applicator summary, keyed by
// the canonical URL so it is reused across users and future launches.
func (d *DB) UpsertSummary(ctx context.Context, url string, s model.JobSummary, modelID string) error {
	if !d.Enabled() || url == "" {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		INSERT INTO job_summaries (url, required, preferred, role, company, employment, pay_note, model, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())
		ON CONFLICT (url) DO UPDATE SET
			required=EXCLUDED.required, preferred=EXCLUDED.preferred, role=EXCLUDED.role,
			company=EXCLUDED.company, employment=EXCLUDED.employment, pay_note=EXCLUDED.pay_note,
			model=EXCLUDED.model, created_at=now()`,
		url, s.Required, s.Preferred, s.Role, s.Company, s.Employment, s.PayNote, modelID)
	return err
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
