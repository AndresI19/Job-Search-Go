package summarize

import (
	"context"
	"regexp"
	"strings"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// MockSummarizer is a $0, no-network Summarizer: it derives a crude summary from
// the listing's own fields and never calls Claude. It exists to prove the whole
// batch flow (SUMMARIZE_BACKEND=mock) before spending a cent on tokens.
type MockSummarizer struct{}

var (
	contractHint = regexp.MustCompile(`(?i)\b(contract|contractor|freelance|temporary|1099|c2c|corp[- ]to[- ]corp)\b`)
	hourlyHint   = regexp.MustCompile(`\$\s*\d[\d,.]*\s*/\s*(?i:hr|hour)`)
)

func (MockSummarizer) Summarize(_ context.Context, l model.Listing) (Summary, error) {
	emp := "permanent"
	if strings.Contains(strings.ToLower(l.EmploymentType), "contract") ||
		contractHint.MatchString(l.Title) || contractHint.MatchString(l.Description) {
		emp = "contract"
	}
	pay := ""
	if m := hourlyHint.FindString(l.Description); m != "" {
		pay = strings.Join(strings.Fields(m), "")
	}
	role := strings.TrimSpace(l.Title)
	if role != "" {
		role = "Works as a " + role
	} else {
		role = "—"
	}
	return Summary{
		Required:   "--",
		Preferred:  "--",
		Role:       role,
		Company:    firstSentence(l.Description),
		Employment: emp,
		PayNote:    pay,
	}, nil
}

// firstSentence returns the first sentence (or first ~160 chars) of s, or a
// fallback when s is empty.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "--"
	}
	if i := strings.IndexAny(s, ".\n"); i > 0 && i < 160 {
		s = s[:i]
	} else if len(s) > 160 {
		s = s[:160]
	}
	return strings.TrimSpace(s)
}
