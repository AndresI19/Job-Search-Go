package ats

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

func TestCandidateSlugs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Stripe", []string{"stripe"}},
		{"Acme Corp", []string{"acme"}},
		{"Acme Corporation, Inc.", []string{"acme"}},
		{"Data Dog", []string{"datadog", "data-dog", "data"}},
		{"The Home Depot", []string{"homedepot", "home-depot", "home"}},
		{"", nil},
	}
	for _, tc := range cases {
		if got := CandidateSlugs(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("CandidateSlugs(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// boardSource is a stub model.Source that returns a board for known slugs and a
// 404-like error for anything else.
type boardSource struct {
	name  string
	valid map[string][]model.Listing
}

func (s boardSource) Name() string { return s.name }

func (s boardSource) Fetch(_ context.Context, query string) ([]model.Listing, error) {
	if b, ok := s.valid[query]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("%s: no board %q", s.name, query)
}

func TestResolvePicksSlugAndSource(t *testing.T) {
	gh := boardSource{name: "greenhouse", valid: map[string][]model.Listing{}}
	lv := boardSource{name: "lever", valid: map[string][]model.Listing{"datadog": {{JobID: "1"}}}}
	r := NewResolver(gh, lv)

	res, ok := r.Resolve(context.Background(), "Data Dog, Inc.")
	if !ok {
		t.Fatal("expected resolution")
	}
	if res.Source != "lever" || res.Slug != "datadog" {
		t.Errorf("resolved to %s/%s, want lever/datadog", res.Source, res.Slug)
	}
	if len(res.Listings) != 1 {
		t.Errorf("listings = %v, want the board handed back", res.Listings)
	}
}

func TestResolvePrefersEarlierSource(t *testing.T) {
	gh := boardSource{name: "greenhouse", valid: map[string][]model.Listing{"acme": {{JobID: "g"}}}}
	lv := boardSource{name: "lever", valid: map[string][]model.Listing{"acme": {{JobID: "l"}}}}
	r := NewResolver(gh, lv)

	res, ok := r.Resolve(context.Background(), "Acme")
	if !ok || res.Source != "greenhouse" {
		t.Errorf("res = %+v ok=%v, want greenhouse to win the tie", res, ok)
	}
}

func TestResolveNoMatch(t *testing.T) {
	gh := boardSource{name: "greenhouse", valid: map[string][]model.Listing{}}
	if _, ok := NewResolver(gh).Resolve(context.Background(), "Nonexistent Co"); ok {
		t.Error("expected no resolution")
	}
}
