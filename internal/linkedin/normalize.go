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
	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Source is the value written to Listing.Source for LinkedIn-ingested rows.
const Source = "apify-linkedin"

// record is the subset of an Actor dataset item this adapter reads.
type record struct {
	ID              string `json:"id"`
	Link            string `json:"link"`
	Title           string `json:"title"`
	CompanyName     string `json:"companyName"`
	CompanyURL      string `json:"companyLinkedinUrl"`
	Location        string `json:"location"`
	PostedAt        string `json:"postedAt"`
	ApplicantsCount string `json:"applicantsCount"`
	ApplyURL        string `json:"applyUrl"`
	DescriptionText string `json:"descriptionText"`
	DescriptionHTML string `json:"descriptionHtml"`
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
		out = append(out, model.Listing{
			Source:           Source,
			JobID:            r.ID,
			Title:            r.Title,
			Company:          r.CompanyName,
			CompanyURL:       r.CompanyURL,
			Location:         r.Location,
			Remote:           strings.Contains(strings.ToLower(r.Location), "remote"),
			Posted:           parseDate(r.PostedAt),
			ApplicantCount:   parseApplicants(r.ApplicantsCount),
			ApplyType:        applyType,
			ExternalApplyURL: r.ApplyURL,
			URL:              r.Link,
			Description:      description(r),
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

// description prefers the Actor's plain-text field, falling back to stripping
// the HTML one.
func description(r record) string {
	if s := strings.TrimSpace(r.DescriptionText); s != "" {
		return strings.Join(strings.Fields(s), " ")
	}
	return ats.StripHTML(r.DescriptionHTML)
}
