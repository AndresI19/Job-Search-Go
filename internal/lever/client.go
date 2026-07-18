// Package lever fetches a company's public Lever job board and maps its open
// postings into normalized model.Listings — the same ground-truth role the
// greenhouse source plays for Greenhouse-hosted companies.
//
// It uses the public postings API, which needs no authentication. The company
// handle is a company's Lever slug: "netflix" resolves to
// api.lever.co/v0/postings/netflix?mode=json.
package lever

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/ats"
	"github.com/AndresI19/Job-Search-Go/internal/model"
)

const defaultBaseURL = "https://api.lever.co/v0"

// Client fetches public Lever boards. Create one with New; the zero value is
// not usable.
type Client struct {
	baseURL string
	http    *http.Client
}

// Option customizes a Client.
type Option func(*Client)

// WithBaseURL overrides the API host (used in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient overrides the underlying http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New returns a Client pointed at the public postings API.
func New(opts ...Option) *Client {
	c := &Client{baseURL: defaultBaseURL, http: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Compile-time guarantee that a Client is a model.Source.
var _ model.Source = (*Client)(nil)

// Name identifies this source in logs and Verdict fields.
func (c *Client) Name() string { return "lever" }

// posting is the subset of a Lever postings API object this client reads. The
// postings endpoint returns a top-level JSON array of these.
type posting struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	HostedURL  string `json:"hostedUrl"`
	CreatedAt  int64  `json:"createdAt"` // epoch milliseconds
	Categories struct {
		Location string `json:"location"`
	} `json:"categories"`
	WorkplaceType    string `json:"workplaceType"` // "remote" | "hybrid" | "on-site", when present
	DescriptionPlain string `json:"descriptionPlain"`
	Description      string `json:"description"`
}

// Fetch returns the open postings on the Lever board identified by query, which
// for this source is a company's Lever handle (its slug, e.g. "netflix"). The
// handle is carried into each Listing's Company/CompanyURL — the canonical
// handle the verification pipeline joins on. A missing board (HTTP 404) is
// returned as an error so a bad slug stays distinguishable from a company with
// no open roles.
func (c *Client) Fetch(ctx context.Context, query string) ([]model.Listing, error) {
	handle := strings.TrimSpace(query)
	if handle == "" {
		return nil, fmt.Errorf("lever: empty company handle")
	}
	url := fmt.Sprintf("%s/postings/%s?mode=json", c.baseURL, handle)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("lever GET %s: %d: %s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var postings []posting
	if err := json.NewDecoder(resp.Body).Decode(&postings); err != nil {
		return nil, fmt.Errorf("lever decode postings %q: %w", handle, err)
	}
	listings := make([]model.Listing, 0, len(postings))
	for _, p := range postings {
		listings = append(listings, model.Listing{
			Source:         "lever",
			JobID:          p.ID,
			Title:          p.Text,
			Company:        handle,
			CompanyURL:     handle,
			Location:       p.Categories.Location,
			Remote:         isRemote(p.WorkplaceType, p.Categories.Location),
			Posted:         postedAt(p.CreatedAt),
			ApplicantCount: -1, // an ATS board does not report applicant counts
			URL:            p.HostedURL,
			Description:    description(p),
		})
	}
	return listings, nil
}

// isRemote trusts Lever's explicit workplaceType when set, falling back to the
// location text for older postings that predate the field.
func isRemote(workplaceType, location string) bool {
	if strings.EqualFold(workplaceType, "remote") {
		return true
	}
	return strings.Contains(strings.ToLower(location), "remote")
}

func postedAt(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// description prefers Lever's plain-text field and falls back to stripping the
// HTML description when the plain one is absent.
func description(p posting) string {
	if s := strings.TrimSpace(p.DescriptionPlain); s != "" {
		return strings.Join(strings.Fields(s), " ")
	}
	return ats.StripHTML(p.Description)
}
