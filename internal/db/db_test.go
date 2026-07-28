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
	if err := d.UpsertResults(context.Background(), 0, []model.Result{listing("u", "t")}); err != nil {
		t.Fatalf("UpsertResults no-op: %v", err)
	}
	got, err := d.Listings(context.Background(), Aggregate)
	if err != nil || got != nil {
		t.Fatalf("Listings no-op: %v %v", got, err)
	}
}

func TestUpsertDedupAndViews(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	run1, _ := d.StartRun(ctx, "software", 2)
	must(t, d.UpsertResults(ctx, run1, []model.Result{listing("https://a", "A"), listing("https://b", "B")}))

	agg, err := d.Listings(ctx, Aggregate)
	must(t, err)
	if len(agg) != 2 {
		t.Fatalf("run1 aggregate = %d, want 2", len(agg))
	}

	// Second run re-sees A (updated title) and adds C. Aggregate dedupes to 3, not 4.
	time.Sleep(5 * time.Millisecond)
	run2, _ := d.StartRun(ctx, "software", 2)
	must(t, d.UpsertResults(ctx, run2, []model.Result{listing("https://a", "A-updated"), listing("https://c", "C")}))

	agg, err = d.Listings(ctx, Aggregate)
	must(t, err)
	if len(agg) != 3 {
		t.Fatalf("aggregate after run2 = %d, want 3 (deduped on url)", len(agg))
	}

	// "New" = only what run2 FIRST discovered: C. A was re-seen, not newly added, so
	// it must NOT appear in New even though the latest run touched it.
	fresh, err := d.Listings(ctx, New)
	must(t, err)
	if len(fresh) != 1 || fresh[0].Listing.URL != "https://c" {
		t.Fatalf("new = %v, want exactly [https://c] (only the newly-added listing)", fresh)
	}

	// The re-seen A still refreshed its stored result in the aggregate (visible there,
	// just not counted as "new").
	all, _ := d.Listings(ctx, Aggregate)
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
	run, _ := d.StartRun(ctx, "software", 2)
	must(t, d.UpsertResults(ctx, run, []model.Result{listing("https://live", "L"), listing("https://dead", "D")}))

	n, err := d.MarkUnavailable(ctx, []string{"https://dead"})
	must(t, err)
	if n != 1 {
		t.Fatalf("marked %d, want 1", n)
	}
	agg, _ := d.Listings(ctx, Aggregate)
	if len(agg) != 1 || agg[0].Listing.URL != "https://live" {
		t.Fatalf("unavailable row still visible: %+v", agg)
	}
	// A re-seen dead URL comes back available (it reappeared in a scan).
	run2, _ := d.StartRun(ctx, "software", 1)
	must(t, d.UpsertResults(ctx, run2, []model.Result{listing("https://dead", "D")}))
	agg, _ = d.Listings(ctx, Aggregate)
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
	run, _ := d.StartRun(ctx, "software", 3)
	must(t, d.UpsertResults(ctx, run, []model.Result{
		listing(base+"?refId=A&trackingId=1", "SWE II"),
		listing(base+"?refId=B&trackingId=2", "SWE II"),
		listing(base+"?position=9&refId=C&trackingId=3", "SWE II"),
	}))
	agg, err := d.Listings(ctx, Aggregate)
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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
