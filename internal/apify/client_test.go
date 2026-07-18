package apify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
