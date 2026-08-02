package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// CLISummarizer summarizes by shelling out to the `claude` CLI in headless mode,
// reusing an existing Claude Code login (a subscription) so no API key is needed.
// Each call spawns a process, so callers should bound concurrency (see Bounded).
type CLISummarizer struct {
	bin   string
	model string
}

func NewCLISummarizer(model string) *CLISummarizer {
	if model == "" {
		model = defaultModel
	}
	return &CLISummarizer{bin: "claude", model: model}
}

// cliEnvelope is the JSON `claude -p --output-format json` prints; with
// --json-schema the schema-validated object lands in StructuredOutput.
type cliEnvelope struct {
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

func (s *CLISummarizer) Summarize(ctx context.Context, l model.Listing) (Summary, error) {
	args := []string{"-p", buildPrompt(l), "--output-format", "json", "--json-schema", summarySchema}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	cmd := exec.CommandContext(ctx, s.bin, args...)
	// Force subscription auth: a present ANTHROPIC_API_KEY would switch the CLI to
	// metered API billing.
	cmd.Env = envWithout(os.Environ(), "ANTHROPIC_API_KEY")

	out, err := cmd.Output()
	if err != nil {
		return Summary{}, fmt.Errorf("claude cli: %w", withStderr(err))
	}
	var env cliEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return Summary{}, fmt.Errorf("decode cli envelope: %w", err)
	}
	if env.IsError {
		return Summary{}, fmt.Errorf("claude cli error: %s", strings.TrimSpace(env.Result))
	}
	payload := []byte(env.Result)
	if len(env.StructuredOutput) > 0 {
		payload = env.StructuredOutput
	}
	raw, err := parseSummary(payload)
	if err != nil {
		return Summary{}, err
	}
	return raw.toModel(), nil
}

func withStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

func envWithout(env []string, drop ...string) []string {
	skip := make(map[string]bool, len(drop))
	for _, d := range drop {
		skip[d] = true
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if !skip[name] {
			out = append(out, e)
		}
	}
	return out
}
