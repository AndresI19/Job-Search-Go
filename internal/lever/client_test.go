package lever

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// samplePostings is a trimmed Lever /postings?mode=json response: a top-level
// JSON array. The first uses the plain-text description and the explicit
// workplaceType; the second has no plain text, forcing the HTML fallback.
const samplePostings = `[
  {
    "id": "abc-123",
    "text": "Backend Engineer",
    "hostedUrl": "https://jobs.lever.co/acme/abc-123",
    "createdAt": 1768000000000,
    "categories": { "location": "San Francisco" },
    "workplaceType": "remote",
    "descriptionPlain": "Write   Go services.",
    "description": "<p>Write Go services.</p>"
  },
  {
    "id": "def-456",
    "text": "Recruiter",
    "hostedUrl": "https://jobs.lever.co/acme/def-456",
    "createdAt": 1769000000000,
    "categories": { "location": "New York" },
    "descriptionPlain": "",
    "description": "&lt;p&gt;Hire &amp;amp; grow.&lt;/p&gt;"
  }
]`

func newTestClient(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/postings/acme"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if r.URL.Query().Get("mode") != "json" {
			t.Errorf("expected mode=json, got %q", r.URL.RawQuery)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestFetchMapsPostingsToListings(t *testing.T) {
	c := newTestClient(t, http.StatusOK, samplePostings)
	got, err := c.Fetch(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2", len(got))
	}

	eng := got[0]
	if eng.Source != "lever" || eng.JobID != "abc-123" {
		t.Errorf("source/id = %q/%q", eng.Source, eng.JobID)
	}
	if eng.Company != "acme" || eng.CompanyURL != "acme" {
		t.Errorf("company join key = %q/%q, want acme/acme", eng.Company, eng.CompanyURL)
	}
	if !eng.Remote {
		t.Errorf("Remote = false, want true (workplaceType=remote)")
	}
	if eng.ApplicantCount != -1 {
		t.Errorf("ApplicantCount = %d, want -1", eng.ApplicantCount)
	}
	// plain-text field is preferred, with whitespace collapsed.
	if want := "Write Go services."; eng.Description != want {
		t.Errorf("Description = %q, want %q", eng.Description, want)
	}
	if !eng.Posted.Equal(time.UnixMilli(1768000000000).UTC()) {
		t.Errorf("Posted = %v", eng.Posted)
	}

	rec := got[1]
	if rec.Remote {
		t.Errorf("Recruiter in New York should not be Remote")
	}
	// no plain text -> HTML fallback, decoded from double-entity-encoded content.
	if want := "Hire & grow."; rec.Description != want {
		t.Errorf("fallback Description = %q, want %q", rec.Description, want)
	}
}

func TestFetchErrors(t *testing.T) {
	t.Run("missing board is an error", func(t *testing.T) {
		c := newTestClient(t, http.StatusNotFound, `{"error":"not found"}`)
		if _, err := c.Fetch(context.Background(), "acme"); err == nil {
			t.Fatal("expected error for 404, got nil")
		}
	})
	t.Run("empty handle is rejected before any request", func(t *testing.T) {
		if _, err := New().Fetch(context.Background(), "  "); err == nil {
			t.Fatal("expected error for empty handle")
		}
	})
}

func TestName(t *testing.T) {
	if New().Name() != "lever" {
		t.Errorf("Name() = %q", New().Name())
	}
}
