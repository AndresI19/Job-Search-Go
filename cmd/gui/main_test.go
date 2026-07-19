package main

import (
	"strings"
	"testing"
)

func TestFieldQuery(t *testing.T) {
	// A field's search is all its roles OR'd — every role query is present.
	q := fieldQuery("software")
	for _, r := range fieldCatalog[0].Roles {
		if !strings.Contains(q, r.Query) {
			t.Errorf("software query missing role %q: %s", r.Key, q)
		}
	}
	if n := strings.Count(q, " OR "); n < len(fieldCatalog[0].Roles)-1 {
		t.Errorf("expected roles OR'd together, got %d separators in %s", n, q)
	}
	// An unknown field falls back to the first field, never empty.
	if fieldQuery("nope") != fieldQuery("software") {
		t.Errorf("unknown field should default to the first field")
	}
}

func TestLocationCatalog(t *testing.T) {
	// Every supported location is a usable filter/normalization mapping: a unique
	// key and label, and at least one lowercase match substring to key off.
	keys, labels := map[string]bool{}, map[string]bool{}
	for _, l := range locationCatalog {
		if keys[l.Key] {
			t.Errorf("duplicate location key %q", l.Key)
		}
		if labels[l.Label] {
			t.Errorf("duplicate location label %q", l.Label)
		}
		keys[l.Key], labels[l.Label] = true, true
		if len(l.Match) == 0 {
			t.Errorf("location %q has no match substrings", l.Key)
		}
		for _, m := range l.Match {
			if m != strings.ToLower(m) {
				t.Errorf("location %q match %q must be lowercase (raw values are lowercased before compare)", l.Key, m)
			}
		}
	}
}
