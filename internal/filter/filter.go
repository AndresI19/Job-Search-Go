// Package filter applies a profile's post-ingest filters to verified result
// rows. It works on the shared CSV representation (output.Header / output.Rows)
// rather than on model.Result, so the CLI and the GUI run the exact same logic:
// the CLI over freshly-verified rows, the GUI over a cached result set.
package filter

import (
	"strconv"
	"strings"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/profile"
)

// Apply returns the rows that survive the profile's filters, preserving order.
// header names come from output.Header; estimate reports whether estimated pay
// counts toward the salary floor; now anchors the freshness cutoff.
func Apply(header []string, rows [][]string, f profile.Filters, estimate bool, now time.Time) [][]string {
	col := indexer(header)
	locI, remI, postI := col("location"), col("remote"), col("posted")
	smaxI, emaxI := col("salary_max"), col("salary_est_max")
	scoreI, confI := col("score"), col("confidence")

	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		switch {
		case !matchLocation(cell(r, locI), boolCell(r, remI), f.Locations, f.RemoteOK):
		case f.MinSalary > 0 && effMax(r, smaxI, emaxI, estimate) < float64(f.MinSalary):
		case f.MinScore > 0 && numCell(r, scoreI) < f.MinScore:
		case !f.IncludeGhosts && strings.EqualFold(cell(r, confI), "likely-ghost"):
		case f.MaxAgeDays > 0 && tooOld(cell(r, postI), f.MaxAgeDays, now):
		default:
			out = append(out, r)
		}
	}
	return out
}

// matchLocation keeps a listing whose location matches any configured term, or —
// when remote is allowed — any remote role. No locations and no remote allowance
// means no location constraint at all.
func matchLocation(loc string, remote bool, locations []string, remoteOK bool) bool {
	if len(locations) == 0 && !remoteOK {
		return true
	}
	if remoteOK && remote {
		return true
	}
	l := strings.ToLower(loc)
	for _, t := range locations {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if t == "remote" && remote {
			return true
		}
		if strings.Contains(l, t) {
			return true
		}
	}
	return false
}

// effMax is the max pay used for the salary floor: the posted figure, or the
// estimate when none was posted and estimates are in play.
func effMax(r []string, smaxI, emaxI int, estimate bool) float64 {
	if m := numCell(r, smaxI); m > 0 {
		return m
	}
	if estimate {
		return numCell(r, emaxI)
	}
	return 0
}

// tooOld reports whether a posting predates the age cutoff. An unknown or
// unparseable date is kept (never "too old"), matching the ingest freshness rule.
func tooOld(posted string, maxAgeDays int, now time.Time) bool {
	t, err := time.Parse("2006-01-02", posted)
	if err != nil {
		return false
	}
	return t.Before(now.AddDate(0, 0, -maxAgeDays))
}

func indexer(header []string) func(string) int {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.ToLower(h)] = i
	}
	return func(name string) int {
		if i, ok := idx[name]; ok {
			return i
		}
		return -1
	}
}

func cell(r []string, i int) string {
	if i < 0 || i >= len(r) {
		return ""
	}
	return r[i]
}

func numCell(r []string, i int) float64 {
	v, _ := strconv.ParseFloat(cell(r, i), 64)
	return v
}

func boolCell(r []string, i int) bool {
	b, _ := strconv.ParseBool(cell(r, i))
	return b
}
