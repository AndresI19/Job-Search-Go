package greenhouse

import (
	"regexp"
	"strings"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// matchThreshold is the minimum title similarity for Match to call a scraped
// listing and a board requisition the same role. It is deliberately lenient:
// this is a cheap pre-filter, and genuinely ambiguous pairs are meant to fall
// through to the Claude judge with the full candidate set.
const matchThreshold = 0.6

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Match finds the board requisition whose title best matches listing's title,
// returning it and true when the similarity clears matchThreshold. It compares
// on word overlap rather than exact strings, so "Sr. Software Engineer, Platform"
// and "Senior Software Engineer - Platform" still line up on their shared words.
func Match(listing model.Listing, reqs []model.Listing) (model.Listing, bool) {
	want := tokenize(listing.Title)
	if len(want) == 0 {
		return model.Listing{}, false
	}
	var best model.Listing
	var bestScore float64
	for _, r := range reqs {
		if s := jaccard(want, tokenize(r.Title)); s > bestScore {
			bestScore, best = s, r
		}
	}
	if bestScore >= matchThreshold {
		return best, true
	}
	return model.Listing{}, false
}

// tokenize lowercases a title and reduces it to a set of alphanumeric words,
// dropping punctuation and duplicates so titles compare on their vocabulary.
func tokenize(title string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, w := range strings.Fields(nonAlnum.ReplaceAllString(strings.ToLower(title), " ")) {
		set[w] = struct{}{}
	}
	return set
}

// jaccard is the intersection-over-union of two word sets: 1 when identical,
// 0 when disjoint.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if _, ok := b[w]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
