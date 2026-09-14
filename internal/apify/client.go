// Package apify is a minimal client for the Apify REST API. It triggers an
// actor run, polls until the run finishes, and fetches the run's dataset —
// the trigger → poll → fetch flow used to ingest LinkedIn listings.
//
// See https://docs.apify.com/api/v2 for the underlying endpoints.
package apify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.apify.com/v2"

// ErrRateLimited is returned (wrapped) when Apify throttles requests (HTTP 429).
// ErrUsageLimit is returned (wrapped) when Apify blocks a request because the
// account's usage/budget limit is reached (e.g. the free plan's monthly cap).
// Both mean "we can't keep ingesting"; callers errors.Is on them to stop
// gracefully and keep the data collected so far rather than failing the run.
var (
	ErrRateLimited = errors.New("apify: rate limited")
	ErrUsageLimit  = errors.New("apify: usage limit reached")
)

// classifyLimit inspects a non-2xx Apify response and returns the matching limit
// sentinel, or nil for an ordinary error. It keys off the structured error type
// (stabler than the status code, which differs between rate and usage limits).
func classifyLimit(status int, body []byte) error {
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	t := strings.ToLower(e.Error.Type)
	switch {
	case status == http.StatusTooManyRequests || strings.Contains(t, "rate-limit"):
		return ErrRateLimited
	case strings.Contains(t, "usage"): // e.g. "monthly-usage-hard-limit-exceeded"
		return ErrUsageLimit
	}
	return nil
}

// Client talks to the Apify REST API using a bearer token.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// Option customizes a Client.
type Option func(*Client)

// WithBaseURL overrides the API host (used in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient overrides the underlying http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New returns a Client authenticated with token.
func New(token string, opts ...Option) *Client {
	c := &Client{
		token:   token,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// RunInfo is the subset of an Apify run object this client uses.
type RunInfo struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	DefaultDatasetID string `json:"defaultDatasetId"`
}

// StartRun triggers actorID with the given input and returns the created run.
func (c *Client) StartRun(ctx context.Context, actorID string, input any) (RunInfo, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return RunInfo{}, fmt.Errorf("marshal input: %w", err)
	}
	url := fmt.Sprintf("%s/acts/%s/runs", c.baseURL, actorID)
	var resp struct {
		Data RunInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, url, bytes.NewReader(body), &resp); err != nil {
		return RunInfo{}, fmt.Errorf("start run: %w", err)
	}
	return resp.Data, nil
}

// WaitForRun polls runID every poll interval until it reaches a terminal
// status, honoring ctx cancellation. A non-SUCCEEDED terminal status is an
// error.
func (c *Client) WaitForRun(ctx context.Context, runID string, poll time.Duration) (RunInfo, error) {
	if poll <= 0 {
		poll = 3 * time.Second
	}
	url := fmt.Sprintf("%s/actor-runs/%s", c.baseURL, runID)
	for {
		var resp struct {
			Data RunInfo `json:"data"`
		}
		if err := c.do(ctx, http.MethodGet, url, nil, &resp); err != nil {
			return RunInfo{}, fmt.Errorf("poll run: %w", err)
		}
		switch resp.Data.Status {
		case "SUCCEEDED":
			return resp.Data, nil
		case "FAILED", "ABORTED", "TIMED-OUT":
			return resp.Data, fmt.Errorf("run %s ended with status %s", runID, resp.Data.Status)
		}
		select {
		case <-ctx.Done():
			return RunInfo{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// RunStatus fetches a run's current state in a single call (WaitForRun loops on
// this; a progress poller calls it directly alongside DatasetInfo).
func (c *Client) RunStatus(ctx context.Context, runID string) (RunInfo, error) {
	url := fmt.Sprintf("%s/actor-runs/%s", c.baseURL, runID)
	var resp struct {
		Data RunInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, url, nil, &resp); err != nil {
		return RunInfo{}, fmt.Errorf("run status: %w", err)
	}
	return resp.Data, nil
}

// DatasetInfo returns a dataset's current item count — polled during a run to
// drive a scrape-progress indicator (the actor pushes items as it scrapes).
func (c *Client) DatasetInfo(ctx context.Context, datasetID string) (int, error) {
	url := fmt.Sprintf("%s/datasets/%s", c.baseURL, datasetID)
	var resp struct {
		Data struct {
			ItemCount int `json:"itemCount"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, url, nil, &resp); err != nil {
		return 0, err
	}
	return resp.Data.ItemCount, nil
}

// Budget is the account's month-to-date Apify spend against its cap, and when that cap rolls over.
//
// CycleEnd matters as much as the numbers: "you have spent your budget" is a dead end, while "you
// have spent your budget, it resets on the 29th" is something the reader can act on. Apify reports
// the cycle alongside the usage, so carrying it costs nothing.
type Budget struct {
	UsedUSD  float64
	LimitUSD float64
	CycleEnd time.Time // zero when Apify did not report a cycle
}

// Remaining is what is left to spend. Negative is clamped away: a cap can be fractionally exceeded
// by a run already in flight, and "-$0.01 left" is a worse thing to render than "$0.00 left".
func (b Budget) Remaining() float64 {
	if r := b.LimitUSD - b.UsedUSD; r > 0 {
		return r
	}
	return 0
}

// Known reports whether the figures mean anything. A zero limit is how an unreadable or unreported
// budget travels through the system, and every consumer must be able to tell that from "zero left".
func (b Budget) Known() bool { return b.LimitUSD > 0 }

// Budget returns the account's month-to-date Apify spend, its cap, and the cycle end. Best-effort on
// shape: fields Apify renames come back zero, which Known() reports as unknown, rather than as an
// error every caller has to distinguish from a real one.
func (c *Client) Budget(ctx context.Context) (Budget, error) {
	url := fmt.Sprintf("%s/users/me/limits", c.baseURL)
	var resp struct {
		Data struct {
			Current struct {
				MonthlyUsageUsd float64 `json:"monthlyUsageUsd"`
			} `json:"current"`
			Limits struct {
				MaxMonthlyUsageUsd float64 `json:"maxMonthlyUsageUsd"`
			} `json:"limits"`
			MonthlyUsageCycle struct {
				EndAt string `json:"endAt"`
			} `json:"monthlyUsageCycle"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, url, nil, &resp); err != nil {
		return Budget{}, err
	}
	b := Budget{
		UsedUSD:  resp.Data.Current.MonthlyUsageUsd,
		LimitUSD: resp.Data.Limits.MaxMonthlyUsageUsd,
	}
	// An unparseable date leaves CycleEnd zero rather than failing the whole read — the spend
	// figures are the load-bearing part, and the reset date is a nicety on top of them.
	if t, perr := time.Parse(time.RFC3339, resp.Data.MonthlyUsageCycle.EndAt); perr == nil {
		b.CycleEnd = t
	}
	return b, nil
}

// datasetPageSize bounds how many dataset items a single request pulls. Apify
// will return an entire dataset in one response, but a full scrape then lands as
// one slice holding every raw item at once — the allocation that OOM-killed the
// container at its 256Mi limit. Paging lets a caller normalize and discard each
// batch, so peak memory tracks the page size rather than the size of the run.
const datasetPageSize = 250

// EachDatasetPage walks datasetID in pages, handing each batch of raw items to
// fn. It stops at the first short page — Apify's signal that the dataset is
// exhausted — or at the first error fn returns. Prefer this over DatasetItems
// wherever items can be processed incrementally.
func (c *Client) EachDatasetPage(ctx context.Context, datasetID string, fn func([]json.RawMessage) error) error {
	for offset := 0; ; offset += datasetPageSize {
		url := fmt.Sprintf("%s/datasets/%s/items?clean=true&format=json&limit=%d&offset=%d",
			c.baseURL, datasetID, datasetPageSize, offset)
		var page []json.RawMessage
		if err := c.do(ctx, http.MethodGet, url, nil, &page); err != nil {
			return fmt.Errorf("fetch dataset: %w", err)
		}
		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		if len(page) < datasetPageSize {
			return nil
		}
	}
}

// DatasetItems fetches every item of datasetID as raw JSON objects, leaving
// field mapping to the caller (the normalizer). It holds the whole dataset in
// memory; callers that can work batch-by-batch should use EachDatasetPage.
func (c *Client) DatasetItems(ctx context.Context, datasetID string) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := c.EachDatasetPage(ctx, datasetID, func(page []json.RawMessage) error {
		items = append(items, page...)
		return nil
	}); err != nil {
		return nil, err
	}
	return items, nil
}

// Run is the convenience flow: trigger the actor, wait for completion, and
// return its dataset items.
func (c *Client) Run(ctx context.Context, actorID string, input any) ([]json.RawMessage, error) {
	started, err := c.StartRun(ctx, actorID, input)
	if err != nil {
		return nil, err
	}
	done, err := c.WaitForRun(ctx, started.ID, 0)
	if err != nil {
		return nil, err
	}
	return c.DatasetItems(ctx, done.DefaultDatasetID)
}

// do performs an authenticated request and decodes a JSON response into out
// (skipped when out is nil).
func (c *Client) do(ctx context.Context, method, url string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if limitErr := classifyLimit(resp.StatusCode, b); limitErr != nil {
			return fmt.Errorf("%w: %s %s: %s", limitErr, method, url, bytes.TrimSpace(b))
		}
		return fmt.Errorf("apify %s %s: %d: %s", method, url, resp.StatusCode, bytes.TrimSpace(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
