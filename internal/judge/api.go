package judge

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// APIJudge evaluates by calling the Anthropic Messages API directly with an API
// key (ANTHROPIC_API_KEY). It is metered per token rather than drawing on a
// Claude subscription; prefer it for higher-throughput runs.
type APIJudge struct {
	client anthropic.Client
	model  anthropic.Model
}

// NewAPIJudge returns an APIJudge for the given model id, reading
// ANTHROPIC_API_KEY from the environment.
func NewAPIJudge(modelID string) (*APIJudge, error) {
	if modelID == "" {
		modelID = defaultModel
	}
	return &APIJudge{
		client: anthropic.NewClient(),
		model:  anthropic.Model(modelID),
	}, nil
}

func (j *APIJudge) Evaluate(ctx context.Context, in Input) (model.Verdict, error) {
	prompt := buildPrompt(in) + "\n\nReturn ONLY a JSON object matching this schema, no prose:\n" + verdictSchema
	msg, err := j.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     j.model,
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return model.Verdict{}, fmt.Errorf("anthropic api: %w", err)
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	raw, err := parseVerdict([]byte(extractJSONObject(text.String())))
	if err != nil {
		return model.Verdict{}, err
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
