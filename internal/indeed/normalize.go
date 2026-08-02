// Package indeed normalizes the raw dataset records from the Apify Indeed ingest
// Actor (curious_coder/indeed-scraper) into the pipeline's model.Listing — the
// Indeed-side counterpart to package linkedin. Everything downstream sees
// normalized Listings, never Actor JSON.
//
// The Indeed actor's fields are looser than LinkedIn's: salary can be a structured
// object, a plain string, or null; company and location arrive both flat and
// nested. Accessors below read whichever form is present, so the adapter is robust
// to the exact shape a given actor build returns (validated against a live sample).
package indeed

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/ats"
	"github.com/AndresI19/Job-Search-Go/internal/comp"
	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Source is the value written to Listing.Source for Indeed-ingested rows.
const Source = "apify-indeed"

// record is the subset of an Indeed Actor dataset item this adapter reads. Fields
// with alternates cover the flat and nested variants the actor may emit.
type record struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Position string `json:"positionName"` // alt title (misceres-style builds)

	Company        string          `json:"company"`
	CompanyDetails json.RawMessage `json:"companyDetails"` // {name, employeeCount, ...}

	FormattedLocation string          `json:"formattedLocation"`
	Location          json.RawMessage `json:"location"` // string OR {city,state,country,formattedLocation}

	Salary   json.RawMessage `json:"salary"`   // object {min,max,type,currency}, string, or null
	JobTypes []string        `json:"jobTypes"` // ["Full-time"] / ["Contract"]
	JobType  []string        `json:"jobType"`  // alt name

	ViewJobLink      string `json:"viewJobLink"`      // relative path
	URL              string `json:"url"`              // absolute, when present
	OriginalApplyURL string `json:"originalApplyUrl"` // external apply link

	JobDescription     string `json:"jobDescription"`
	JobDescriptionHTML string `json:"jobDescriptionHTML"`
	Description        string `json:"description"`     // alt plain text
	DescriptionHTML    string `json:"descriptionHTML"` // alt html

	PubDate  int64  `json:"pubDate"`  // unix ms
	PostedAt string `json:"postedAt"` // alt ISO/relative
}

// Normalize maps raw Indeed Actor dataset items into Listings, skipping items that
// fail to decode or carry no title.
func Normalize(raw []json.RawMessage) []model.Listing {
	out := make([]model.Listing, 0, len(raw))
	for _, item := range raw {
		var r record
		if err := json.Unmarshal(item, &r); err != nil {
			continue
		}
		title := firstNonEmpty(r.Title, r.Position)
		if strings.TrimSpace(title) == "" {
			continue
		}
		desc := r.description()
		company := r.company()
		location := r.location()
		salMin, salMax := r.salary()
		estMin, estMax := 0, 0
		if salMin == 0 && salMax == 0 {
			estMin, estMax = comp.Estimate(title, "", location)
		}
		apply := strings.TrimSpace(r.OriginalApplyURL)
		applyType := "easy_apply"
		if apply != "" {
			applyType = "external"
		}
		out = append(out, model.Listing{
			Source:           Source,
			JobID:            r.ID,
			Title:            title,
			Company:          company,
			EmploymentType:   r.employmentType(),
			Location:         location,
			Remote:           isRemote(location, desc),
			Posted:           r.posted(),
			ApplicantCount:   -1, // Indeed does not expose an applicant count
			YearsExperience:  parseYears(desc),
			SalaryMin:        salMin,
			SalaryMax:        salMax,
			SalaryEstMin:     estMin,
			SalaryEstMax:     estMax,
			ApplyType:        applyType,
			ExternalApplyURL: apply,
			URL:              r.jobURL(),
			Description:      desc,
		})
	}
	return out
}

// --- record accessors (tolerate flat vs nested shapes) ---

func (r record) company() string {
	if c := strings.TrimSpace(r.Company); c != "" {
		return c
	}
	var cd struct {
		Name string `json:"name"`
	}
	if len(r.CompanyDetails) > 0 && json.Unmarshal(r.CompanyDetails, &cd) == nil {
		return strings.TrimSpace(cd.Name)
	}
	return ""
}

func (r record) location() string {
	if l := strings.TrimSpace(r.FormattedLocation); l != "" {
		return l
	}
	var s string
	if len(r.Location) > 0 && json.Unmarshal(r.Location, &s) == nil && s != "" {
		return strings.TrimSpace(s)
	}
	var lo struct {
		FormattedLocation, City, State, Country string
	}
	if len(r.Location) > 0 && json.Unmarshal(r.Location, &lo) == nil {
		if lo.FormattedLocation != "" {
			return lo.FormattedLocation
		}
		parts := []string{}
		for _, p := range []string{lo.City, lo.State, lo.Country} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

func (r record) description() string {
	if s := strings.TrimSpace(firstNonEmpty(r.JobDescription, r.Description)); s != "" {
		return strings.Join(strings.Fields(s), " ")
	}
	return ats.StripHTML(firstNonEmpty(r.JobDescriptionHTML, r.DescriptionHTML))
}

func (r record) employmentType() string {
	t := r.JobTypes
	if len(t) == 0 {
		t = r.JobType
	}
	return strings.Join(t, ", ")
}

// jobURL returns an absolute Indeed posting URL, absolutizing a relative
// viewJobLink against the US host when no absolute url is present.
func (r record) jobURL() string {
	if u := strings.TrimSpace(r.URL); u != "" {
		return u
	}
	v := strings.TrimSpace(r.ViewJobLink)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "http") {
		return v
	}
	return "https://www.indeed.com" + v
}

func (r record) posted() time.Time {
	if r.PubDate > 0 {
		return time.UnixMilli(r.PubDate).UTC()
	}
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(r.PostedAt)); err == nil {
		return t
	}
	return time.Time{}
}

// salary reads Indeed's salary in whatever form the actor emitted (structured
// object, plain string, or null) and returns an annual USD range. Hourly figures
// are annualized at 2080 hours.
func (r record) salary() (min, max int) {
	if len(r.Salary) == 0 {
		return 0, 0
	}
	var so struct {
		Min, Max float64
		Type     string
	}
	if json.Unmarshal(r.Salary, &so) == nil && (so.Min > 0 || so.Max > 0) {
		mult := 1.0
		if strings.Contains(strings.ToLower(so.Type), "hour") {
			mult = 2080
		}
		min, max = int(so.Min*mult+0.5), int(so.Max*mult+0.5)
		if max == 0 {
			max = min
		}
		return min, max
	}
	var s string
	if json.Unmarshal(r.Salary, &s) == nil {
		return parseSalaryString(s)
	}
	return 0, 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// --- shared parsing (kept local to decouple from package linkedin) ---

var salaryNum = regexp.MustCompile(`\$\s*([\d,]+(?:\.\d+)?)\s*/?\s*(yr|hr|year|hour|annually|hourly)?`)

func parseSalaryString(s string) (min, max int) {
	// A trailing unit in a range ("$85 - $95 / hr") lexically attaches only to the
	// last figure, so infer a string-wide hourly flag: without it the first number
	// annualizes to nothing and gets dropped as sub-$1,000 noise.
	ls := strings.ToLower(s)
	hourly := strings.Contains(ls, "hr") || strings.Contains(ls, "hour")
	var nums []int
	for _, m := range salaryNum.FindAllStringSubmatch(s, -1) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err != nil {
			continue
		}
		perHour := hourly
		if m[2] != "" { // an explicit per-number unit overrides the string-wide guess
			perHour = strings.HasPrefix(m[2], "h")
		}
		if perHour {
			v *= 2080
		} else if v < 1000 {
			continue // a bare sub-$1,000 figure with no hourly context is noise
		}
		nums = append(nums, int(v+0.5))
	}
	if len(nums) == 0 {
		return 0, 0
	}
	min, max = nums[0], nums[0]
	for _, n := range nums {
		if n < min {
			min = n
		}
		if n > max {
			max = n
		}
	}
	return min, max
}

var remotePhrases = []string{
	"fully remote", "100% remote", "remote-first", "remote first",
	"work from home", "work-from-home", "wfh", "work remotely",
	"remote position", "remote role", "remote-eligible", "remote eligible",
}

func isRemote(location, description string) bool {
	if strings.Contains(strings.ToLower(location), "remote") {
		return true
	}
	d := strings.ToLower(description)
	for _, p := range remotePhrases {
		if strings.Contains(d, p) {
			return true
		}
	}
	return false
}

var yearsRE = regexp.MustCompile(`(?i)(\d{1,2})\s*\+?\s*(?:-\s*\d{1,2}\s*)?years?`)

func parseYears(desc string) int {
	m := yearsRE.FindStringSubmatch(desc)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 || n > 20 {
		return 0
	}
	return n
}
