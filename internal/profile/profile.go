// Package profile is the single source of the tunable knobs that shape a run —
// the post-ingest filters and the review-sheet highlight thresholds. Lifting
// them out of hardcoded constants means a search's specification lives in a
// profile.yaml (editable by hand or by the GUI), not baked into the binary, so
// you are never committed to one set of job criteria.
package profile

import (
	"os"

	"go.yaml.in/yaml/v4"
)

// Filters decide which verified listings survive to the review sheet.
type Filters struct {
	Locations     []string `yaml:"locations" json:"locations"`           // keep listings matching any of these (a "remote" term also keeps remote roles)
	RemoteOK      bool     `yaml:"remote_ok" json:"remote_ok"`           // keep remote roles regardless of location
	MaxAgeDays    int      `yaml:"max_age_days" json:"max_age_days"`     // drop listings posted longer ago than this; 0 disables
	MinSalary     int      `yaml:"min_salary" json:"min_salary"`         // drop listings whose max pay (posted, else estimated) is below this; 0 disables
	MinScore      float64  `yaml:"min_score" json:"min_score"`           // drop listings scoring below this (0..1)
	IncludeGhosts bool     `yaml:"include_ghosts" json:"include_ghosts"` // keep likely-ghost listings (dropped by default)
}

// Highlight thresholds drive the review sheet's cell colours.
type Highlight struct {
	SalaryLight  int `yaml:"salary_light" json:"salary_light"`               // salary max at/above this gets the light-green highlight
	SalaryStrong int `yaml:"salary_strong" json:"salary_strong"`             // salary max at/above this gets the strong-green highlight
	StartupMax   int `yaml:"startup_max" json:"startup_max"`                 // company headcount below this (and > 0) reads as a startup
	FreshDays    int `yaml:"recency_fresh_days" json:"recency_fresh_days"`   // posted within this many days: the freshest colour
	RecentDays   int `yaml:"recency_recent_days" json:"recency_recent_days"` // posted within this many days: the recent colour
	AgingDays    int `yaml:"recency_aging_days" json:"recency_aging_days"`   // posted within this many days: the aging colour; older is stale
}

// Profile bundles every knob a search's specification exposes.
type Profile struct {
	Filters        Filters   `yaml:"filters" json:"filters"`
	Highlight      Highlight `yaml:"highlight" json:"highlight"`
	EstimateSalary bool      `yaml:"estimate_salary" json:"estimate_salary"` // count/show heuristic pay estimates for listings that post none
}

// Default is the profile matching the tool's built-in behaviour — the settings
// arrived at through this project's tuning. A hand-written or GUI-saved profile
// overrides whichever fields it sets.
func Default() Profile {
	return Profile{
		Filters: Filters{
			Locations:     []string{"Boston", "New York", "Los Angeles"},
			RemoteOK:      true,
			MaxAgeDays:    90,
			MinSalary:     160000,
			MinScore:      0,
			IncludeGhosts: false,
		},
		Highlight: Highlight{
			SalaryLight:  165000,
			SalaryStrong: 180000,
			StartupMax:   200,
			FreshDays:    7,
			RecentDays:   30,
			AgingDays:    90,
		},
		EstimateSalary: true,
	}
}

// Load reads a profile from a YAML file, starting from Default so a partial file
// only overrides the fields it names.
func Load(path string) (Profile, error) {
	p := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := yaml.Unmarshal(b, &p); err != nil {
		return p, err
	}
	return p, nil
}

// Save writes the profile to a YAML file.
func (p Profile) Save(path string) error {
	b, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
