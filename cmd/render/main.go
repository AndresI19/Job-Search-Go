// Command render turns a results CSV (as written by jobsearch) into a
// color-coded .xlsx: each row is tinted by its `confidence` verdict, the header
// row is frozen and filterable, and numeric columns are written as numbers so
// the sheet sorts and filters correctly in any spreadsheet app — e.g. filter
// `location` to Remote or a metro, or sort by `salary_min`.
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

	"github.com/xuri/excelize/v2"
)

// numericCols are written as numbers (not text) so they sort/filter correctly.
var numericCols = map[string]bool{"score": true, "salary_min": true, "salary_max": true, "applicants": true}

// verdictFill maps a confidence value to a fill colour (Excel's good/neutral/bad).
var verdictFill = map[string]string{"likely-real": "C6EFCE", "uncertain": "FFEB9C", "likely-ghost": "FFC7CE"}

// colWidth overrides the default width for a few wide/narrow columns.
var colWidth = map[string]float64{"title": 34, "company": 22, "location": 22, "reasoning": 90, "url": 52, "verified_via": 26, "coverage": 18, "salary_min": 12, "salary_max": 12}

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

	confCol := -1
	for i, h := range header {
		if strings.EqualFold(h, "confidence") {
			confCol = i
		}
	}

	xl := excelize.NewFile()
	defer xl.Close()
	if err := xl.SetSheetName("Sheet1", *sheet); err != nil {
		return err
	}

	headStyle, _ := xl.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"1F3B73"}},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	rowStyle := map[string]int{}
	for verdict, hex := range verdictFill {
		id, _ := xl.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{hex}}, Alignment: &excelize.Alignment{Vertical: "top"}})
		rowStyle[verdict] = id
	}
	plain, _ := xl.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{Vertical: "top"}})

	// Write cells; numeric columns become real numbers.
	for r, row := range rows {
		for c, val := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if r > 0 && c < len(header) && val != "" && numericCols[strings.ToLower(header[c])] {
				if fv, perr := strconv.ParseFloat(val, 64); perr == nil {
					_ = xl.SetCellValue(*sheet, cell, fv)
					continue
				}
			}
			_ = xl.SetCellValue(*sheet, cell, val)
		}
	}

	// Header: styled, frozen, filterable.
	lastHeader, _ := excelize.CoordinatesToCellName(len(header), 1)
	_ = xl.SetCellStyle(*sheet, "A1", lastHeader, headStyle)
	_ = xl.SetPanes(*sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	_ = xl.AutoFilter(*sheet, "A1:"+lastHeader, []excelize.AutoFilterOptions{})

	// Tint each data row by its verdict.
	if confCol >= 0 {
		for r := 1; r < len(rows); r++ {
			style := plain
			if id, ok := rowStyle[strings.ToLower(rows[r][confCol])]; ok {
				style = id
			}
			a, _ := excelize.CoordinatesToCellName(1, r+1)
			z, _ := excelize.CoordinatesToCellName(len(header), r+1)
			_ = xl.SetCellStyle(*sheet, a, z, style)
		}
	}

	// Column widths.
	for i, h := range header {
		col, _ := excelize.ColumnNumberToName(i + 1)
		w := 13.0
		if v, ok := colWidth[strings.ToLower(h)]; ok {
			w = v
		}
		_ = xl.SetColWidth(*sheet, col, col, w)
	}

	return xl.SaveAs(*out)
}
