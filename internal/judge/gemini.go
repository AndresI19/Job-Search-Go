package judge

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
// solid structured-JSON output, which is all this one-shot verdict needs. It is
// deliberately a "-latest" alias, not a pinned version: Google moves older pinned
// models (e.g. gemini-2.0-flash) OFF the free tier over time (limit: 0), so tracking
// the current lite flash keeps this backend free without periodic model bumps.
const defaultGeminiModel = "gemini-flash-lite-latest"

// geminiEndpoint is the Generative Language REST base; the model and API key are
// appended per request (…/models/<model>:generateContent?key=<key>).
const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/"

// GeminiJudge evaluates a listing by calling Google's Gemini API with a free-tier key
// — a keyed backend like APIJudge, but $0. It is what lets the whole service run on ONE
// model: with JUDGE_BACKEND=gemini (and SUMMARIZE_BACKEND inheriting it), both the
// verdict and the applicator summaries go to Gemini, so no Anthropic key is needed in
// the cluster. The key is resolved via secret.Value: GEMINI_API_KEY env first (local),
// else the mounted file (default /etc/.secrets/gemini-api-key), so in-cluster it is read
// from a read-only secret mount and never placed in the process environment. Uses only
// net/http, so it adds no SDK dependency. responseMimeType=application/json guarantees
// the reply is a bare JSON object matching the shared verdictSchema.
//
// The request/response plumbing intentionally mirrors summarize.GeminiSummarizer; each
// backend file stays self-contained (as APIJudge and CLIJudge do), rather than sharing a
// client, so its wire shape is testable in isolation via the baseURL field.
type GeminiJudge struct {
	http    *http.Client
	baseURL string // the …/models/ REST base; overridable in tests
	model   string
	apiKey  string
}

// NewGeminiJudge returns a GeminiJudge for the given model id.
func NewGeminiJudge(modelID string) *GeminiJudge {
	// FromEnv may hand us the Claude default (JUDGE_MODEL); ignore any non-Gemini id and
	// use the free-tier default so the backend is self-correcting.
	if modelID == "" || !strings.HasPrefix(modelID, "gemini") {
		modelID = defaultGeminiModel
	}
	return &GeminiJudge{
		http:    &http.Client{Timeout: 60 * time.Second},
		baseURL: geminiEndpoint,
		model:   modelID,
		apiKey:  secret.Value("GEMINI_API_KEY", "GEMINI_API_KEY_FILE", "/etc/.secrets/gemini-api-key"),
	}
}

// geminiRequest mirrors the generateContent body: one user turn, JSON-mode output,
// temperature 0 so the verdict is deterministic.
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

func (j *GeminiJudge) Evaluate(ctx context.Context, in Input) (model.Verdict, error) {
	if j.apiKey == "" {
		return model.Verdict{}, fmt.Errorf("gemini: no API key (set GEMINI_API_KEY or mount /etc/.secrets/gemini-api-key)")
	}
	prompt := buildPrompt(in) + "\n\nReturn ONLY a JSON object matching this schema, no prose:\n" + verdictSchema
	body, err := json.Marshal(geminiRequest{
		Contents:         []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
		GenerationConfig: geminiGenerationCfg{ResponseMIMEType: "application/json", Temperature: 0},
	})
	if err != nil {
		return model.Verdict{}, err
	}

	url := j.baseURL + j.model + ":generateContent?key=" + j.apiKey
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return model.Verdict{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := j.http.Do(req)
	if err != nil {
		return model.Verdict{}, fmt.Errorf("gemini: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var gr geminiResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return model.Verdict{}, fmt.Errorf("gemini: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := gr.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return model.Verdict{}, fmt.Errorf("gemini: http %d: %s", resp.StatusCode, msg)
	}
	if gr.PromptFeedback.BlockReason != "" {
		return model.Verdict{}, fmt.Errorf("gemini: prompt blocked (%s)", gr.PromptFeedback.BlockReason)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return model.Verdict{}, fmt.Errorf("gemini: empty response")
	}

	var text strings.Builder
	for _, p := range gr.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	rv, err := parseVerdict([]byte(extractJSONObject(text.String())))
	if err != nil {
		return model.Verdict{}, err
	}
	return rv.toModel(), nil
}
