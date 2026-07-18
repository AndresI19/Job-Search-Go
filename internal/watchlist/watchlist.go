// Package watchlist parses the watch-list config that drives a run: what job
// fields to search, at what salary, seniority, location, and recency. Salary,
// seniority, recency, and remote map to LinkedIn's server-side search facets so
// the scrape returns already-filtered results; a light age check post-filters
// what comes back.
package watchlist

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Query is one search in the watch-list.
type Query struct {
	Field      string   `yaml:"field"`      // keywords / job field (required)
	SalaryMin  int      `yaml:"salary_min"` // minimum annual salary, USD
	SalaryMax  int      `yaml:"salary_max"` // informational; LinkedIn filters on a floor only
	Location   string   `yaml:"location"`   // LinkedIn location string
	Seniority  []string `yaml:"seniority"`  // e.g. [mid, senior]
	Remote     bool     `yaml:"remote"`     // remote-only
	MaxAgeDays int      `yaml:"max_age_days"`
	Sources    []string `yaml:"sources"` // ATS boards to verify against, e.g. [greenhouse, lever]
}

// Watchlist is a set of queries plus a defaults block they inherit from.
type Watchlist struct {
	Defaults Query   `yaml:"defaults"`
	Queries  []Query `yaml:"queries"`
}

// Load reads and parses a watch-list YAML file, applying the defaults block to
// every query so per-query blocks only state what differs.
func Load(path string) (*Watchlist, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("watchlist: %w", err)
	}
	var wl Watchlist
	if err := yaml.Unmarshal(b, &wl); err != nil {
		return nil, fmt.Errorf("watchlist %s: %w", path, err)
	}
	if len(wl.Queries) == 0 {
		return nil, fmt.Errorf("watchlist %s: no queries defined", path)
	}
	for i := range wl.Queries {
		wl.Queries[i].inheritFrom(wl.Defaults)
		if strings.TrimSpace(wl.Queries[i].Field) == "" {
			return nil, fmt.Errorf("watchlist %s: query %d has no field", path, i+1)
		}
	}
	return &wl, nil
}

// inheritFrom fills fields left unset on a query from the defaults block.
func (q *Query) inheritFrom(d Query) {
	if q.Location == "" {
		q.Location = d.Location
	}
	if q.MaxAgeDays == 0 {
		q.MaxAgeDays = d.MaxAgeDays
	}
	if q.SalaryMin == 0 {
		q.SalaryMin = d.SalaryMin
	}
	if q.SalaryMax == 0 {
		q.SalaryMax = d.SalaryMax
	}
	if len(q.Seniority) == 0 {
		q.Seniority = d.Seniority
	}
	if len(q.Sources) == 0 {
		q.Sources = d.Sources
	}
	if !q.Remote {
		q.Remote = d.Remote
	}
}

const searchBase = "https://www.linkedin.com/jobs/search/"

// SearchURL builds the LinkedIn jobs-search URL for this query, mapping salary,
// seniority, recency, and remote to LinkedIn's server-side facets.
func (q Query) SearchURL() string {
	v := url.Values{}
	v.Set("keywords", q.Field)
	if q.Location != "" {
		v.Set("location", q.Location)
	}
	if q.MaxAgeDays > 0 {
		v.Set("f_TPR", fmt.Sprintf("r%d", q.MaxAgeDays*86400)) // "posted within N days" in seconds
	}
	if q.Remote {
		v.Set("f_WT", "2")
	}
	if b := salaryBucket(q.SalaryMin); b != "" {
		v.Set("f_SB2", b)
	}
	if e := experienceCodes(q.Seniority); e != "" {
		v.Set("f_E", e)
	}
	return searchBase + "?" + v.Encode()
}

// salaryBucket maps a minimum annual salary (USD) to LinkedIn's f_SB2 bucket
// (1=$40k … 9=$200k), returning the highest bucket whose floor the salary meets,
// or "" below the lowest bucket.
func salaryBucket(minUSD int) string {
	floors := []int{40, 60, 80, 100, 120, 140, 160, 180, 200} // thousands
	code := ""
	for i, floor := range floors {
		if minUSD >= floor*1000 {
			code = strconv.Itoa(i + 1)
		}
	}
	return code
}

// experienceCode maps seniority names to LinkedIn's f_E codes.
var experienceCode = map[string]string{
	"internship": "1", "intern": "1",
	"entry": "2", "entry-level": "2",
	"associate": "3",
	"mid":       "4", "senior": "4", "mid-senior": "4",
	"director":  "5",
	"executive": "6",
}

// experienceCodes maps seniority names to LinkedIn's f_E codes, de-duplicated and
// comma-joined (mid and senior both fall in the mid-senior bucket).
func experienceCodes(seniority []string) string {
	seen := map[string]bool{}
	var codes []string
	for _, s := range seniority {
		if c, ok := experienceCode[strings.ToLower(strings.TrimSpace(s))]; ok && !seen[c] {
			seen[c] = true
			codes = append(codes, c)
		}
	}
	return strings.Join(codes, ",")
}

// Fresh reports whether a listing is recent enough for this query. Listings with
// an unknown posting date are kept (the recency facet already filtered the scrape).
func (q Query) Fresh(l model.Listing, now time.Time) bool {
	if q.MaxAgeDays <= 0 || l.Posted.IsZero() {
		return true
	}
	return !l.Posted.Before(now.AddDate(0, 0, -q.MaxAgeDays))
}
