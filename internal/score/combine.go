// Package score blends the verification arms' signals into a single legitimacy
// Verdict. It is coverage-aware: an arm that did not run contributes nothing and
// its weight is redistributed across the arms that did, so a verdict from thin
// coverage stays distinguishable (via Verdict.Coverage) from a well-covered one.
package score

import (
	"fmt"
	"strings"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Weights are the relative blend weights of each verification arm. Use
// DefaultWeights for the tuned defaults.
type Weights struct {
	ATS    float64
	Claude float64
}

// DefaultWeights leans on the ATS arm as the stronger signal (it is ground
// truth about whether a requisition exists) while still letting the Claude
// judge move the score.
func DefaultWeights() Weights { return Weights{ATS: 0.6, Claude: 0.4} }

// ATSResult is the ATS arm's outcome for one listing. Resolved is false when no
// board was found for the company, which is treated as no ATS coverage rather
// than as a negative signal.
type ATSResult struct {
	Resolved bool
	Matched  bool
	Source   string // ATS the board lives on, set when Resolved
	Slug     string // validated board slug, set when Resolved
}

// Score contributions for the ATS outcomes. A matched requisition is a strong
// positive; a resolved board with no matching requisition is a strong negative
// (the company publishes its roles and this is not among them).
const (
	atsMatchedScore   = 0.9
	atsUnmatchedScore = 0.1

	likelyRealAt  = 0.66 // blended score at/above this is LikelyReal
	likelyGhostAt = 0.33 // blended score at/below this is LikelyGhost
)

// signal is one arm's weighted contribution to the blend.
type signal struct {
	name     string
	weight   float64
	score    float64
	positive string // VerifiedVia text when this is the strongest positive
}

// Combine blends the ATS outcome and the Claude verdict into one Verdict. claude
// is nil when the judge did not run. The result's Coverage lists the arms that
// actually contributed, and VerifiedVia names the strongest positive signal.
func Combine(w Weights, ats ATSResult, claude *model.Verdict) model.Verdict {
	var signals []signal
	if ats.Resolved {
		s := signal{name: ats.Source, weight: w.ATS, score: atsUnmatchedScore}
		if ats.Matched {
			s.score = atsMatchedScore
			s.positive = fmt.Sprintf("%s:%s matched", ats.Source, ats.Slug)
		}
		signals = append(signals, s)
	}
	if claude != nil {
		signals = append(signals, signal{name: "claude", weight: w.Claude, score: claude.Score, positive: "claude"})
	}

	if len(signals) == 0 {
		return model.Verdict{Confidence: model.Uncertain, Reasoning: "no verification signals ran"}
	}

	var weighted, totalWeight, bestPositive float64
	var coverage []string
	var verifiedVia string
	for _, s := range signals {
		weighted += s.weight * s.score
		totalWeight += s.weight
		coverage = append(coverage, s.name)
		if s.positive != "" && s.score >= 0.5 && s.score > bestPositive {
			bestPositive, verifiedVia = s.score, s.positive
		}
	}
	blended := weighted / totalWeight

	return model.Verdict{
		Confidence:  confidenceFor(blended),
		Score:       blended,
		Coverage:    coverage,
		VerifiedVia: verifiedVia,
		Reasoning:   reason(ats, claude, blended),
	}
}

func confidenceFor(score float64) model.Confidence {
	switch {
	case score >= likelyRealAt:
		return model.LikelyReal
	case score <= likelyGhostAt:
		return model.LikelyGhost
	default:
		return model.Uncertain
	}
}

// reason renders a short, human-readable account of what each arm contributed.
func reason(ats ATSResult, claude *model.Verdict, blended float64) string {
	var parts []string
	switch {
	case !ats.Resolved:
		parts = append(parts, "no ATS board found")
	case ats.Matched:
		parts = append(parts, fmt.Sprintf("%s board lists a matching role", ats.Source))
	default:
		parts = append(parts, fmt.Sprintf("%s board found but no matching role", ats.Source))
	}
	if claude != nil && strings.TrimSpace(claude.Reasoning) != "" {
		parts = append(parts, "judge: "+claude.Reasoning)
	}
	return fmt.Sprintf("%s (score %.2f)", strings.Join(parts, "; "), blended)
}
