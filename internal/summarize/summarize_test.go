package summarize

import (
	"context"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

func TestMockSummarizerContractDetection(t *testing.T) {
	m := MockSummarizer{}
	ctx := context.Background()

	// Contract signalled by the title, and an hourly rate lifted into PayNote.
	c, _ := m.Summarize(ctx, model.Listing{Title: "Contract Backend Engineer", Description: "Build APIs. Pays $80/hr."})
	if c.Employment != "contract" {
		t.Errorf("title contract → employment=%q, want contract", c.Employment)
	}
	if c.PayNote != "$80/hr" {
		t.Errorf("pay note = %q, want $80/hr", c.PayNote)
	}

	// EmploymentType field is an independent contract signal.
	e, _ := m.Summarize(ctx, model.Listing{Title: "Engineer", EmploymentType: "Contract", Description: "x"})
	if e.Employment != "contract" {
		t.Errorf("EmploymentType contract → %q", e.Employment)
	}

	// Otherwise permanent, with sensible field defaults.
	p, _ := m.Summarize(ctx, model.Listing{Title: "Senior Engineer", Description: "Join our platform team. Great benefits."})
	if p.Employment != "permanent" {
		t.Errorf("default employment = %q, want permanent", p.Employment)
	}
	if p.Required != "Not specified" || p.Preferred != "None stated" || p.Role == "" {
		t.Errorf("defaults not filled: %+v", p)
	}
}

func TestFromEnvMock(t *testing.T) {
	t.Setenv("SUMMARIZE_BACKEND", "mock")
	s, err := FromEnv()
	if err != nil || s == nil {
		t.Fatalf("FromEnv(mock) = %v, %v", s, err)
	}
	out, err := s.Summarize(context.Background(), model.Listing{Title: "Software Engineer", Description: "Ship features."})
	if err != nil || out.Role == "" {
		t.Fatalf("summarize: %v %+v", err, out)
	}
}
