// Package greenhouse fetches a company's public Greenhouse job board and maps
// its open requisitions into normalized model.Listings, so a scraped posting
// can be cross-referenced against what the company actually has open — the ATS
// board being the legitimacy source of truth.
//
// It uses the public Boards API, which needs no authentication. The board token
// is a company's Greenhouse slug: "stripe" resolves to
// boards-api.greenhouse.io/v1/boards/stripe/jobs.
package greenhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

const defaultBaseURL = "https://boards-api.greenhouse.io/v1"

// Client fetches public Greenhouse boards. Create one with New; the zero value
// is not usable.
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

// New returns a Client pointed at the public Boards API.
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
func (c *Client) Name() string { return "greenhouse" }

// board is the subset of the Boards API /jobs response this client reads.
type board struct {
	Jobs []struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		AbsoluteURL string `json:"absolute_url"`
		UpdatedAt   string `json:"updated_at"`
		Location    struct {
			Name string `json:"name"`
		} `json:"location"`
		Content string `json:"content"`
	} `json:"jobs"`
}

// Fetch returns the open requisitions on the Greenhouse board identified by
// query, which for this source is a company's Greenhouse board token (its slug,
// e.g. "stripe"). The token is carried into each Listing's Company/CompanyURL —
// the canonical handle the verification pipeline joins on. A missing board
// (HTTP 404) is returned as an error so a bad slug is distinguishable from a
// company that simply has no open roles.
func (c *Client) Fetch(ctx context.Context, query string) ([]model.Listing, error) {
	token := strings.TrimSpace(query)
	if token == "" {
		return nil, fmt.Errorf("greenhouse: empty board token")
	}
	url := fmt.Sprintf("%s/boards/%s/jobs?content=true", c.baseURL, token)
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
		return nil, fmt.Errorf("greenhouse GET %s: %d: %s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var bd board
	if err := json.NewDecoder(resp.Body).Decode(&bd); err != nil {
		return nil, fmt.Errorf("greenhouse decode board %q: %w", token, err)
	}
	listings := make([]model.Listing, 0, len(bd.Jobs))
	for _, j := range bd.Jobs {
		listings = append(listings, model.Listing{
			Source:         "greenhouse",
			JobID:          strconv.FormatInt(j.ID, 10),
			Title:          j.Title,
			Company:        token,
			CompanyURL:     token,
			Location:       j.Location.Name,
			Remote:         strings.Contains(strings.ToLower(j.Location.Name), "remote"),
			Posted:         parseTime(j.UpdatedAt),
			ApplicantCount: -1, // an ATS board does not report applicant counts
			URL:            j.AbsoluteURL,
			Description:    stripHTML(j.Content),
		})
	}
	return listings, nil
}

// parseTime accepts the ISO-8601 shapes Greenhouse emits for updated_at,
// returning the zero time when the value is absent or unrecognized.
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// stripHTML turns Greenhouse's entity-encoded HTML content into plain text.
// The content field arrives entity-encoded, so the first unescape yields markup;
// tags are dropped and a second unescape resolves entities that were inside it.
func stripHTML(s string) string {
	s = html.UnescapeString(s)
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}
