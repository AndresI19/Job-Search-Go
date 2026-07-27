package judge

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/secret"
)

// APIJudge evaluates by calling the Anthropic Messages API directly with an API
// key. It is metered per token rather than drawing on a Claude subscription;
// this is the in-cluster backend, where a pod has no Claude Code login for the
// CLI judge to reuse. Local dev keeps using the CLI judge.
type APIJudge struct {
	client anthropic.Client
	model  anthropic.Model
}

// NewAPIJudge returns an APIJudge for the given model id. The API key is resolved
// via secret.Value: ANTHROPIC_API_KEY env first (local), else the mounted file
// (ANTHROPIC_API_KEY_FILE, default /etc/.secrets/anthropic-api-key) — so
// in-cluster the key is read from a read-only secret mount and never placed in the
// process environment. When resolved from a file it is passed explicitly with
// option.WithAPIKey; when it comes from the env the SDK's own default picks it up.
func NewAPIJudge(modelID string) (*APIJudge, error) {
	if modelID == "" {
		modelID = defaultModel
	}
	var opts []option.RequestOption
	if key := secret.Value("ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_FILE", "/etc/.secrets/anthropic-api-key"); key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	return &APIJudge{
		client: anthropic.NewClient(opts...),
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
