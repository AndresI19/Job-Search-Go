package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/apify"
)

// The Apify budget guard and the spend figures shown during a run.
//
// This is a regression suite for a bug that cost real money: the run strip was seeded with an
// invented "$0.19 / $5.00 free-plan baseline" and the true figure was only fetched at the END of a
// successful run. Because the scrape bills in the first seconds, every run that died in between —
// and several did, OOM-killed — left a comfortable 19¢ on screen while the account climbed to its
// $5 cap. The three behaviours asserted here are: the budget is READ before a run starts, an
// unreadable budget reports nothing rather than a placeholder, and an exhausted budget refuses to
// start rather than spending into a wall.

// fakeApify serves just enough of the Apify API: the limits endpoint with the figures under test,
// and a failing actor-run endpoint so a launched run gives up immediately instead of reaching out
// to the real network.
func fakeApify(t *testing.T, used, limit float64, limitsStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/limits", func(w http.ResponseWriter, _ *http.Request) {
		if limitsStatus != http.StatusOK {
			w.WriteHeader(limitsStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"current":{"monthlyUsageUsd":%g},"limits":{"maxMonthlyUsageUsd":%g}}}`, used, limit)
	})
	// Anything else (the actor start a launched run attempts) fails fast and locally.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"not in this test"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A server wired for REAL runs against the fake Apify. No auth verifier, so the admin decision comes
// from the request body — which is the local/dev path, and lets the test reach the guard without
// minting tokens.
func budgetServer(t *testing.T, api *httptest.Server, minBudget float64) *server {
	t.Helper()
	return &server{
		base:         "/",
		realReady:    true,
		apify:        apify.New("tkn", apify.WithBaseURL(api.URL)),
		minBudgetUSD: minBudget,
		jobs:         map[string]*jobState{},
		demoRuns:     map[string]time.Time{},
	}
}

func postRun(t *testing.T, s *server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{"role":"admin","field":"swe"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.run(w, req)
	return w
}

func TestBudgetGuardRefusesAnExhaustedAccount(t *testing.T) {
	// The state the account was actually in when Apify sent the limit email.
	api := fakeApify(t, 4.9998, 5.00, http.StatusOK)
	s := budgetServer(t, api, defaultMinBudgetUSD)

	w := postRun(t, s)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 — a scan with no budget must not start", w.Code)
	}
	body := w.Body.String()
	// The message has to carry the numbers. "Budget exhausted" alone leaves the reader unable to
	// tell a genuinely spent cap from a misconfigured threshold. The REMAINDER especially: at two
	// decimal places $4.9998 prints as the cap itself, so "$5.00 of $5.00" alone would not
	// distinguish "spent to the penny" from "threshold set too high".
	for _, want := range []string{"$5.00 of the $5.00", "leaving $0.00", "at least $0.10"} {
		if !strings.Contains(body, want) {
			t.Errorf("message %q does not contain %q", body, want)
		}
	}
	if len(s.jobs) != 0 {
		t.Errorf("a refused run still created %d job(s)", len(s.jobs))
	}
}

func TestBudgetGuardAllowsAndSeedsFromTheRealReading(t *testing.T) {
	api := fakeApify(t, 1.25, 5.00, http.StatusOK)
	s := budgetServer(t, api, defaultMinBudgetUSD)

	w := postRun(t, s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200 — there is budget left", w.Code, strings.TrimSpace(w.Body.String()))
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || started.ID == "" {
		t.Fatalf("no job id in %s", w.Body.String())
	}

	s.jobsMu.Lock()
	j := s.jobs[started.ID]
	s.jobsMu.Unlock()
	if j == nil {
		t.Fatal("job was not registered")
	}
	snap := j.snapshot()
	rate, _ := snap["rate"].(map[string]float64)
	// Seeded from the reading taken BEFORE the run, which is the whole point: a run that dies
	// mid-flight now still shows a real figure rather than a placeholder.
	if rate["used"] != 1.25 || rate["limit"] != 5.00 {
		t.Errorf("rate = %+v, want the measured 1.25/5.00", rate)
	}
	// The specific number that must never come back.
	if rate["used"] == mockBudgetUsedUSD {
		t.Error("a real run is showing the mock's invented baseline")
	}
}

func TestBudgetGuardFailsOpenWhenApifyCannotBeRead(t *testing.T) {
	// An Apify outage must not become an outage of the scan button. The run proceeds, and reports
	// no budget rather than a made-up one.
	api := fakeApify(t, 0, 0, http.StatusInternalServerError)
	s := budgetServer(t, api, defaultMinBudgetUSD)

	w := postRun(t, s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200 — an unreadable budget must not block the scan",
			w.Code, strings.TrimSpace(w.Body.String()))
	}
	var started struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &started)
	s.jobsMu.Lock()
	j := s.jobs[started.ID]
	s.jobsMu.Unlock()
	if j == nil {
		t.Fatal("job was not registered")
	}
	rate, _ := j.snapshot()["rate"].(map[string]float64)
	// Zero limit is the "unknown" signal the client renders as a dash.
	if rate["limit"] != 0 {
		t.Errorf("limit = %v, want 0 so the strip shows a dash instead of a number", rate["limit"])
	}
}

func TestBudgetGuardThresholdIsTheRemainder(t *testing.T) {
	// The guard compares REMAINING against the threshold, not total spend — a larger cap with room
	// left must run even though more has been spent in absolute terms.
	api := fakeApify(t, 48.00, 50.00, http.StatusOK)
	s := budgetServer(t, api, defaultMinBudgetUSD)
	if w := postRun(t, s); w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — $2.00 remains, well over the $%.2f threshold",
			w.Code, defaultMinBudgetUSD)
	}
}
