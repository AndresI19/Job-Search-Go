package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// These are integration tests: they need a real Postgres. Set TEST_DATABASE_URL to
// run them (a throwaway container is enough); unset — as in CI without a DB service —
// they skip. Each test works in its own schema-reset so runs are independent.
func testDB(t *testing.T) *DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run db integration tests")
	}
	t.Setenv("DATABASE_URL", url)
	d, err := Open(context.Background())
	if err != nil || !d.Enabled() {
		t.Fatalf("open: %v", err)
	}
	// Fresh tables per test.
	if _, err := d.pool.Exec(context.Background(), `DROP TABLE IF EXISTS listings, runs, saved`); err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func listing(url, title string) model.Result {
	return model.Result{
		Listing: model.Listing{URL: url, Title: title, Company: "Acme"},
		Verdict: model.Verdict{Confidence: model.LikelyReal, Score: 0.8},
	}
}

func TestOpenDisabledWithoutURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", "")
	d, err := Open(context.Background())
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if d.Enabled() {
		t.Fatal("expected disabled DB with no DATABASE_URL")
	}
	// All methods must be safe no-ops on the nil/disabled DB.
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate no-op: %v", err)
	}
	if err := d.UpsertResults(context.Background(), "u1", 0, []model.Result{listing("u", "t")}); err != nil {
		t.Fatalf("UpsertResults no-op: %v", err)
	}
	got, err := d.Listings(context.Background(), "u1", Aggregate)
	if err != nil || got != nil {
		t.Fatalf("Listings no-op: %v %v", got, err)
	}
	sl, err := d.SavedListings(context.Background(), "user-123")
	if err != nil || sl != nil {
		t.Fatalf("SavedListings no-op: %v %v", sl, err)
	}
}

func TestUpsertDedupAndViews(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	run1, _ := d.StartRun(ctx, "u1", "software", 2)
	must(t, d.UpsertResults(ctx, "u1", run1, []model.Result{listing("https://a", "A"), listing("https://b", "B")}))

	agg, err := d.Listings(ctx, "u1", Aggregate)
	must(t, err)
	if len(agg) != 2 {
		t.Fatalf("run1 aggregate = %d, want 2", len(agg))
	}

	// Second run re-sees A (updated title) and adds C. Aggregate dedupes to 3, not 4.
	time.Sleep(5 * time.Millisecond)
	run2, _ := d.StartRun(ctx, "u1", "software", 2)
	must(t, d.UpsertResults(ctx, "u1", run2, []model.Result{listing("https://a", "A-updated"), listing("https://c", "C")}))

	agg, err = d.Listings(ctx, "u1", Aggregate)
	must(t, err)
	if len(agg) != 3 {
		t.Fatalf("aggregate after run2 = %d, want 3 (deduped on url)", len(agg))
	}

	// "New" = only what run2 FIRST discovered: C. A was re-seen, not newly added, so
	// it must NOT appear in New even though the latest run touched it.
	fresh, err := d.Listings(ctx, "u1", New)
	must(t, err)
	if len(fresh) != 1 || fresh[0].Listing.URL != "https://c" {
		t.Fatalf("new = %v, want exactly [https://c] (only the newly-added listing)", fresh)
	}

	// The re-seen A still refreshed its stored result in the aggregate (visible there,
	// just not counted as "new").
	all, _ := d.Listings(ctx, "u1", Aggregate)
	var sawUpdated bool
	for _, r := range all {
		if r.Listing.URL == "https://a" && r.Listing.Title == "A-updated" {
			sawUpdated = true
		}
	}
	if !sawUpdated {
		t.Fatal("re-seen listing did not refresh its stored result")
	}
}

func TestMarkUnavailableHidesFromViews(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	run, _ := d.StartRun(ctx, "u1", "software", 2)
	must(t, d.UpsertResults(ctx, "u1", run, []model.Result{listing("https://live", "L"), listing("https://dead", "D")}))

	n, err := d.MarkUnavailable(ctx, "u1", []string{"https://dead"})
	must(t, err)
	if n != 1 {
		t.Fatalf("marked %d, want 1", n)
	}
	agg, _ := d.Listings(ctx, "u1", Aggregate)
	if len(agg) != 1 || agg[0].Listing.URL != "https://live" {
		t.Fatalf("unavailable row still visible: %+v", agg)
	}
	// A re-seen dead URL comes back available (it reappeared in a scan).
	run2, _ := d.StartRun(ctx, "u1", "software", 1)
	must(t, d.UpsertResults(ctx, "u1", run2, []model.Result{listing("https://dead", "D")}))
	agg, _ = d.Listings(ctx, "u1", Aggregate)
	if len(agg) != 2 {
		t.Fatalf("re-seen listing not restored to available: %d", len(agg))
	}
}

func TestCanonicalURL(t *testing.T) {
	base := "https://www.linkedin.com/jobs/view/software-engineer-ii-at-kensho-4403143638"
	cases := map[string]string{
		base: base,
		base + "?position=57&pageNum=0&refId=AAA&trackingId=BBB": base,
		base + "?refId=DIFFERENT&trackingId=OTHER":               base,
		base + "#section": base,
		"not a url":       "not a url",
	}
	for in, want := range cases {
		if got := canonicalURL(in); got != want {
			t.Errorf("canonicalURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// The same posting scraped three times with different LinkedIn tracking params must
// dedup to ONE row — the bug that made one job appear three times in the aggregate.
func TestUpsertDedupsTrackingParams(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	base := "https://www.linkedin.com/jobs/view/swe-ii-at-kensho-4403143638"
	run, _ := d.StartRun(ctx, "u1", "software", 3)
	must(t, d.UpsertResults(ctx, "u1", run, []model.Result{
		listing(base+"?refId=A&trackingId=1", "SWE II"),
		listing(base+"?refId=B&trackingId=2", "SWE II"),
		listing(base+"?position=9&refId=C&trackingId=3", "SWE II"),
	}))
	agg, err := d.Listings(ctx, "u1", Aggregate)
	must(t, err)
	if len(agg) != 1 {
		t.Fatalf("tracking-param variants = %d rows, want 1 (deduped on canonical URL)", len(agg))
	}
	if agg[0].Listing.URL != base {
		t.Fatalf("stored URL = %q, want canonical %q", agg[0].Listing.URL, base)
	}
}

func TestSavedRoundTrip(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	const user = "user-123"

	must(t, d.SetSaved(ctx, user, "https://a", SavedFlags{Pinned: true}))
	must(t, d.SetSaved(ctx, user, "https://b", SavedFlags{Applied: true}))
	got, err := d.Saved(ctx, user)
	must(t, err)
	if !got["https://a"].Pinned || !got["https://b"].Applied {
		t.Fatalf("saved round-trip lost flags: %+v", got)
	}
	// Another user sees nothing — saved is per-identity.
	other, _ := d.Saved(ctx, "someone-else")
	if len(other) != 0 {
		t.Fatalf("saved leaked across identities: %+v", other)
	}
	// Clearing both flags deletes the row.
	must(t, d.SetSaved(ctx, user, "https://a", SavedFlags{}))
	got, _ = d.Saved(ctx, user)
	if _, ok := got["https://a"]; ok {
		t.Fatal("clearing both flags should delete the saved row")
	}
}

// The fix's core guarantee: a saved job the availability sweep retired must still be
// returned by SavedListings (so the Saved/Applied tabs keep it), flagged Available=false
// — unlike Listings, which hides unavailable rows from the aggregate.
func TestSavedListingsSurviveRefreshSweep(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	const user = "user-123"
	run, _ := d.StartRun(ctx, user, "software", 2)
	must(t, d.UpsertResults(ctx, user, run, []model.Result{
		listing("https://live", "Live Job"),
		listing("https://dead", "Dead Job"),
	}))
	// The client saves on the canonical URL — the same key listings dedups on — so the join lines
	// up. Both are pinned: a PINNED saved job is sweepable (only APPLIED/manifested jobs are spared),
	// and the point here is that a swept-but-saved job survives in the Saved tab, flagged unavailable.
	must(t, d.SetSaved(ctx, user, "https://live", SavedFlags{Pinned: true}))
	must(t, d.SetSaved(ctx, user, "https://dead", SavedFlags{Pinned: true}))
	// A pin whose posting was never persisted (saved off the mock preview) has no stored
	// row; it stays a browser-local snapshot and must NOT surface here.
	must(t, d.SetSaved(ctx, user, "https://never-persisted", SavedFlags{Pinned: true}))
	// The Refresh sweep retires the dead posting — it leaves the aggregate…
	if _, err := d.MarkUnavailable(ctx, user, []string{"https://dead"}); err != nil {
		t.Fatal(err)
	}
	if agg, _ := d.Listings(ctx, user, Aggregate); len(agg) != 1 {
		t.Fatalf("aggregate should hide the retired job, got %d rows", len(agg))
	}

	// …but the user's saved view must still hold both saved-and-persisted jobs.
	saved, err := d.SavedListings(ctx, user)
	must(t, err)
	if len(saved) != 2 {
		t.Fatalf("SavedListings = %d rows, want 2 (retired-but-saved job must survive; unpersisted pin excluded)", len(saved))
	}
	byURL := map[string]SavedListing{}
	for _, s := range saved {
		byURL[s.Result.Listing.URL] = s
	}
	if live := byURL["https://live"]; !live.Available || !live.Pinned {
		t.Fatalf("live saved job wrong: %+v", live)
	}
	if dead := byURL["https://dead"]; dead.Available || !dead.Pinned || dead.Result.Listing.Title != "Dead Job" {
		t.Fatalf("retired saved job must return Available=false with intact row data: %+v", dead)
	}
	// Saved is per-identity: another user sees none of this.
	if other, _ := d.SavedListings(ctx, "someone-else"); len(other) != 0 {
		t.Fatalf("SavedListings leaked across identities: %+v", other)
	}
}

// The Applicator's data path: SavedNotApplied narrows to the apply backlog, and
// job_summaries round-trips + upserts by canonical URL.
func TestApplicatorSummariesAndBacklog(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	const user = "user-app"
	run, _ := d.StartRun(ctx, user, "software", 2)
	must(t, d.UpsertResults(ctx, user, run, []model.Result{listing("https://a", "A"), listing("https://b", "B")}))
	must(t, d.SetSaved(ctx, user, "https://a", SavedFlags{Pinned: true}))
	must(t, d.SetSaved(ctx, user, "https://b", SavedFlags{Pinned: true, Applied: true})) // applied ⇒ off the backlog

	na, err := d.SavedNotApplied(ctx, user)
	must(t, err)
	if len(na) != 1 || na[0].Result.Listing.URL != "https://a" {
		t.Fatalf("SavedNotApplied = %+v, want exactly [https://a]", na)
	}

	got, err := d.SummariesFor(ctx, []string{"https://a"})
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("expected no summaries yet, got %v", got)
	}
	must(t, d.UpsertSummary(ctx, "https://a",
		model.JobSummary{Required: "5y Go", Preferred: "K8s", Role: "Build", Company: "SaaS", Employment: "contract", PayNote: "$90/hr"}, "haiku"))
	got, err = d.SummariesFor(ctx, []string{"https://a", "https://b"})
	must(t, err)
	if s := got["https://a"]; s.Employment != "contract" || s.PayNote != "$90/hr" || s.Required != "5y Go" {
		t.Fatalf("summary round-trip: %+v", s)
	}
	if _, ok := got["https://b"]; ok {
		t.Fatal("unsummarized URL should be absent from SummariesFor")
	}
	// Re-upsert overwrites in place.
	must(t, d.UpsertSummary(ctx, "https://a", model.JobSummary{Required: "3y", Employment: "permanent"}, "haiku"))
	got, _ = d.SummariesFor(ctx, []string{"https://a"})
	if s := got["https://a"]; s.Employment != "permanent" || s.Required != "3y" {
		t.Fatalf("upsert did not overwrite: %+v", s)
	}
}

// TestListingsScopedPerUser pins the cache-deception fix's core guarantee: listings are private to
// their owner — one identity's runs never surface for another identity or a guest, and the same URL
// under two owners is two independent rows (dedup is per-owner, not global).
func TestListingsScopedPerUser(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	runA, _ := d.StartRun(ctx, "alice", "software", 1)
	must(t, d.UpsertResults(ctx, "alice", runA, []model.Result{listing("https://a", "Alice job")}))
	runB, _ := d.StartRun(ctx, "bob", "software", 1)
	must(t, d.UpsertResults(ctx, "bob", runB, []model.Result{listing("https://b", "Bob job")}))

	if a, _ := d.Listings(ctx, "alice", Aggregate); len(a) != 1 || a[0].Listing.URL != "https://a" {
		t.Fatalf("alice sees %v, want only her own [https://a]", a)
	}
	if b, _ := d.Listings(ctx, "bob", Aggregate); len(b) != 1 || b[0].Listing.URL != "https://b" {
		t.Fatalf("bob sees %v, want only his own [https://b]", b)
	}
	// A guest (empty identity) sees NEITHER — no real user's runs leak to the tokenless caller.
	if guest, _ := d.Listings(ctx, "", Aggregate); len(guest) != 0 {
		t.Fatalf("guest sees %v, want none (this is the leak the fix closes)", guest)
	}
	// bob re-scanning a URL alice also has must NOT clobber alice's row — the dedup is per-owner.
	must(t, d.UpsertResults(ctx, "bob", runB, []model.Result{listing("https://a", "Bob's copy of A")}))
	if b, _ := d.Listings(ctx, "bob", Aggregate); len(b) != 2 {
		t.Fatalf("bob should have his own 2 rows (b + his copy of a), got %d", len(b))
	}
	if a, _ := d.Listings(ctx, "alice", Aggregate); len(a) != 1 || a[0].Listing.Title != "Alice job" {
		t.Fatalf("alice's row was clobbered by bob's same-url insert: %v", a)
	}
}

// ListingsWithNew flags each row new iff the LATEST run first-discovered it — the one-query form
// the results endpoint uses instead of an Aggregate+New pair.
func TestListingsWithNew(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	run1, _ := d.StartRun(ctx, "u1", "software", 2)
	must(t, d.UpsertResults(ctx, "u1", run1, []model.Result{listing("https://a", "A"), listing("https://b", "B")}))
	run2, _ := d.StartRun(ctx, "u1", "software", 1)
	must(t, d.UpsertResults(ctx, "u1", run2, []model.Result{listing("https://c", "C")})) // c added by the latest run
	_ = run2

	got, err := d.ListingsWithNew(ctx, "u1")
	must(t, err)
	if len(got) != 3 {
		t.Fatalf("want 3 listings, got %d", len(got))
	}
	isNew := map[string]bool{}
	for _, g := range got {
		isNew[g.Result.Listing.URL] = g.New
	}
	if !isNew["https://c"] || isNew["https://a"] || isNew["https://b"] {
		t.Fatalf("only c (latest run's first-discovery) should be new: %v", isNew)
	}
	// A caller with no runs gets an empty set, not an error (COALESCE guards the NULL MAX).
	if none, err := d.ListingsWithNew(ctx, "nobody"); err != nil || len(none) != 0 {
		t.Fatalf("empty owner: %v %v", none, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
