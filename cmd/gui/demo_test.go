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
