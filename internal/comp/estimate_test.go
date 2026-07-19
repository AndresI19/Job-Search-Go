package comp

import "testing"

func TestEstimate(t *testing.T) {
	tests := []struct {
		name             string
		title, sen, loc  string
		wantMin, wantMax int
	}{
		{"senior in NYC scales up", "Senior Backend Engineer", "", "New York, NY", 198000, 242000},
		{"mid default when title is plain", "Software Engineer", "", "Austin, TX", 150000, 185000},
		{"staff tier from title", "Staff Platform Engineer", "", "Remote, US", 220000, 280000},
		{"entry from junior title", "Junior Developer", "", "Chicago, IL", 110000, 140000},
		{"seniority field breaks the tie", "Software Engineer", "Mid-Senior level", "Boston, MA", 194000, 238000},
		{"associate maps to mid, not entry", "Software Engineer", "Associate", "Austin, TX", 150000, 185000},
		{"non-software role is not priced", "Account Executive", "", "New York, NY", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			min, max := Estimate(tt.title, tt.sen, tt.loc)
			if min != tt.wantMin || max != tt.wantMax {
				t.Errorf("Estimate(%q,%q,%q) = %d,%d; want %d,%d",
					tt.title, tt.sen, tt.loc, min, max, tt.wantMin, tt.wantMax)
			}
		})
	}
}
