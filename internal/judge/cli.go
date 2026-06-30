package judge

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

// CLIJudge evaluates by shelling out to the `claude` CLI in headless mode,
// reusing an existing Claude Code login (e.g. a Pro/Max subscription) so no
// Anthropic API key is required. Each call spawns a claude process, so callers
// should bound concurrency (see Bounded).
type CLIJudge struct {
	bin   string // claude binary (PATH name or absolute path)
	model string // value for --model
}

// NewCLIJudge returns a CLIJudge that runs `claude` from PATH with the given
// model (defaulting to a cheap model to limit subscription-usage burn).
func NewCLIJudge(model string) *CLIJudge {
	if model == "" {
		model = defaultModel
	}
	return &CLIJudge{bin: "claude", model: model}
}

// cliEnvelope is the JSON `claude -p --output-format json` prints. With
// --json-schema, the schema-validated object lands in StructuredOutput.
type cliEnvelope struct {
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

func (j *CLIJudge) Evaluate(ctx context.Context, in Input) (model.Verdict, error) {
	args := []string{
		"-p", buildPrompt(in),
		"--output-format", "json",
		"--json-schema", verdictSchema,
	}
	if j.model != "" {
		args = append(args, "--model", j.model)
	}
	cmd := exec.CommandContext(ctx, j.bin, args...)
	// Force subscription auth: a present ANTHROPIC_API_KEY would switch the CLI
	// to metered API billing. (Do NOT add --bare; it also drops the login.)
	cmd.Env = envWithout(os.Environ(), "ANTHROPIC_API_KEY")

	out, err := cmd.Output()
	if err != nil {
		return model.Verdict{}, fmt.Errorf("claude cli: %w", withStderr(err))
	}

	var env cliEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return model.Verdict{}, fmt.Errorf("decode cli envelope: %w", err)
	}
	if env.IsError {
		return model.Verdict{}, fmt.Errorf("claude cli error: %s", strings.TrimSpace(env.Result))
	}

	payload := []byte(env.Result)
	if len(env.StructuredOutput) > 0 {
		payload = env.StructuredOutput
	}
	raw, err := parseVerdict(payload)
	if err != nil {
		return model.Verdict{}, err
	}
	return raw.toModel(), nil
}

// withStderr enriches an exec error with the process's stderr, if any.
func withStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

// envWithout returns env with the named variables removed.
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
