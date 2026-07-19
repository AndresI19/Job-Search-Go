// Package linkedin normalizes the raw dataset records produced by the Apify
// LinkedIn ingest Actor into the pipeline's model.Listing. It is the ingest-side
// adapter: everything downstream sees normalized Listings, never Actor JSON.
package linkedin

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

// Source is the value written to Listing.Source for LinkedIn-ingested rows.
const Source = "apify-linkedin"

// record is the subset of an Actor dataset item this adapter reads.
type record struct {
	ID                    string `json:"id"`
	Link                  string `json:"link"`
	Title                 string `json:"title"`
	CompanyName           string `json:"companyName"`
	CompanyURL            string `json:"companyLinkedinUrl"`
	Location              string `json:"location"`
	PostedAt              string `json:"postedAt"`
	ApplicantsCount       string `json:"applicantsCount"`
	ApplyURL              string `json:"applyUrl"`
	Salary                string `json:"salary"`
	CompanyEmployeesCount int    `json:"companyEmployeesCount"`
	Industries            string `json:"industries"`
	SeniorityLevel        string `json:"seniorityLevel"`
	DescriptionText       string `json:"descriptionText"`
	DescriptionHTML       string `json:"descriptionHtml"`
}

// Normalize maps raw Actor dataset items (the []RawMessage from apify.Client.Run)
// into Listings, skipping items that fail to decode or carry no title.
func Normalize(raw []json.RawMessage) []model.Listing {
	out := make([]model.Listing, 0, len(raw))
	for _, item := range raw {
		var r record
		if err := json.Unmarshal(item, &r); err != nil || strings.TrimSpace(r.Title) == "" {
			continue
		}
		applyType := "easy_apply"
		if r.ApplyURL != "" {
			applyType = "external"
		}
		salMin, salMax := parseSalary(r.Salary)
		desc := description(r)
		// Estimate pay only when the posting gives none, so a blank salary
		// doesn't hide a strong role — never overriding a real published figure.
		estMin, estMax := 0, 0
		if salMin == 0 && salMax == 0 {
			estMin, estMax = comp.Estimate(r.Title, r.SeniorityLevel, r.Location)
		}
		out = append(out, model.Listing{
			Source:           Source,
			JobID:            r.ID,
			Title:            r.Title,
			Company:          r.CompanyName,
			CompanyURL:       r.CompanyURL,
			CompanySize:      r.CompanyEmployeesCount,
			Industries:       r.Industries,
			Location:         r.Location,
			Remote:           isRemote(r.Location, desc),
			Posted:           parseDate(r.PostedAt),
			ApplicantCount:   parseApplicants(r.ApplicantsCount),
			YearsExperience:  parseYears(desc),
			SalaryMin:        salMin,
			SalaryMax:        salMax,
			SalaryEstMin:     estMin,
			SalaryEstMax:     estMax,
			ApplyType:        applyType,
			ExternalApplyURL: r.ApplyURL,
			URL:              r.Link,
			Description:      desc,
		})
	}
	return out
}

func parseDate(s string) time.Time {
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(s)); err == nil {
		return t
	}
	return time.Time{}
}

var digits = regexp.MustCompile(`\d+`)

// parseApplicants pulls the count out of LinkedIn's applicant string ("25",
// "Over 200 applicants"), returning -1 when there is no number to read.
func parseApplicants(s string) int {
	if m := digits.FindString(s); m != "" {
		if n, err := strconv.Atoi(m); err == nil {
			return n
		}
	}
	return -1
}

// salaryNum matches a dollar amount and its optional /yr or /hr unit.
var salaryNum = regexp.MustCompile(`\$\s*([\d,]+(?:\.\d+)?)\s*/?\s*(yr|hr|year|hour)?`)

// parseSalary extracts an annual USD range from LinkedIn's salary string
// ("$160,000.00/yr - $220,000.00/yr", or "$75.00/hr"). Hourly rates are
// annualized at 2080 hours. Returns 0,0 when there is no salary to read.
func parseSalary(s string) (min, max int) {
	var nums []int
	for _, m := range salaryNum.FindAllStringSubmatch(s, -1) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err != nil {
			continue
		}
		if strings.HasPrefix(m[2], "h") {
			v *= 2080
		}
		if v >= 1000 { // ignore stray small numbers that aren't a salary
			nums = append(nums, int(v+0.5))
		}
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

// remotePhrases are description signals that a role offers remote work. LinkedIn
// tags most remote-eligible jobs with a city (the HQ), so the location text alone
// misses them; these phrases are specific enough to avoid matching an in-office
// posting that merely mentions the word "remote".
var remotePhrases = []string{
	"fully remote", "100% remote", "remote-first", "remote first",
	"work from home", "work-from-home", "wfh", "work remotely",
	"remote position", "remote role", "remote opportunity",
	"remote-friendly", "remote friendly", "remote-eligible", "remote eligible",
}

// isRemote reports whether a listing offers remote work — from the location text
// (e.g. "Remote, US") or, failing that, an explicit signal in the description.
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

// yearsRE matches an experience requirement like "5+ years", "3-5 years", or
// "5 years of experience". It captures the first (minimum) number.
var yearsRE = regexp.MustCompile(`(?i)(\d{1,2})\s*\+?\s*(?:-\s*\d{1,2}\s*)?years?`)

// parseYears pulls the minimum years-of-experience a description asks for,
// bounded to a plausible 1–20 to avoid matching unrelated numbers ("2 years
// ago"). Returns 0 when nothing plausible is found.
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

// description prefers the Actor's plain-text field, falling back to stripping
// the HTML one.
func description(r record) string {
	if s := strings.TrimSpace(r.DescriptionText); s != "" {
		return strings.Join(strings.Fields(s), " ")
	}
	return ats.StripHTML(r.DescriptionHTML)
}
