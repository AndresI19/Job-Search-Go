package summarize

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/secret"
)

// APISummarizer summarizes by calling the Anthropic Messages API with a key — the
// in-cluster backend, where a pod has no Claude Code login for the CLI to reuse.
// The key is resolved via secret.Value: ANTHROPIC_API_KEY env first (local), else
// the mounted file (default /etc/.secrets/anthropic-api-key), so in-cluster it is
// read from a read-only secret mount and never placed in the process environment.
type APISummarizer struct {
	client anthropic.Client
	model  anthropic.Model
}

func NewAPISummarizer(modelID string) *APISummarizer {
	if modelID == "" {
		modelID = defaultModel
	}
	var opts []option.RequestOption
	if key := secret.Value("ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE", "/etc/.secrets/anthropic-api-key"); key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	return &APISummarizer{client: anthropic.NewClient(opts...), model: anthropic.Model(modelID)}
}

func (s *APISummarizer) Summarize(ctx context.Context, l model.Listing) (Summary, error) {
	prompt := buildPrompt(l) + "\n\nReturn ONLY a JSON object matching this schema, no prose:\n" + summarySchema
	msg, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     s.model,
		MaxTokens: 1024,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))},
	})
	if err != nil {
		return Summary{}, fmt.Errorf("anthropic api: %w", err)
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	raw, err := parseSummary([]byte(extractJSONObject(text.String())))
	if err != nil {
		return Summary{}, err
	}
	return raw.toModel(), nil
}

// extractJSONObject returns the substring from the first '{' to the last '}',
// tolerating stray prose around the JSON object.
func extractJSONObject(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}
