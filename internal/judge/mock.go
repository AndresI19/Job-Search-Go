package judge

import (
	"context"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// MockJudge is a $0, no-network Judge: it scores a listing heuristically from its
// own signals and never calls Claude. It exists to exercise the whole ingest →
// verify pipeline end to end for free (select it with JUDGE_BACKEND=mock), so the
// real Apify+Claude wiring can be proven before spending a cent on tokens.
type MockJudge struct{}

// Evaluate returns a verdict from cheap signals: an ATS requisition match reads
// real; a board that was checked but has no match reads ghost; a low applicant
// count leans real; everything else is uncertain.
func (MockJudge) Evaluate(_ context.Context, in Input) (model.Verdict, error) {
	verdict, conf, matched := "uncertain", 0.5, false
	switch {
	case len(in.Candidates) > 0:
		verdict, conf, matched = "likely-real", 0.8, true
	case in.ATSChecked:
		verdict, conf = "likely-ghost", 0.7
	case in.Listing.ApplicantCount >= 0 && in.Listing.ApplicantCount < 30:
		verdict, conf = "likely-real", 0.6
	}
	return rawVerdict{
		Verdict:    verdict,
		Confidence: conf,
		Matched:    matched,
		Reasoning:  "mock heuristic verdict (no Claude call)",
	}.toModel(), nil
}
