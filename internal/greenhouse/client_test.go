package greenhouse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sampleBoard is a trimmed Greenhouse /jobs?content=true response. The content
// field is entity-encoded HTML, exactly as the real API returns it.
const sampleBoard = `{
  "jobs": [
    {
      "id": 4012345,
      "title": "Senior Software Engineer, Platform",
      "absolute_url": "https://boards.greenhouse.io/acme/jobs/4012345",
      "updated_at": "2026-01-15T10:30:00-05:00",
      "location": { "name": "Remote - US" },
      "content": "&lt;p&gt;Build &amp;amp; ship.&lt;/p&gt;&lt;ul&gt;&lt;li&gt;Go&lt;/li&gt;&lt;/ul&gt;"
    },
    {
      "id": 4012346,
      "title": "Product Designer",
      "absolute_url": "https://boards.greenhouse.io/acme/jobs/4012346",
      "updated_at": "2026-02-01T09:00:00Z",
      "location": { "name": "New York, NY" },
      "content": "&lt;p&gt;Design things.&lt;/p&gt;"
    }
  ],
  "meta": { "total": 2 }
}`

func newTestClient(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/boards/acme/jobs"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if r.URL.Query().Get("content") != "true" {
			t.Errorf("expected content=true, got %q", r.URL.RawQuery)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestFetchMapsBoardToListings(t *testing.T) {
	c := newTestClient(t, http.StatusOK, sampleBoard)
	got, err := c.Fetch(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	eng := got[0]
	if eng.Source != "greenhouse" || eng.JobID != "4012345" {
		t.Errorf("source/id = %q/%q", eng.Source, eng.JobID)
	}
	if eng.Company != "acme" || eng.CompanyURL != "acme" {
		t.Errorf("company join key = %q/%q, want acme/acme", eng.Company, eng.CompanyURL)
	}
	if eng.URL != "https://boards.greenhouse.io/acme/jobs/4012345" {
		t.Errorf("url = %q", eng.URL)
	}
	if !eng.Remote {
		t.Errorf("Remote = false for location %q, want true", eng.Location)
	}
	if eng.ApplicantCount != -1 {
		t.Errorf("ApplicantCount = %d, want -1 (unknown for ATS)", eng.ApplicantCount)
	}
	// content is double-entity-encoded HTML; expect clean plain text.
	if want := "Build & ship. Go"; eng.Description != want {
		t.Errorf("Description = %q, want %q", eng.Description, want)
	}
	if !eng.Posted.Equal(time.Date(2026, 1, 15, 15, 30, 0, 0, time.UTC)) {
		t.Errorf("Posted = %v, want 2026-01-15T15:30:00Z", eng.Posted.UTC())
	}

	if got[1].Remote {
		t.Errorf("Product Designer in New York should not be Remote")
	}
}

func TestFetchErrors(t *testing.T) {
	t.Run("missing board is an error", func(t *testing.T) {
		c := newTestClient(t, http.StatusNotFound, `{"error":"not found"}`)
		if _, err := c.Fetch(context.Background(), "acme"); err == nil {
			t.Fatal("expected error for 404, got nil")
		}
	})
	t.Run("empty token is rejected before any request", func(t *testing.T) {
		if _, err := New().Fetch(context.Background(), "  "); err == nil {
			t.Fatal("expected error for empty token")
		}
	})
}

func TestName(t *testing.T) {
	if New().Name() != "greenhouse" {
		t.Errorf("Name() = %q", New().Name())
	}
}
