package filter

import (
	"testing"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/profile"
)

// header mirrors the columns filter.Apply reads (a subset of output.Header).
var header = []string{"title", "location", "remote", "posted", "salary_max", "salary_est_max", "score", "confidence"}

// row builds a row in header order.
func row(title, loc, remote, posted, salMax, estMax, score, conf string) []string {
	return []string{title, loc, remote, posted, salMax, estMax, score, conf}
}

func TestApply(t *testing.T) {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	recent := now.AddDate(0, 0, -10).Format("2006-01-02")
	old := now.AddDate(0, 0, -200).Format("2006-01-02")

	rows := [][]string{
		row("A metro posted-pay", "New York, NY", "false", recent, "200000", "", "0.9", "likely-real"),
		row("B remote estimated", "Austin, TX", "true", recent, "", "185000", "0.8", "uncertain"),
		row("C wrong city", "Denver, CO", "false", recent, "200000", "", "0.9", "likely-real"),
		row("D under floor", "Boston, MA", "false", recent, "140000", "", "0.7", "likely-real"),
		row("E ghost", "New York, NY", "false", recent, "200000", "", "0.2", "likely-ghost"),
		row("F stale", "Boston, MA", "false", old, "200000", "", "0.9", "likely-real"),
	}

	f := profile.Filters{
		Locations:     []string{"New York", "Boston", "Los Angeles"},
		RemoteOK:      true,
		MaxAgeDays:    90,
		MinSalary:     160000,
		MinScore:      0,
		IncludeGhosts: false,
	}
	got := Apply(header, rows, f, true, now)

	var titles []string
	for _, r := range got {
		titles = append(titles, r[0])
	}
	want := []string{"A metro posted-pay", "B remote estimated"}
	if len(titles) != len(want) {
		t.Fatalf("kept %v, want %v", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("kept %v, want %v", titles, want)
		}
	}
}

func TestApplyEstimateOffDropsEstimatedPay(t *testing.T) {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	recent := now.AddDate(0, 0, -5).Format("2006-01-02")
	rows := [][]string{
		row("estimated only", "New York, NY", "false", recent, "", "185000", "0.9", "likely-real"),
	}
	f := profile.Filters{Locations: []string{"New York"}, MinSalary: 160000}

	if got := Apply(header, rows, f, true, now); len(got) != 1 {
		t.Errorf("estimate on: kept %d, want 1", len(got))
	}
	if got := Apply(header, rows, f, false, now); len(got) != 0 {
		t.Errorf("estimate off: kept %d, want 0 (no posted pay to clear the floor)", len(got))
	}
}
