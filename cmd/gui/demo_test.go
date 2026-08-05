package main

import (
	"testing"
	"time"
)

// The demo scan quota: one per window per key. A second immediate attempt on the same key
// is blocked with a positive wait; a different key is independent.
func TestAllowDemoRun(t *testing.T) {
	s := &server{demoRuns: map[string]time.Time{}}

	if _, ok := s.allowDemoRun("u:alice"); !ok {
		t.Fatal("first scan for a key should be allowed")
	}
	wait, ok := s.allowDemoRun("u:alice")
	if ok {
		t.Fatal("second immediate scan for the same key should be blocked")
	}
	if wait <= 0 || wait > demoRunWindow {
		t.Errorf("wait = %v, want a positive duration within the window", wait)
	}
	if _, ok := s.allowDemoRun("u:bob"); !ok {
		t.Error("a different key should be allowed independently")
	}
}

// The escape hatch that lets paid backends run without platform auth (trusted local testing).
func TestUnauthedRealAllowed(t *testing.T) {
	t.Setenv("ALLOW_UNAUTHENTICATED_REAL", "")
	if unauthedRealAllowed() {
		t.Error("unset should be false — a deploy without auth must stay mock-only")
	}
	t.Setenv("ALLOW_UNAUTHENTICATED_REAL", "1")
	if !unauthedRealAllowed() {
		t.Error(`"1" should opt in`)
	}
	t.Setenv("ALLOW_UNAUTHENTICATED_REAL", "true")
	if unauthedRealAllowed() {
		t.Error("only exactly \"1\" opts in, not any truthy string")
	}
}
