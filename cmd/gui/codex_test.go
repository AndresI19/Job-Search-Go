package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/db"
)

// The Codex handler must be safe with no DB and no identity: an anonymous caller
// reads an empty list (guests keep templates in localStorage), and writes are refused
// with 401 rather than silently dropped.
func TestCodexHandlerAnon(t *testing.T) {
	t.Setenv("DEV_USER_ID", "") // force anonymous even if the env sets a dev user
	s := &server{}              // nil db + nil auth → userID "", nil-safe DB methods

	t.Run("GET returns empty", func(t *testing.T) {
		w := httptest.NewRecorder()
		s.codex(w, httptest.NewRequest(http.MethodGet, "/api/codex", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET status = %d, want 200", w.Code)
		}
		var got struct {
			Templates []db.Template `json:"templates"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Templates) != 0 {
			t.Errorf("templates = %d, want 0 for an anonymous caller", len(got.Templates))
		}
	})

	t.Run("POST without identity is 401", func(t *testing.T) {
		w := httptest.NewRecorder()
		body := strings.NewReader(`{"id":"t1","title":"Hi","body":"Dear {{COMPANY}}"}`)
		s.codex(w, httptest.NewRequest(http.MethodPost, "/api/codex", body))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("POST status = %d, want 401", w.Code)
		}
	})

	t.Run("DELETE without identity is 401", func(t *testing.T) {
		w := httptest.NewRecorder()
		s.codex(w, httptest.NewRequest(http.MethodDelete, "/api/codex?id=t1", nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("DELETE status = %d, want 401", w.Code)
		}
	})
}
