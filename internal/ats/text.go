package ats

import (
	"html"
	"regexp"
	"strings"
)

var tagRE = regexp.MustCompile(`<[^>]*>`)

// StripHTML turns a source's HTML job description into plain text. ATS APIs
// return descriptions as entity-encoded HTML, so the first unescape yields
// markup; tags are dropped and a second unescape resolves entities that were
// inside it. Whitespace is collapsed to single spaces.
func StripHTML(s string) string {
	s = html.UnescapeString(s)
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}
