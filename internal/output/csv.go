// Package output renders verified listings to their final formats.
package output

import (
	"encoding/csv"
	"io"
	"strconv"
	"strings"

	"github.com/AndresI19/Job-Search-Go/internal/model"
)

// csvHeader is the column order written by WriteCSV. Keep rowFor in sync.
var csvHeader = []string{
	"title", "company", "company_size", "industries", "location", "remote", "posted",
	"years_experience", "salary_min", "salary_max", "salary_est_min", "salary_est_max",
	"apply_type", "url", "source", "score", "confidence", "applicants", "reasoning",
	"verified_via", "coverage",
}

// Header returns the CSV column order. render and the filter package key off
// these names, so callers can share one row representation.
func Header() []string { return csvHeader }

// Rows flattens results into CSV rows (no header), one per result.
func Rows(results []model.Result) [][]string {
	rows := make([][]string, 0, len(results))
	for _, r := range results {
		rows = append(rows, rowFor(r))
	}
	return rows
}

// WriteCSV writes results as CSV (with a header row) to w. It flushes before
// returning and reports the first write error encountered.
func WriteCSV(w io.Writer, results []model.Result) error {
	return WriteRows(w, Rows(results))
}

// WriteRows writes a header plus the given rows as CSV to w.
func WriteRows(w io.Writer, rows [][]string) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, row := range rows {
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// rowFor flattens one Result into a CSV row matching csvHeader. Unknown values
// (zero Posted time, negative ApplicantCount) render as empty cells rather than
// misleading placeholders like "0001-01-01" or "-1".
func rowFor(r model.Result) []string {
	l, v := r.Listing, r.Verdict

	posted := ""
	if !l.Posted.IsZero() {
		posted = l.Posted.UTC().Format("2006-01-02")
	}

	applicants := ""
	if l.ApplicantCount >= 0 {
		applicants = strconv.Itoa(l.ApplicantCount)
	}

	years := ""
	if l.YearsExperience > 0 {
		years = strconv.Itoa(l.YearsExperience)
	}

	// Column order is presentation-oriented: identity, then what the role asks
	// for, then the verdict (score drives the row colour in render, so no separate
	// confidence column), with the verbose verified/coverage fields trailing.
	size := ""
	if l.CompanySize > 0 {
		size = strconv.Itoa(l.CompanySize)
	}

	return []string{
		l.Title,
		l.Company,
		size,
		l.Industries,
		l.Location,
		strconv.FormatBool(l.Remote),
		posted,
		years,
		usdOrEmpty(l.SalaryMin),
		usdOrEmpty(l.SalaryMax),
		usdOrEmpty(l.SalaryEstMin),
		usdOrEmpty(l.SalaryEstMax),
		l.ApplyType,
		l.URL,
		l.Source,
		strconv.FormatFloat(v.Score, 'f', 2, 64),
		string(v.Confidence),
		applicants,
		v.Reasoning,
		v.VerifiedVia,
		strings.Join(v.Coverage, ";"),
	}
}

// usdOrEmpty renders a salary figure, or "" when unknown (0), so a missing
// salary is a blank cell rather than a misleading 0.
func usdOrEmpty(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}
