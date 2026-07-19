package report

import (
	"strings"
	"testing"
	"time"
)

// cfg is a Config with the default thresholds, for exercising the colour rules.
var cfg = Config{
	SalaryLight: 165000, SalaryStrong: 180000, StartupMax: 200,
	FreshDays: 7, RecentDays: 30, AgingDays: 90, EstimateSalary: true,
}

func TestIsConsultingShop(t *testing.T) {
	tests := []struct {
		industries string
		want       bool
	}{
		{"IT Services and IT Consulting", true},
		{"Staffing and Recruiting", true},
		{"Human Resources Services", true},
		{"Business Consulting and Services", true},
		{"Software Development", false},
		{"Software Development, Information Services, and IT Services and IT Consulting", false},
		{"Financial Services", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isConsultingShop(tt.industries); got != tt.want {
			t.Errorf("isConsultingShop(%q) = %v, want %v", tt.industries, got, tt.want)
		}
	}
}

func TestIsStartup(t *testing.T) {
	row := func(size, ind string) []string { return []string{size, ind} }
	tests := []struct {
		name string
		row  []string
		want bool
	}{
		{"small product company", row("40", "Software Development"), true},
		{"small body-shop excluded", row("9", "IT Services and IT Consulting"), false},
		{"too big to be a startup", row("5000", "Software Development"), false},
		{"unknown size", row("", "Software Development"), false},
	}
	for _, tt := range tests {
		if got := cfg.isStartup(tt.row, 0, 1); got != tt.want {
			t.Errorf("%s: isStartup = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSalaryTierFill(t *testing.T) {
	tests := []struct {
		max  float64
		want string
	}{
		{250000, fillSalaryHi},
		{180000, fillSalaryHi},
		{179999, fillSalary},
		{165000, fillSalary},
		{164999, ""},
		{160000, ""},
		{0, ""},
	}
	for _, tt := range tests {
		if got := cfg.salaryTierFill(tt.max); got != tt.want {
			t.Errorf("salaryTierFill(%v) = %q, want %q", tt.max, got, tt.want)
		}
	}
}

func TestRecencyFill(t *testing.T) {
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	day := func(n int) string { return now.AddDate(0, 0, -n).Format("2006-01-02") }
	tests := []struct {
		posted string
		want   string
	}{
		{day(2), fillFresh},
		{day(20), fillRecent},
		{day(60), fillAging},
		{day(200), fillStale},
		{"", ""},
		{"not-a-date", ""},
	}
	for _, tt := range tests {
		if got := cfg.recencyFill(tt.posted, now); got != tt.want {
			t.Errorf("recencyFill(%q) = %q, want %q", tt.posted, got, tt.want)
		}
	}
}

func TestRecencyIsDistinctFromSalaryGreens(t *testing.T) {
	for _, r := range []string{fillFresh, fillRecent, fillAging, fillStale} {
		for _, s := range []string{fillSalary, fillSalaryHi} {
			if strings.EqualFold(r, s) {
				t.Errorf("recency fill %s collides with salary fill %s", r, s)
			}
		}
	}
}
