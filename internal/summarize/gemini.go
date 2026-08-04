package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/secret"
)

// defaultGeminiModel is Google's current free-tier lite-flash alias — sub-second,
// solid structured-JSON output, which is all this one-shot distillation needs. It is
// deliberately a "-latest" alias, not a pinned version: Google moves older pinned
// models (e.g. gemini-2.0-flash) OFF the free tier over time (limit: 0), so tracking
// the current lite flash keeps this backend free without periodic model bumps.
const defaultGeminiModel = "gemini-flash-lite-latest"

// geminiEndpoint is the Generative Language REST base; the model and API key are
// appended per request (…/models/<model>:generateContent?key=<key>).
const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/"

// GeminiSummarizer summarizes by calling Google's Gemini API with a free-tier key —
// a keyed backend like APISummarizer, but $0. The key is resolved via secret.Value:
// GEMINI_API_KEY env first (local), else the mounted file (default
// /etc/.secrets/gemini-api-key), so in-cluster it is read from a read-only secret
// mount and never placed in the process environment. Uses only net/http, so it adds
// no SDK dependency. Gemini's responseMimeType=application/json guarantees the reply
// is a bare JSON object matching the shared summary schema.
type GeminiSummarizer struct {
	http    *http.Client
	baseURL string // the …/models/ REST base; overridable in tests
	model   string
	apiKey  string
}

func NewGeminiSummarizer(modelID string) *GeminiSummarizer {
	// FromEnv may hand us the Claude default (via the JUDGE_MODEL fallthrough); ignore
	// any non-Gemini id and use the free-tier default so the backend is self-correcting.
	if modelID == "" || !strings.HasPrefix(modelID, "gemini") {
		modelID = defaultGeminiModel
	}
	return &GeminiSummarizer{
		http:    &http.Client{Timeout: 60 * time.Second},
		baseURL: geminiEndpoint,
		model:   modelID,
		apiKey:  secret.Value("GEMINI_API_KEY", "GEMINI_API_KEY_FILE", "/etc/.secrets/gemini-api-key"),
	}
}

// geminiRequest mirrors the generateContent body: one user turn, JSON-mode output,
// temperature 0 so the distillation is deterministic.
type geminiRequest struct {
	Contents         []geminiContent     `json:"contents"`
	GenerationConfig geminiGenerationCfg `json:"generationConfig"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationCfg struct {
	ResponseMIMEType string  `json:"responseMimeType"`
	Temperature      float64 `json:"temperature"`
}

// geminiResponse is the subset of the reply we read: the first candidate's text part
// (the JSON object), plus promptFeedback so a safety block yields a clear error.
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *GeminiSummarizer) Summarize(ctx context.Context, l model.Listing) (Summary, error) {
	if s.apiKey == "" {
		return Summary{}, fmt.Errorf("gemini: no API key (set GEMINI_API_KEY or mount /etc/.secrets/gemini-api-key)")
	}
	prompt := buildPrompt(l) + "\n\nReturn ONLY a JSON object matching this schema, no prose:\n" + summarySchema
	body, err := json.Marshal(geminiRequest{
		Contents:         []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
		GenerationConfig: geminiGenerationCfg{ResponseMIMEType: "application/json", Temperature: 0},
	})
	if err != nil {
		return Summary{}, err
	}

	url := s.baseURL + s.model + ":generateContent?key=" + s.apiKey
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Summary{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return Summary{}, fmt.Errorf("gemini: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var gr geminiResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return Summary{}, fmt.Errorf("gemini: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := gr.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return Summary{}, fmt.Errorf("gemini: http %d: %s", resp.StatusCode, msg)
	}
	if gr.PromptFeedback.BlockReason != "" {
		return Summary{}, fmt.Errorf("gemini: prompt blocked (%s)", gr.PromptFeedback.BlockReason)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return Summary{}, fmt.Errorf("gemini: empty response")
	}

	var text strings.Builder
	for _, p := range gr.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	parsed, err := parseSummary([]byte(extractJSONObject(text.String())))
	if err != nil {
		return Summary{}, err
	}
	return parsed.toModel(), nil
}
