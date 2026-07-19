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
