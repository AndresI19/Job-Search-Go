// Package judge evaluates a job listing's legitimacy with Claude, returning a
// model.Verdict. It offers two interchangeable backends behind one interface:
//
//   - CLIJudge shells out to the `claude` command in headless mode, reusing an
//     existing Claude Code login (e.g. a Pro/Max subscription) — no API key.
//   - APIJudge calls the Anthropic API directly with an API key.
//
// Pick one with FromEnv. Wrap either with Bounded to cap concurrency.
package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

const defaultModel = "claude-haiku-4-5"

// Input is what the judge evaluates: a scraped listing plus candidate ATS
// requisitions to match it against.
type Input struct {
	Listing    model.Listing
	Candidates []model.Listing
}

// Judge evaluates a listing's legitimacy and returns a Verdict. Implementations
// must honor ctx cancellation.
type Judge interface {
	Evaluate(ctx context.Context, in Input) (model.Verdict, error)
}

// verdictSchema is the JSON shape both backends ask Claude to return. Coverage
// and VerifiedVia on model.Verdict are filled by the pipeline, not the judge.
const verdictSchema = `{"type":"object","properties":{"matched":{"type":"boolean"},"confidence":{"type":"number"},"verdict":{"type":"string","enum":["likely-real","uncertain","likely-ghost"]},"reasoning":{"type":"string"}},"required":["matched","verdict","reasoning"]}`

// rawVerdict is the JSON both backends decode before mapping to model.Verdict.
type rawVerdict struct {
	Matched    bool    `json:"matched"`
	Confidence float64 `json:"confidence"`
	Verdict    string  `json:"verdict"`
	Reasoning  string  `json:"reasoning"`
}

func (r rawVerdict) toModel() model.Verdict {
	return model.Verdict{
		Confidence: model.Confidence(r.Verdict),
		Score:      r.Confidence,
		Reasoning:  r.Reasoning,
	}
}

func parseVerdict(b []byte) (rawVerdict, error) {
	var r rawVerdict
	if err := json.Unmarshal(b, &r); err != nil {
		return rawVerdict{}, fmt.Errorf("decode verdict: %w", err)
	}
	return r, nil
}

// buildPrompt renders the judging instruction for a listing + candidates.
func buildPrompt(in Input) string {
	var b strings.Builder
	b.WriteString("Decide whether a job listing is a legitimate, active posting or a likely ghost job.\n\n")
	fmt.Fprintf(&b, "LISTING\n  title: %s\n  company: %s\n  location: %s\n  applicants: %d\n  apply_type: %s\n\n",
		in.Listing.Title, in.Listing.Company, in.Listing.Location, in.Listing.ApplicantCount, in.Listing.ApplyType)
	if len(in.Candidates) == 0 {
		b.WriteString("No matching requisition was found on the company's ATS.\n")
	} else {
		b.WriteString("CANDIDATE ATS REQUISITIONS\n")
		for _, c := range in.Candidates {
			fmt.Fprintf(&b, "  - %s (%s)\n", c.Title, c.Location)
		}
	}
	b.WriteString("\nSet matched=true only if the listing clearly corresponds to one of the candidate requisitions. Keep reasoning to one or two sentences.")
	return b.String()
}

// FromEnv builds a Judge from configuration:
//
//	JUDGE_BACKEND      "cli" (default) or "api"
//	JUDGE_MODEL        model id (default claude-haiku-4-5)
//	JUDGE_CONCURRENCY  max concurrent evaluations (default: cli=3, api=16)
func FromEnv() (Judge, error) {
	backend := strings.ToLower(envOr("JUDGE_BACKEND", "cli"))
	modelID := envOr("JUDGE_MODEL", defaultModel)

	var (
		inner      Judge
		err        error
		defaultLim int
	)
	switch backend {
	case "cli":
		inner = NewCLIJudge(modelID)
		defaultLim = 3
	case "api":
		inner, err = NewAPIJudge(modelID)
		defaultLim = 16
	default:
		return nil, fmt.Errorf("unknown JUDGE_BACKEND %q (want \"cli\" or \"api\")", backend)
	}
	if err != nil {
		return nil, err
	}

	lim := defaultLim
	if v := os.Getenv("JUDGE_CONCURRENCY"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			lim = n
		}
	}
	return Bounded(inner, lim), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
