package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	p := Default()
	p.Filters.MinSalary = 175000
	p.Highlight.SalaryStrong = 200000
	p.EstimateSalary = false

	path := filepath.Join(t.TempDir(), "profile.yaml")
	if err := p.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Filters.MinSalary != 175000 || got.Highlight.SalaryStrong != 200000 || got.EstimateSalary {
		t.Errorf("round-trip lost values: %+v", got)
	}
}

func TestLoadPartialKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.yaml")
	// Only the salary floor is set; everything else must fall back to Default.
	if err := os.WriteFile(path, []byte("filters:\n  min_salary: 200000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Filters.MinSalary != 200000 {
		t.Errorf("min_salary = %d, want 200000", got.Filters.MinSalary)
	}
	if got.Highlight.SalaryLight != Default().Highlight.SalaryLight {
		t.Errorf("unset highlight not defaulted: %d", got.Highlight.SalaryLight)
	}
	if !got.EstimateSalary {
		t.Error("unset estimate_salary should default to true")
	}
}
