// Package comp produces a rough salary estimate for a listing that publishes no
// salary, so a blank pay field doesn't quietly bury an otherwise-strong role.
//
// The estimate is a heuristic, NOT sourced compensation data: it maps a role's
// seniority (from its title, since LinkedIn's seniority field is usually "Not
// Applicable") to a base band, scales it by the local market, and prices only
// software/engineering titles — a band it can't responsibly guess returns 0,0.
// Callers must present the result as an estimate, never as posted pay.
package comp

import "strings"

// band is the annual-USD [min,max] for a seniority tier, before the metro scale.
var bands = map[string][2]int{
	"entry":  {110000, 140000},
	"mid":    {150000, 185000},
	"senior": {180000, 220000},
	"staff":  {220000, 280000},
}

// metroMult scales a band for the local market. First substring match wins;
// anything unlisted (including remote) stays at 1.0.
var metroMult = []struct {
	key  string
	mult float64
}{
	{"san francisco", 1.15}, {"bay area", 1.15}, {"palo alto", 1.15}, {"mountain view", 1.15},
	{"new york", 1.10}, {"seattle", 1.10}, {"boston", 1.08}, {"los angeles", 1.05},
}

// softwareRole gates estimation to engineering/technical titles, so the SWE
// bands are never applied to a role we can't price this way (sales, recruiting).
var softwareRole = []string{
	"engineer", "developer", "swe", "software", "programmer", "data ",
	"machine learning", " ml ", "backend", "back-end", "frontend", "front-end",
	"full stack", "fullstack", "full-stack", "devops", " sre ", "platform",
	"security", "infrastructure", "architect",
}

// tier picks a band key from the seniority label and the title. The title is the
// stronger signal because LinkedIn's seniority field is "Not Applicable" for most
// postings; the label only breaks ties (e.g. its "Mid-Senior level").
func tier(seniority, title string) string {
	s := strings.ToLower(seniority)
	t := strings.ToLower(title)
	switch {
	case strings.Contains(t, "principal"), strings.Contains(t, "staff"),
		strings.Contains(t, "distinguished"), strings.Contains(t, "architect"):
		return "staff"
	case strings.Contains(t, "senior"), strings.Contains(t, "sr."), strings.Contains(t, "sr "),
		strings.Contains(t, "lead"), strings.Contains(s, "mid-senior"):
		return "senior"
	// LinkedIn's "Associate" is early-career but past entry, so it maps to mid
	// (the default), not entry — only a junior/entry/intern signal is entry.
	case strings.Contains(t, "junior"), strings.Contains(t, "entry"),
		strings.Contains(s, "entry"), strings.Contains(s, "internship"):
		return "entry"
	default:
		return "mid"
	}
}

func isSoftwareRole(title string) bool {
	t := strings.ToLower(" " + title + " ")
	for _, k := range softwareRole {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// Estimate returns a rough annual-USD band for a role, or 0,0 when it can't
// responsibly price one (a non-technical title). The result is an estimate and
// must be labelled as such by the caller.
func Estimate(title, seniority, location string) (min, max int) {
	if !isSoftwareRole(title) {
		return 0, 0
	}
	b := bands[tier(seniority, title)]
	mult := 1.0
	loc := strings.ToLower(location)
	for _, m := range metroMult {
		if strings.Contains(loc, m.key) {
			mult = m.mult
			break
		}
	}
	round := func(v float64) int { return int((v+500)/1000) * 1000 }
	return round(float64(b[0]) * mult), round(float64(b[1]) * mult)
}
