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
	"title", "company", "location", "remote", "posted",
	"applicants", "apply_type", "url", "source",
	"confidence", "score", "verified_via", "coverage", "reasoning",
}

// WriteCSV writes results as CSV (with a header row) to w. It flushes before
// returning and reports the first write error encountered.
func WriteCSV(w io.Writer, results []model.Result) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return err
	}
	for _, r := range results {
		if err := cw.Write(rowFor(r)); err != nil {
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

	return []string{
		l.Title,
		l.Company,
		l.Location,
		strconv.FormatBool(l.Remote),
		posted,
		applicants,
		l.ApplyType,
		l.URL,
		l.Source,
		string(v.Confidence),
		strconv.FormatFloat(v.Score, 'f', 2, 64),
		v.VerifiedVia,
		strings.Join(v.Coverage, ";"),
		v.Reasoning,
	}
}
