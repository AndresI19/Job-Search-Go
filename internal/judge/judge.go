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
	// ATSChecked is true when the company's ATS board was located and read. When
	// false, the board could not be found at all (a coverage gap, not evidence),
	// which the prompt must present very differently from "checked, no match".
	ATSChecked bool
}

// Judge evaluates a listing's legitimacy and returns a Verdict. Implementations
// must honor ctx cancellation.
type Judge interface {
	Evaluate(ctx context.Context, in Input) (model.Verdict, error)
}

// verdictSchema is the JSON shape both backends ask Claude to return. Coverage
// and VerifiedVia on model.Verdict are filled by the pipeline, not the judge.
// confidence is certainty in the verdict, NOT a legitimacy score — the two are
// combined into a legitimacy score in toModel.
const verdictSchema = `{"type":"object","properties":{"matched":{"type":"boolean"},"verdict":{"type":"string","enum":["likely-real","uncertain","likely-ghost"]},"confidence":{"type":"number","description":"certainty in the verdict, 0.0 (unsure) to 1.0 (certain)"},"reasoning":{"type":"string"}},"required":["matched","verdict","confidence","reasoning"]}`

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
		Score:      legitimacyScore(r.Verdict, r.Confidence),
		Reasoning:  r.Reasoning,
	}
}

// legitimacyScore turns the model's categorical verdict and its certainty in
// that verdict into a 0..1 legitimacy score (0 = ghost, 1 = real). The verdict
// sets the direction; the certainty sets how far from the neutral 0.5 midpoint.
// This keeps the number aligned with the words — a confident likely-ghost scores
// LOW, not high — which reading the raw confidence as the score silently inverted.
func legitimacyScore(verdict string, certainty float64) float64 {
	switch {
	case certainty < 0:
		certainty = 0
	case certainty > 1:
		certainty = 1
	}
	switch model.Confidence(verdict) {
	case model.LikelyReal:
		return 0.5 + 0.5*certainty
	case model.LikelyGhost:
		return 0.5 - 0.5*certainty
	default: // uncertain, or an unrecognized verdict
		return 0.5
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
	// A board we located (or candidates we already have) is real ATS evidence; a
	// board we could not find is a coverage gap and must NOT read as a ghost.
	checked := in.ATSChecked || len(in.Candidates) > 0
	switch {
	case !checked:
		b.WriteString("The company's ATS board could not be located (it may use an applicant-tracking system this tool does not check). Treat this as NO information either way — judge only on the listing's own signals (company reputation, role specificity, applicant count, apply type, description quality). Do NOT treat missing ATS data as evidence of a ghost.\n")
	case len(in.Candidates) == 0:
		b.WriteString("The company's ATS board was checked and has NO open requisition matching this listing — a strong ghost signal.\n")
	default:
		b.WriteString("The company's ATS board was checked. Its open requisitions:\n")
		for _, c := range in.Candidates {
			fmt.Fprintf(&b, "  - %s (%s)\n", c.Title, c.Location)
		}
		b.WriteString("Set matched=true only if the listing clearly corresponds to one of these requisitions.\n")
	}
	b.WriteString("\nGive verdict as your categorical call (likely-real, uncertain, or likely-ghost), and confidence as how certain you are of that verdict — from 0.0 (unsure) to 1.0 (certain) — NOT how legitimate the job is. Keep reasoning to one or two sentences.")
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
