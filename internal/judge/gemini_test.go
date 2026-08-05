package judge

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// newTestGeminiJudge points a GeminiJudge at an httptest server so no real network (or
// key) is needed. handler receives the decoded request body for assertions.
func newTestGeminiJudge(t *testing.T, handler func(t *testing.T, req geminiRequest, r *http.Request) (int, string)) *GeminiJudge {
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
	return &GeminiJudge{
		http:    srv.Client(),
		baseURL: srv.URL + "/v1beta/models/",
		model:   defaultGeminiModel,
		apiKey:  "test-key",
	}
}

// geminiCandidateJSON nests the model's text (itself JSON) where Gemini puts it:
// candidates[0].content.parts[0].text.
func geminiCandidateJSON(obj string) string {
	b, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": obj}}}}},
	})
	return string(b)
}

func sampleInput() Input {
	return Input{
		Listing:    model.Listing{Title: "Backend Eng", Company: "Acme", Location: "Remote", ApplicantCount: 12},
		ATSChecked: true,
	}
}

func TestGeminiJudgeHappyPath(t *testing.T) {
	j := newTestGeminiJudge(t, func(t *testing.T, req geminiRequest, r *http.Request) (int, string) {
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
		if len(req.Contents) == 0 || len(req.Contents[0].Parts) == 0 || !strings.Contains(req.Contents[0].Parts[0].Text, "Backend Eng") {
			t.Errorf("prompt missing the listing title")
		}
		return 200, geminiCandidateJSON(`{"matched":true,"verdict":"likely-real","confidence":0.9,"reasoning":"solid role, active board match"}`)
	})

	got, err := j.Evaluate(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Confidence != model.LikelyReal {
		t.Errorf("Confidence = %q, want likely-real", got.Confidence)
	}
	// verdict likely-real @ certainty 0.9 → 0.5 + 0.5*0.9 = 0.95 legitimacy.
	if math.Abs(got.Score-0.95) > 1e-9 {
		t.Errorf("Score = %v, want 0.95", got.Score)
	}
	if got.Reasoning != "solid role, active board match" {
		t.Errorf("Reasoning = %q", got.Reasoning)
	}
}

// A confident ghost verdict must score LOW, not high — the direction comes from the
// verdict, the distance from the confidence (guards the score/confidence inversion).
func TestGeminiJudgeConfidentGhostScoresLow(t *testing.T) {
	j := newTestGeminiJudge(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
		return 200, geminiCandidateJSON(`{"matched":false,"verdict":"likely-ghost","confidence":0.8,"reasoning":"no board req"}`)
	})
	got, err := j.Evaluate(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Confidence != model.LikelyGhost {
		t.Errorf("Confidence = %q, want likely-ghost", got.Confidence)
	}
	if math.Abs(got.Score-0.1) > 1e-9 { // 0.5 - 0.5*0.8
		t.Errorf("Score = %v, want 0.1", got.Score)
	}
}

// A model that wraps its JSON in stray prose must still parse (extractJSONObject).
func TestGeminiJudgeToleratesProse(t *testing.T) {
	j := newTestGeminiJudge(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
		return 200, geminiCandidateJSON("Here is my verdict:\n{\"matched\":false,\"verdict\":\"uncertain\",\"confidence\":0.4,\"reasoning\":\"thin signals\"}\nDone.")
	})
	got, err := j.Evaluate(context.Background(), sampleInput())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Confidence != model.Uncertain {
		t.Errorf("Confidence = %q, want uncertain", got.Confidence)
	}
	if math.Abs(got.Score-0.5) > 1e-9 { // uncertain pins to the 0.5 midpoint
		t.Errorf("Score = %v, want 0.5", got.Score)
	}
}

func TestGeminiJudgeErrors(t *testing.T) {
	t.Run("http error surfaces message", func(t *testing.T) {
		j := newTestGeminiJudge(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
			return 429, `{"error":{"message":"quota exceeded"}}`
		})
		_, err := j.Evaluate(context.Background(), sampleInput())
		if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
			t.Errorf("err = %v, want quota exceeded", err)
		}
	})
	t.Run("safety block", func(t *testing.T) {
		j := newTestGeminiJudge(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
			return 200, `{"promptFeedback":{"blockReason":"SAFETY"}}`
		})
		_, err := j.Evaluate(context.Background(), sampleInput())
		if err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("err = %v, want blocked", err)
		}
	})
	t.Run("empty candidates", func(t *testing.T) {
		j := newTestGeminiJudge(t, func(t *testing.T, _ geminiRequest, _ *http.Request) (int, string) {
			return 200, `{"candidates":[]}`
		})
		_, err := j.Evaluate(context.Background(), sampleInput())
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("err = %v, want empty", err)
		}
	})
	t.Run("missing key short-circuits", func(t *testing.T) {
		j := &GeminiJudge{http: http.DefaultClient, baseURL: geminiEndpoint, model: defaultGeminiModel}
		_, err := j.Evaluate(context.Background(), sampleInput())
		if err == nil || !strings.Contains(err.Error(), "no API key") {
			t.Errorf("err = %v, want no API key", err)
		}
	})
}

// NewGeminiJudge must coerce a non-Gemini model id (e.g. the Claude default that FromEnv
// passes through via JUDGE_MODEL) to the Gemini free-tier default.
func TestGeminiJudgeModelCoercion(t *testing.T) {
	for _, in := range []string{"", "claude-haiku-4-5", "gpt-4o"} {
		if got := NewGeminiJudge(in).model; got != defaultGeminiModel {
			t.Errorf("NewGeminiJudge(%q).model = %q, want %q", in, got, defaultGeminiModel)
		}
	}
	if got := NewGeminiJudge("gemini-1.5-flash").model; got != "gemini-1.5-flash" {
		t.Errorf("explicit gemini model not preserved: %q", got)
	}
}
