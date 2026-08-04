// Package summarize turns a scraped job listing's description into the compact,
// apply-ready summary the Applicator page shows: what a posting REQUIRES vs
// PREFERS, what the role does, what the company does, and whether it is contract
// or permanent work (which is not comparable to a salaried role). It mirrors
// internal/judge: one Summarizer interface with three interchangeable backends —
//
//   - CLISummarizer shells out to the `claude` command, reusing a Claude Code
//     login (a subscription) so no API key is needed — local dev.
//   - APISummarizer calls the Anthropic API with a key — a keyed in-cluster backend.
//   - GeminiSummarizer calls Google's Gemini API with a free-tier key ($0) — the
//     in-cluster backend when an Anthropic key is not worth the cost/billing.
//   - MockSummarizer derives a cheap summary from the listing's own fields, so the
//     whole batch flow can be proven end to end for $0.
//
// Pick one with FromEnv; wrap it with Bounded to cap concurrent Claude calls.
package summarize

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

// Summary is the per-listing result rendered on the Applicator page. It aliases
// model.JobSummary so the db and server can share the type without depending on
// this package (or the Anthropic SDK it pulls in).
type Summary = model.JobSummary

// Summarizer produces a Summary for one listing. Implementations honor ctx.
type Summarizer interface {
	Summarize(ctx context.Context, l model.Listing) (Summary, error)
}

// summarySchema is the JSON shape every backend asks Claude to return. Employment
// separates contract/temp/freelance work from a permanent salaried role, since the
// Applicator treats the two very differently (comp basis, term, benefits).
const summarySchema = `{"type":"object","properties":` +
	`{"required":{"type":"string","description":"must-have experience and qualifications in one line (years, seniority, key skills); '--' if none stated"},` +
	`"preferred":{"type":"string","description":"nice-to-have/preferred/bonus experience in one line; '--' if none"},` +
	`"role":{"type":"string","description":"what the person in this role actually does day-to-day, one line"},` +
	`"company":{"type":"string","description":"what the company does or its product, one line; '--' if the posting does not say"},` +
	`"employment":{"type":"string","enum":["contract","permanent","unclear"],"description":"contract/temp/freelance engagement vs a permanent salaried position"},` +
	`"pay_note":{"type":"string","description":"the pay basis if the posting states one, e.g. '$75/hr' or '$120k-$160k/yr'; empty string if not stated"}},` +
	`"required":["required","preferred","role","company","employment"]}`

// rawSummary is the JSON every backend decodes before mapping to Summary.
type rawSummary struct {
	Required   string `json:"required"`
	Preferred  string `json:"preferred"`
	Role       string `json:"role"`
	Company    string `json:"company"`
	Employment string `json:"employment"`
	PayNote    string `json:"pay_note"`
}

func (r rawSummary) toModel() Summary {
	emp := strings.ToLower(strings.TrimSpace(r.Employment))
	if emp != "contract" && emp != "permanent" {
		emp = "unclear"
	}
	return Summary{
		Required:   dflt(r.Required, "--"),
		Preferred:  dflt(r.Preferred, "--"),
		Role:       dflt(r.Role, "--"),
		Company:    dflt(r.Company, "--"),
		Employment: emp,
		PayNote:    strings.TrimSpace(r.PayNote),
	}
}

func dflt(s, def string) string {
	if s = strings.TrimSpace(s); s == "" {
		return def
	}
	return s
}

// FromEnv builds a Summarizer from the environment, mirroring judge.FromEnv:
// SUMMARIZE_BACKEND (cli|api|mock) falls back to JUDGE_BACKEND then "cli";
// SUMMARIZE_MODEL falls back to JUDGE_MODEL then the cheap default. The result is
// wrapped in Bounded (SUMMARIZE_CONCURRENCY, else a per-backend default).
func FromEnv() (Summarizer, error) {
	backend := strings.ToLower(envOr("SUMMARIZE_BACKEND", envOr("JUDGE_BACKEND", "cli")))
	modelID := envOr("SUMMARIZE_MODEL", envOr("JUDGE_MODEL", defaultModel))

	var (
		inner      Summarizer
		err        error
		defaultLim int
	)
	switch backend {
	case "cli":
		inner, defaultLim = NewCLISummarizer(modelID), 3
	case "api":
		inner, err, defaultLim = NewAPISummarizer(modelID), nil, 16
	case "gemini":
		// Free-tier keyed backend; lower concurrency to stay inside the free RPM quota.
		inner, defaultLim = NewGeminiSummarizer(modelID), 4
	case "mock":
		inner, defaultLim = MockSummarizer{}, 16
	default:
		return nil, fmt.Errorf("unknown SUMMARIZE_BACKEND %q (want \"cli\", \"api\", or \"mock\")", backend)
	}
	if err != nil {
		return nil, err
	}

	lim := defaultLim
	if v := os.Getenv("SUMMARIZE_CONCURRENCY"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			lim = n
		}
	}
	return Bounded(inner, lim), nil
}

// buildPrompt asks Claude for the six summary fields from the listing's own
// description, grounded strictly in the posting text (never invented).
func buildPrompt(l model.Listing) string {
	var b strings.Builder
	b.WriteString("Summarize this job posting for an applicant, using ONLY its description. Never invent facts; if the description does not state something, use the specified fallback.\n\n")
	fmt.Fprintf(&b, "TITLE: %s\nCOMPANY: %s\nLOCATION: %s\n\nDESCRIPTION:\n%s\n\n", l.Title, l.Company, l.Location, l.Description)
	b.WriteString("Return the required, preferred, role, company, employment, and pay_note fields. Keep each to one concise, plain line (no marketing fluff). employment: classify as contract (contract/temp/freelance/hourly engagement) or permanent (a salaried role); use unclear only if the posting genuinely gives no signal.")
	return b.String()
}

func parseSummary(b []byte) (rawSummary, error) {
	var r rawSummary
	if err := json.Unmarshal(b, &r); err != nil {
		return rawSummary{}, fmt.Errorf("decode summary: %w", err)
	}
	return r, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
