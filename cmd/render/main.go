// Command render turns a results CSV (as written by jobsearch) into a formatted
// .xlsx for review. Rather than colouring whole rows, it emphasises specific
// cells: the company when it is Fortune 500, and the salary columns (one shade
// when a salary exists, a stronger shade when the range reaches above $170k).
// The title is a trimmed clickable hyperlink, dates show dd-mm-yyyy, every cell
// is bordered, and the header is frozen and auto-filterable.
//
//	render --in results.csv --out results.xlsx
package main

import (
	_ "embed"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

//go:embed fortune500.txt
var fortune500Raw string

const highSalary = 170000 // a salary range whose max reaches this gets the stronger emphasis

// fill colours.
const (
	fillF500     = "FFE699" // gold — Fortune 500 company
	fillSalary   = "E2EFDA" // light green — a salary is published
	fillSalaryHi = "A9D08E" // stronger green — range reaches above $170k
	fillHeaderBg = "1F3B73"
	borderColor  = "D9D9D9"
)

var numericCols = map[string]bool{"score": true, "salary_min": true, "salary_max": true, "applicants": true, "years_experience": true}

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
	postedCol, urlCol, titleCol := idx("posted"), idx("url"), idx("title")
	companyCol, salMinCol, salMaxCol := idx("company"), idx("salary_min"), idx("salary_max")
	salaryCol := map[int]bool{salMinCol: true, salMaxCol: true}

	f500 := loadF500()

	// The url is folded into the title hyperlink, so it is not its own column.
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
		{Type: "left", Style: 1, Color: borderColor}, {Type: "right", Style: 1, Color: borderColor},
		{Type: "top", Style: 1, Color: borderColor}, {Type: "bottom", Style: 1, Color: borderColor},
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
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{fillHeaderBg}},
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

	for r := 1; r < len(rows); r++ {
		row := rows[r]
		url := ""
		if urlCol >= 0 && urlCol < len(row) {
			url = row[urlCol]
		}
		isF500 := companyCol >= 0 && companyCol < len(row) && f500.match(row[companyCol])
		// A salary "exists" if either bound is set; it's "high" if the max reaches the bar.
		salaryFill := ""
		if numOf(row, salMinCol) > 0 || numOf(row, salMaxCol) > 0 {
			salaryFill = fillSalary
			if numOf(row, salMaxCol) >= highSalary {
				salaryFill = fillSalaryHi
			}
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

			fill, numFmt := "", ""
			switch {
			case c == companyCol && isF500:
				fill = fillF500
			case salaryCol[c] && salaryFill != "":
				fill = salaryFill
			}

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

func numOf(row []string, col int) float64 {
	if col < 0 || col >= len(row) {
		return 0
	}
	v, _ := strconv.ParseFloat(row[col], 64)
	return v
}

func trimURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// f500Set matches company names against the embedded Fortune 500 list.
type f500Set struct{ names map[string]bool }

var legalSuffix = map[string]bool{
	"inc": true, "incorporated": true, "corp": true, "corporation": true, "co": true,
	"company": true, "llc": true, "ltd": true, "limited": true, "plc": true,
	"holdings": true, "group": true, "sa": true, "ag": true,
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// normCompany lowercases, strips punctuation and a leading "the", and drops a
// trailing legal suffix, so "Reddit, Inc." and "Reddit" normalize the same.
func normCompany(s string) string {
	words := strings.Fields(nonAlnum.ReplaceAllString(strings.ToLower(s), " "))
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	if len(words) > 1 && legalSuffix[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

func loadF500() f500Set {
	s := f500Set{names: map[string]bool{}}
	for _, line := range strings.Split(fortune500Raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if n := normCompany(line); n != "" {
			s.names[n] = true
		}
	}
	return s
}

// match reports whether company is (or starts with, on a word boundary) a
// Fortune 500 name — so "Amazon Web Services" still matches "Amazon".
func (s f500Set) match(company string) bool {
	n := normCompany(company)
	if n == "" {
		return false
	}
	if s.names[n] {
		return true
	}
	for name := range s.names {
		if strings.HasPrefix(n, name+" ") {
			return true
		}
	}
	return false
}
