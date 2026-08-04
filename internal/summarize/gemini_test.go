package summarize

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// newTestGemini points a GeminiSummarizer at an httptest server so no real network
// (or key) is needed. handler receives the decoded request body for assertions.
func newTestGemini(t *testing.T, handler func(t *testing.T, req geminiRequest, r *http.Request) (int, string)) *GeminiSummarizer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req geminiRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		status, resp := handler(t, req, r)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return &GeminiSummarizer{
		http:    srv.Client(),
		baseURL: srv.URL + "/v1beta/models/",
		model:   defaultGeminiModel,
		apiKey:  "test-key",
	}
}

func candidateJSON(obj string) string {
	// Gemini nests the model's text (itself JSON) inside candidates[0].content.parts[0].text.
	b, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": obj}}}}},
	})
	return string(b)
}

func TestGeminiSummarizeHappyPath(t *testing.T) {
	s := newTestGemini(t, func(t *testing.T, req geminiRequest, r *http.Request) (int, string) {
		// Request shape: model in the path, key in the query, JSON mode + temp 0.
		if !strings.Contains(r.URL.Path, defaultGeminiModel+":generateContent") {
			t.Errorf("path = %q, want model:generateContent", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("key query = %q, want test-key", r.URL.Query().Get("key"))
		}
		if req.GenerationConfig.ResponseMIMEType != "application/json" {
			t.Errorf("responseMimeType = %q, want application/json", req.GenerationConfig.ResponseMIMEType)
		}
		if req.GenerationConfig.Temperature != 0 {
			t.Errorf("temperature = %v, want 0", req.GenerationConfig.Temperature)
		}
		if len(req.Contents) == 0 || len(req.Contents[0].Parts) == 0 || !strings.Contains(req.Contents[0].Parts[0].Text, "DESCRIPTION:") {
			t.Errorf("prompt missing the listing description")
		}
		return 200, candidateJSON(`{"required":"5+ yrs Go","preferred":"k8s","role":"build APIs","company":"widgets","employment":"permanent","pay_note":"$160k-$190k/yr"}`)
	})

	got, err := s.Summarize(context.Background(), model.Listing{Title: "Backend Eng", Company: "Acme", Description: "We need Go."})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got.Required != "5+ yrs Go" || got.Preferred != "k8s" || got.Employment != "permanent" || got.PayNote != "$160k-$190k/yr" {
		t.Errorf("summary = %+v", got)
	}
}

// A model that wraps its JSON in stray prose must still parse (extractJSONObject).
func TestGeminiSummarizeToleratesProse(t *testing.T) {
	s := newTestGemini(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
		return 200, candidateJSON("Here you go:\n{\"required\":\"3 yrs\",\"role\":\"ops\"}\nHope that helps!")
	})
	got, err := s.Summarize(context.Background(), model.Listing{Description: "x"})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got.Required != "3 yrs" || got.Role != "ops" {
		t.Errorf("summary = %+v", got)
	}
	// Unspecified fields fall back to their defaults via toModel.
	if got.Preferred != "None stated" || got.Employment != "unclear" {
		t.Errorf("defaults not applied: %+v", got)
	}
}

func TestGeminiSummarizeErrors(t *testing.T) {
	t.Run("http error surfaces message", func(t *testing.T) {
		s := newTestGemini(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
			return 429, `{"error":{"message":"quota exceeded"}}`
		})
		_, err := s.Summarize(context.Background(), model.Listing{Description: "x"})
		if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
			t.Errorf("err = %v, want quota exceeded", err)
		}
	})
	t.Run("safety block", func(t *testing.T) {
		s := newTestGemini(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
			return 200, `{"promptFeedback":{"blockReason":"SAFETY"}}`
		})
		_, err := s.Summarize(context.Background(), model.Listing{Description: "x"})
		if err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("err = %v, want blocked", err)
		}
	})
	t.Run("empty candidates", func(t *testing.T) {
		s := newTestGemini(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
			return 200, `{"candidates":[]}`
		})
		_, err := s.Summarize(context.Background(), model.Listing{Description: "x"})
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("err = %v, want empty", err)
		}
	})
	t.Run("missing key short-circuits", func(t *testing.T) {
		s := &GeminiSummarizer{http: http.DefaultClient, baseURL: geminiEndpoint, model: defaultGeminiModel}
		_, err := s.Summarize(context.Background(), model.Listing{Description: "x"})
		if err == nil || !strings.Contains(err.Error(), "no API key") {
			t.Errorf("err = %v, want no API key", err)
		}
	})
}

// NewGeminiSummarizer must coerce a non-Gemini model id (e.g. the Claude default that
// FromEnv passes through via JUDGE_MODEL) to the Gemini default.
func TestGeminiModelCoercion(t *testing.T) {
	for _, in := range []string{"", "claude-haiku-4-5", "gpt-4o"} {
		if got := NewGeminiSummarizer(in).model; got != defaultGeminiModel {
			t.Errorf("NewGeminiSummarizer(%q).model = %q, want %q", in, got, defaultGeminiModel)
		}
	}
	if got := NewGeminiSummarizer("gemini-1.5-flash").model; got != "gemini-1.5-flash" {
		t.Errorf("explicit gemini model not preserved: %q", got)
	}
}
