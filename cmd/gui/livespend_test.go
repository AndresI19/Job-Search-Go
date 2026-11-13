package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startRun posts a run as the given role and returns the registered job.
func startRun(t *testing.T, s *server, role string) *jobState {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{"role":"`+role+`","field":"swe"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.run(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s run: status = %d (%s), want 200", role, w.Code, strings.TrimSpace(w.Body.String()))
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil || started.ID == "" {
		t.Fatalf("%s run: no job id in %s", role, w.Body.String())
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	j := s.jobs[started.ID]
	if j == nil {
		t.Fatalf("%s run: job %s was not registered", role, started.ID)
	}
	return j
}

// The live spend display costs an Apify API call per tick and shows the whole account's spend, so
// only an admin's real run gets it. A signed-in non-admin's run is just as real — and keeps the
// existing behaviour: measured before the run and again on every exit.
func TestLiveSpendIsAdminOnly(t *testing.T) {
	api := fakeApify(t, 1.25, 5.00, http.StatusOK)

	if j := startRun(t, budgetServer(t, api, defaultMinBudgetUSD), "admin"); !j.liveSpend {
		t.Error("admin real run: liveSpend = false, want true")
	}

	t.Setenv("DEV_USER_ID", "user-1") // signed in locally, but not an admin
	if j := startRun(t, budgetServer(t, api, defaultMinBudgetUSD), "user"); j.liveSpend {
		t.Error("signed-in non-admin real run: liveSpend = true, want false")
	}
}

func TestWatchSpendRefreshesUntilStopped(t *testing.T) {
	var calls atomic.Int32
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		watchSpend(stop, 2*time.Millisecond, func() { calls.Add(1) })
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("refresh called %d times in 2s, want it to keep firing while the run is live", calls.Load())
		case <-time.After(time.Millisecond):
		}
	}

	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchSpend did not return after stop was closed — it would outlive the run")
	}
	after := calls.Load()
	time.Sleep(20 * time.Millisecond)
	if n := calls.Load(); n != after {
		t.Errorf("refresh called %d more time(s) after stop", n-after)
	}
}
