// Command render turns a results CSV (as written by jobsearch) into a formatted
// .xlsx for review: each row is tinted by its score (green/amber/red — so a
// separate confidence column is unnecessary), the title is a trimmed clickable
// hyperlink to the posting (the url column is folded into it), salary shows as
// currency, dates as dd-mm-yyyy, every cell is bordered, and the header is
// frozen and auto-filterable (filter by location, salary, years, etc.).
//
//	render --in results.csv --out results.xlsx
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// numericCols are written as numbers (not text) so they sort/filter correctly.
var numericCols = map[string]bool{"score": true, "salary_min": true, "salary_max": true, "applicants": true, "years_experience": true}

// colWidth overrides the default width for a few wide/narrow columns.
var colWidth = map[string]float64{"title": 40, "company": 22, "location": 22, "reasoning": 90, "verified_via": 26, "coverage": 18, "years_experience": 10}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	in := flag.String("in", "results.csv", "input results CSV")
	out := flag.String("out", "results.xlsx", "output xlsx path")
	sheet := flag.String("sheet", "Listings", "sheet name")
	flag.Parse()

	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return err
	}
	if len(rows) < 1 {
		return fmt.Errorf("%s has no header row", *in)
	}
	header := rows[0]
	idx := func(name string) int {
		for i, h := range header {
			if strings.EqualFold(h, name) {
				return i
			}
		}
		return -1
	}
	postedCol, urlCol, scoreCol, titleCol := idx("posted"), idx("url"), idx("score"), idx("title")
	salaryCol := map[int]bool{idx("salary_min"): true, idx("salary_max"): true}

	// The url is folded into the title hyperlink, so it is not its own column.
	// outCol maps each input column to its output column, or -1 if omitted.
	outCol := make([]int, len(header))
	oc := 0
	for c := range header {
		if c == urlCol {
			outCol[c] = -1
			continue
		}
		outCol[c] = oc
		oc++
	}
	outN := oc

	xl := excelize.NewFile()
	defer xl.Close()
	if err := xl.SetSheetName("Sheet1", *sheet); err != nil {
		return err
	}

	border := []excelize.Border{
		{Type: "left", Style: 1, Color: "D9D9D9"}, {Type: "right", Style: 1, Color: "D9D9D9"},
		{Type: "top", Style: 1, Color: "D9D9D9"}, {Type: "bottom", Style: 1, Color: "D9D9D9"},
	}
	cache := map[string]int{}
	styleFor := func(fillHex, numFmt string) int {
		key := fillHex + "|" + numFmt
		if id, ok := cache[key]; ok {
			return id
		}
		s := &excelize.Style{Border: border, Alignment: &excelize.Alignment{Vertical: "top"}}
		if fillHex != "" {
			s.Fill = excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{fillHex}}
		}
		if numFmt != "" {
			nf := numFmt
			s.CustomNumFmt = &nf
		}
		id, _ := xl.NewStyle(s)
		cache[key] = id
		return id
	}
	headStyle, _ := xl.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Border: border,
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"1F3B73"}},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})

	// Header row (skipping the folded-away url column).
	for c, h := range header {
		if outCol[c] < 0 {
			continue
		}
		cell, _ := excelize.CoordinatesToCellName(outCol[c]+1, 1)
		_ = xl.SetCellValue(*sheet, cell, h)
	}
	lastHeader, _ := excelize.CoordinatesToCellName(outN, 1)
	_ = xl.SetCellStyle(*sheet, "A1", lastHeader, headStyle)

	// Data rows.
	for r := 1; r < len(rows); r++ {
		row := rows[r]
		fill := scoreFill(row, scoreCol)
		url := ""
		if urlCol >= 0 && urlCol < len(row) {
			url = row[urlCol]
		}
		for c, val := range row {
			if outCol[c] < 0 {
				continue
			}
			name := ""
			if c < len(header) {
				name = strings.ToLower(header[c])
			}
			cell, _ := excelize.CoordinatesToCellName(outCol[c]+1, r+1)
			numFmt := ""
			switch {
			case c == titleCol:
				_ = xl.SetCellValue(*sheet, cell, val)
				if url != "" {
					_ = xl.SetCellHyperLink(*sheet, cell, trimURL(url), "External")
				}
			case c == postedCol && val != "":
				if t, perr := time.Parse("2006-01-02", val); perr == nil {
					_ = xl.SetCellValue(*sheet, cell, t)
					numFmt = "dd-mm-yyyy"
				} else {
					_ = xl.SetCellValue(*sheet, cell, val)
				}
			case salaryCol[c] && val != "":
				if fv, perr := strconv.ParseFloat(val, 64); perr == nil {
					_ = xl.SetCellValue(*sheet, cell, fv)
					numFmt = "$#,##0"
				} else {
					_ = xl.SetCellValue(*sheet, cell, val)
				}
			case numericCols[name] && val != "":
				if fv, perr := strconv.ParseFloat(val, 64); perr == nil {
					_ = xl.SetCellValue(*sheet, cell, fv)
				} else {
					_ = xl.SetCellValue(*sheet, cell, val)
				}
			default:
				_ = xl.SetCellValue(*sheet, cell, val)
			}
			_ = xl.SetCellStyle(*sheet, cell, cell, styleFor(fill, numFmt))
		}
	}

	_ = xl.SetPanes(*sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	_ = xl.AutoFilter(*sheet, "A1:"+lastHeader, []excelize.AutoFilterOptions{})
	for c, h := range header {
		if outCol[c] < 0 {
			continue
		}
		col, _ := excelize.ColumnNumberToName(outCol[c] + 1)
		w := 13.0
		if v, ok := colWidth[strings.ToLower(h)]; ok {
			w = v
		}
		_ = xl.SetColWidth(*sheet, col, col, w)
	}
	return xl.SaveAs(*out)
}

// scoreFill maps a row's score to Excel's good/neutral/bad fills, matching the
// verdict thresholds (>=0.66 real, <=0.33 ghost).
func scoreFill(row []string, scoreCol int) string {
	if scoreCol < 0 || scoreCol >= len(row) {
		return ""
	}
	sc, err := strconv.ParseFloat(row[scoreCol], 64)
	if err != nil {
		return ""
	}
	switch {
	case sc >= 0.66:
		return "C6EFCE"
	case sc <= 0.33:
		return "FFC7CE"
	default:
		return "FFEB9C"
	}
}

// trimURL drops the query string so a long tracking URL links to the clean job page.
func trimURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}
