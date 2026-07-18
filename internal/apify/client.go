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
	"time"
)

const defaultBaseURL = "https://api.apify.com/v2"

// ErrRateLimited is returned (wrapped) when Apify responds 429 Too Many Requests.
// Callers can errors.Is on it to stop ingesting gracefully and keep the data
// collected so far, rather than failing the run.
var ErrRateLimited = errors.New("apify: rate limited")

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

// DatasetItems fetches every item of datasetID as raw JSON objects, leaving
// field mapping to the caller (the normalizer).
func (c *Client) DatasetItems(ctx context.Context, datasetID string) ([]json.RawMessage, error) {
	url := fmt.Sprintf("%s/datasets/%s/items?clean=true&format=json", c.baseURL, datasetID)
	var items []json.RawMessage
	if err := c.do(ctx, http.MethodGet, url, nil, &items); err != nil {
		return nil, fmt.Errorf("fetch dataset: %w", err)
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
	if resp.StatusCode == http.StatusTooManyRequests {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%w: %s %s: %s", ErrRateLimited, method, url, bytes.TrimSpace(b))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("apify %s %s: %d: %s", method, url, resp.StatusCode, bytes.TrimSpace(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
