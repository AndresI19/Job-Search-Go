package apify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRun drives the full trigger → poll → fetch flow against a mock Apify API,
// so the client is exercised end-to-end with no real network calls or spend.
func TestRun(t *testing.T) {
	var gotInput map[string]any
	polls := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/acts/test-actor/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("runs: method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tkn" {
			t.Errorf("auth header = %q, want %q", got, "Bearer tkn")
		}
		_ = json.NewDecoder(r.Body).Decode(&gotInput)
		_, _ = w.Write([]byte(`{"data":{"id":"RUN1","status":"RUNNING","defaultDatasetId":"DS1"}}`))
	})
	mux.HandleFunc("/actor-runs/RUN1", func(w http.ResponseWriter, r *http.Request) {
		polls++
		status := "RUNNING"
		if polls >= 2 { // succeed on the second poll to exercise the loop
			status = "SUCCEEDED"
		}
		_, _ = w.Write([]byte(`{"data":{"id":"RUN1","status":"` + status + `","defaultDatasetId":"DS1"}}`))
	})
	mux.HandleFunc("/datasets/DS1/items", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"title":"Backend Engineer"},{"title":"SRE"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New("tkn", WithBaseURL(srv.URL))
	// Tiny poll interval so the two-poll loop returns immediately.
	started, err := c.StartRun(context.Background(), "test-actor", map[string]any{"count": 20})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	done, err := c.WaitForRun(context.Background(), started.ID, time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForRun: %v", err)
	}
	items, err := c.DatasetItems(context.Background(), done.DefaultDatasetID)
	if err != nil {
		t.Fatalf("DatasetItems: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if gotInput["count"] != float64(20) {
		t.Errorf("forwarded input count = %v, want 20", gotInput["count"])
	}
	if polls < 2 {
		t.Errorf("polled %d times, want >= 2", polls)
	}
	if !strings.Contains(string(items[0]), "Backend Engineer") {
		t.Errorf("item[0] = %s", items[0])
	}
}

// TestRateLimited checks a 429 surfaces as the ErrRateLimited sentinel so the
// caller can stop gracefully, and that it propagates through the Run flow.
func TestRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate-limit-exceeded"}}`))
	}))
	defer srv.Close()

	c := New("tkn", WithBaseURL(srv.URL))
	if _, err := c.Run(context.Background(), "test-actor", map[string]any{}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Run error = %v, want wrapped ErrRateLimited", err)
	}
}

// TestUsageLimit checks the account usage/budget cap (a non-429 error typed
// "…usage…") surfaces as ErrUsageLimit, so the run stops gracefully like a rate
// limit rather than failing generically.
func TestUsageLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"type":"monthly-usage-hard-limit-exceeded","message":"limit reached"}}`))
	}))
	defer srv.Close()

	c := New("tkn", WithBaseURL(srv.URL))
	if _, err := c.Run(context.Background(), "test-actor", map[string]any{}); !errors.Is(err, ErrUsageLimit) {
		t.Fatalf("Run error = %v, want wrapped ErrUsageLimit", err)
	}
}

// TestDatasetPaging checks a dataset larger than one page is walked in
// limit/offset batches and reassembled in order — the whole point being that no
// single request pulls the entire dataset into memory (which OOM-killed the
// container when a full scrape landed as one allocation).
func TestDatasetPaging(t *testing.T) {
	const total = datasetPageSize*2 + 7 // two full pages plus a short final one
	var gotOffsets []int
	var maxPage int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("clean") != "true" {
			t.Errorf("clean = %q, want true", q.Get("clean"))
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		if limit != datasetPageSize {
			t.Errorf("limit = %d, want %d", limit, datasetPageSize)
		}
		gotOffsets = append(gotOffsets, offset)

		n := total - offset
		if n > limit {
			n = limit
		}
		if n < 0 {
			n = 0
		}
		if n > maxPage {
			maxPage = n
		}
		items := make([]string, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, fmt.Sprintf(`{"title":"job-%d"}`, offset+i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	}))
	defer srv.Close()

	c := New("tkn", WithBaseURL(srv.URL))

	// EachDatasetPage streams: every batch must be bounded by the page size.
	var streamed int
	var batches int
	if err := c.EachDatasetPage(context.Background(), "DS1", func(page []json.RawMessage) error {
		batches++
		if len(page) > datasetPageSize {
			t.Errorf("batch of %d items exceeds page size %d", len(page), datasetPageSize)
		}
		streamed += len(page)
		return nil
	}); err != nil {
		t.Fatalf("EachDatasetPage: %v", err)
	}
	if streamed != total {
		t.Errorf("streamed %d items, want %d", streamed, total)
	}
	if batches != 3 {
		t.Errorf("got %d batches, want 3", batches)
	}
	if want := []int{0, datasetPageSize, datasetPageSize * 2}; !slices.Equal(gotOffsets, want) {
		t.Errorf("offsets = %v, want %v", gotOffsets, want)
	}

	// DatasetItems reassembles the same dataset, in order, from those pages.
	gotOffsets = nil
	items, err := c.DatasetItems(context.Background(), "DS1")
	if err != nil {
		t.Fatalf("DatasetItems: %v", err)
	}
	if len(items) != total {
		t.Fatalf("got %d items, want %d", len(items), total)
	}
	for _, i := range []int{0, datasetPageSize, total - 1} {
		if want := fmt.Sprintf(`"job-%d"`, i); !strings.Contains(string(items[i]), want) {
			t.Errorf("items[%d] = %s, want it to contain %s", i, items[i], want)
		}
	}
}

// TestDatasetPagingExact checks the boundary where the dataset is an exact
// multiple of the page size: the walk needs one extra empty request to learn it
// is done, and must not emit an empty batch or loop forever.
func TestDatasetPagingExact(t *testing.T) {
	const total = datasetPageSize
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		n := total - offset
		if n < 0 {
			n = 0
		}
		items := make([]string, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, `{"title":"t"}`)
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	}))
	defer srv.Close()

	c := New("tkn", WithBaseURL(srv.URL))
	batches := 0
	if err := c.EachDatasetPage(context.Background(), "DS1", func(page []json.RawMessage) error {
		if len(page) == 0 {
			t.Error("fn called with an empty batch")
		}
		batches++
		return nil
	}); err != nil {
		t.Fatalf("EachDatasetPage: %v", err)
	}
	if batches != 1 {
		t.Errorf("got %d batches, want 1", batches)
	}
	if requests != 2 {
		t.Errorf("got %d requests, want 2 (full page, then the empty page that ends it)", requests)
	}
}

// TestDatasetPagingStopsOnError checks a callback error aborts the walk instead
// of paging through the rest of the dataset.
func TestDatasetPagingStopsOnError(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		items := make([]string, 0, datasetPageSize)
		for i := 0; i < datasetPageSize; i++ {
			items = append(items, `{"title":"t"}`)
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	}))
	defer srv.Close()

	boom := errors.New("boom")
	c := New("tkn", WithBaseURL(srv.URL))
	if err := c.EachDatasetPage(context.Background(), "DS1", func([]json.RawMessage) error {
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if requests != 1 {
		t.Errorf("made %d requests, want 1 (walk should stop at the failing batch)", requests)
	}
}
