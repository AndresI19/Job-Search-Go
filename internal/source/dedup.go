package source

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// Dedup removes cross-source duplicates from a merged listing set — the same job
// posted to both LinkedIn and Indeed. The DB's URL-unique constraint can't catch
// these (different boards, different URLs), so this runs before verification.
//
// Two listings are the same job when they share a canonical external apply URL
// (both link to the same ATS), OR a fuzzy identity key (normalized company +
// title + location). First-seen wins, so the stable source order in All()
// (LinkedIn first) decides which copy is kept. Returns the deduped set and how
// many duplicates were dropped (for the scan summary).
func Dedup(in []model.Listing) (out []model.Listing, dropped int) {
	seenApply := map[string]bool{}
	seenFuzzy := map[string]bool{}
	out = make([]model.Listing, 0, len(in))
	for _, l := range in {
		ak := canonURL(l.ExternalApplyURL)
		fk := fuzzyKey(l)
		if (ak != "" && seenApply[ak]) || (fk != "" && seenFuzzy[fk]) {
			dropped++
			continue
		}
		if ak != "" {
			seenApply[ak] = true
		}
		if fk != "" {
			seenFuzzy[fk] = true
		}
		out = append(out, l)
	}
	return out, dropped
}

// canonURL reduces a URL to scheme+host+path (query/fragment dropped), so the same
// ATS apply link tracked differently by each board collapses to one key.
func canonURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}
	u.RawQuery, u.Fragment = "", ""
	return strings.ToLower(u.String())
}

func fuzzyKey(l model.Listing) string {
	c, t := normCompany(l.Company), normText(l.Title)
	if c == "" || t == "" {
		return ""
	}
	return c + "|" + t + "|" + normText(locPrefix(l.Location))
}

// legalSuffix are trailing company-name words that differ cosmetically between
// boards (mirrors the ATS matcher's normalization).
var legalSuffix = map[string]bool{
	"inc": true, "incorporated": true, "corp": true, "corporation": true, "co": true,
	"company": true, "llc": true, "ltd": true, "limited": true, "plc": true, "lp": true, "llp": true,
}

func normCompany(s string) string {
	words := strings.Fields(normText(s))
	if len(words) > 1 && legalSuffix[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9 ]+`)

// normText lowercases, strips punctuation, and collapses whitespace so cosmetic
// differences between the two boards' text don't defeat the match.
func normText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonAlnum.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func locPrefix(s string) string {
	if i := strings.IndexByte(s, ','); i > 0 {
		return s[:i]
	}
	return s
}
