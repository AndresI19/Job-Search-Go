// Package report renders a verified results table (the shared CSV representation
// from package output) for review — as a formatted .xlsx (WriteXLSX) or as a
// structured table for the GUI (Preview). Both share one set of colour rules, so
// the browser preview and the spreadsheet always agree.
//
// Rather than colouring whole rows it emphasises specific cells: the company by
// tier (Fortune 500 > high-pay software company > startup), the salary columns
// on a two-step green scale (posted and estimated pay alike), and the posted date
// on a freshness gradient. The url and confidence columns are folded away; the
// title carries a trimmed hyperlink; a colour-code key sits in columns U–Z. Every
// threshold that drives a colour comes from Config, reflecting the active profile.
package report

import (
	_ "embed"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/AndresI19/Job-Search-Go/internal/profile"
)

//go:embed fortune500.txt
var fortune500Raw string

//go:embed software.txt
var softwareRaw string

// fill colours — a fixed palette. Which cell gets which is threshold-driven.
const (
	fillF500     = "FFE699" // gold — Fortune 500
	fillSoftware = "9BC2E6" // blue — high-pay software company (not F500)
	fillStartup  = "D9C2E9" // lavender — startup (small company)
	fillSalary   = "E2EFDA" // light green — salary max reaches the light bar
	fillSalaryHi = "A9D08E" // stronger green — salary max reaches the strong bar
	fillHeaderBg = "1F3B73"
	borderColor  = "D9D9D9"

	fillFresh  = "92D050" // green — freshest
	fillRecent = "FFEB9C" // pale yellow — recent
	fillAging  = "FFC000" // amber — aging
	fillStale  = "FF9999" // red — stale
)

// Config holds the profile-driven thresholds that decide the cell colours.
type Config struct {
	SalaryLight    int
	SalaryStrong   int
	StartupMax     int
	FreshDays      int
	RecentDays     int
	AgingDays      int
	EstimateSalary bool
}

// ConfigFrom projects a profile's highlight thresholds into a render Config.
func ConfigFrom(p profile.Profile) Config {
	return Config{
		SalaryLight:    p.Highlight.SalaryLight,
		SalaryStrong:   p.Highlight.SalaryStrong,
		StartupMax:     p.Highlight.StartupMax,
		FreshDays:      p.Highlight.FreshDays,
		RecentDays:     p.Highlight.RecentDays,
		AgingDays:      p.Highlight.AgingDays,
		EstimateSalary: p.EstimateSalary,
	}
}

// cols caches the column indices and company lists the colour rules need.
type cols struct {
	title, url, posted, company    int
	salMin, salMax, estMin, estMax int
	size, industries               int
	f500, software                 companySet
}

func newCols(header []string) cols {
	idx := func(name string) int {
		for i, h := range header {
			if strings.EqualFold(h, name) {
				return i
			}
		}
		return -1
	}
	return cols{
		title: idx("title"), url: idx("url"), posted: idx("posted"), company: idx("company"),
		salMin: idx("salary_min"), salMax: idx("salary_max"),
		estMin: idx("salary_est_min"), estMax: idx("salary_est_max"),
		size: idx("company_size"), industries: idx("industries"),
		f500: loadCompanies(fortune500Raw), software: loadCompanies(softwareRaw),
	}
}

// fillFor returns the highlight for cell c of a row, or "" — the single colour
// rule shared by the spreadsheet and the preview.
func (cfg Config) fillFor(c int, row []string, cl cols, now time.Time) string {
	switch {
	case c == cl.company && c >= 0 && c < len(row):
		switch {
		case cl.f500.match(row[c]):
			return fillF500
		case cl.software.match(row[c]):
			return fillSoftware
		case cfg.isStartup(row, cl.size, cl.industries):
			return fillStartup
		}
	case c == cl.posted:
		return cfg.recencyFill(strOf(row, cl.posted), now)
	case c == cl.salMin || c == cl.salMax:
		return cfg.salaryTierFill(numOf(row, cl.salMax))
	case c == cl.estMin || c == cl.estMax:
		return cfg.salaryTierFill(numOf(row, cl.estMax))
	}
	return ""
}

// skip reports whether a column is folded away from the review view: url (folded
// into the title link), confidence (a filter input), industries (used for the
// startup cross-check but not shown), and — estimates off — the estimate columns.
func (cfg Config) skip(name string) bool {
	switch strings.ToLower(name) {
	case "url", "confidence", "industries":
		return true
	case "salary_est_min", "salary_est_max":
		return !cfg.EstimateSalary
	}
	return false
}

// WriteXLSX renders header+rows to an .xlsx written to w. now anchors recency.
func WriteXLSX(w io.Writer, header []string, rows [][]string, cfg Config, now time.Time) error {
	sheet := "Listings"
	cl := newCols(header)

	outCol := make([]int, len(header))
	oc := 0
	for c, h := range header {
		if cfg.skip(h) {
			outCol[c] = -1
			continue
		}
		outCol[c] = oc
		oc++
	}
	outN := oc

	xl := excelize.NewFile()
	defer xl.Close()
	if err := xl.SetSheetName("Sheet1", sheet); err != nil {
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
	legendLabel, _ := xl.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{Vertical: "center"}})
	legendHead, _ := xl.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Alignment: &excelize.Alignment{Vertical: "center"}})

	for c, h := range header {
		if outCol[c] < 0 {
			continue
		}
		cell, _ := excelize.CoordinatesToCellName(outCol[c]+1, 1)
		_ = xl.SetCellValue(sheet, cell, h)
	}
	lastHeader, _ := excelize.CoordinatesToCellName(outN, 1)
	_ = xl.SetCellStyle(sheet, "A1", lastHeader, headStyle)

	for r, row := range rows {
		url := strOf(row, cl.url)
		for c, val := range row {
			if c >= len(header) || outCol[c] < 0 {
				continue
			}
			name := strings.ToLower(header[c])
			cell, _ := excelize.CoordinatesToCellName(outCol[c]+1, r+2)
			fill := cfg.fillFor(c, row, cl, now)
			numFmt := ""

			switch {
			case c == cl.title:
				_ = xl.SetCellValue(sheet, cell, val)
				if url != "" {
					_ = xl.SetCellHyperLink(sheet, cell, trimURL(url), "External")
				}
			case c == cl.posted && val != "":
				if t, perr := time.Parse("2006-01-02", val); perr == nil {
					_ = xl.SetCellValue(sheet, cell, t)
					numFmt = "dd-mm-yyyy"
				} else {
					_ = xl.SetCellValue(sheet, cell, val)
				}
			case isSalaryCol(name) && val != "":
				if fv, perr := strconv.ParseFloat(val, 64); perr == nil {
					_ = xl.SetCellValue(sheet, cell, fv)
					numFmt = "$#,##0"
				} else {
					_ = xl.SetCellValue(sheet, cell, val)
				}
			case numericCols[name] && val != "":
				if fv, perr := strconv.ParseFloat(val, 64); perr == nil {
					_ = xl.SetCellValue(sheet, cell, fv)
				} else {
					_ = xl.SetCellValue(sheet, cell, val)
				}
			default:
				_ = xl.SetCellValue(sheet, cell, val)
			}
			_ = xl.SetCellStyle(sheet, cell, cell, styleFor(fill, numFmt))
		}
	}

	_ = xl.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	_ = xl.AutoFilter(sheet, "A1:"+lastHeader, []excelize.AutoFilterOptions{})
	for c, h := range header {
		if outCol[c] < 0 {
			continue
		}
		col, _ := excelize.ColumnNumberToName(outCol[c] + 1)
		width := 13.0
		if v, ok := colWidth[strings.ToLower(h)]; ok {
			width = v
		}
		_ = xl.SetColWidth(sheet, col, col, width)
	}
	cfg.writeLegend(xl, sheet, styleFor, legendLabel, legendHead)
	return xl.Write(w)
}

// PCell is one preview table cell: its display value and highlight (hex, "" none).
type PCell struct {
	Value string `json:"value"`
	Fill  string `json:"fill"`
}

// PRow is one preview row: the posting URL (for the title link) and its cells,
// aligned to the visible columns.
type PRow struct {
	URL   string  `json:"url"`
	Cells []PCell `json:"cells"`
}

// Preview applies the same colour rules as WriteXLSX and returns a table for the
// GUI: the visible column names and, per row, a cell per column.
func Preview(header []string, rows [][]string, cfg Config, now time.Time) (columns []string, table []PRow) {
	cl := newCols(header)
	var vis []int
	for c, h := range header {
		if !cfg.skip(h) {
			vis = append(vis, c)
			columns = append(columns, h)
		}
	}
	for _, row := range rows {
		pr := PRow{URL: trimURL(strOf(row, cl.url)), Cells: make([]PCell, 0, len(vis))}
		for _, c := range vis {
			pr.Cells = append(pr.Cells, PCell{Value: strOf(row, c), Fill: cfg.fillFor(c, row, cl, now)})
		}
		table = append(table, pr)
	}
	return columns, table
}

func isSalaryCol(name string) bool {
	switch name {
	case "salary_min", "salary_max", "salary_est_min", "salary_est_max":
		return true
	}
	return false
}

var numericCols = map[string]bool{
	"score": true, "salary_min": true, "salary_max": true, "salary_est_min": true,
	"salary_est_max": true, "applicants": true, "years_experience": true, "company_size": true,
}

var colWidth = map[string]float64{
	"title": 40, "company": 22, "industries": 30, "location": 22,
	"reasoning": 90, "verified_via": 26, "coverage": 18, "years_experience": 10,
}

// salaryTierFill maps a salary max to its highlight: the stronger green at the
// strong bar, the lighter green at the light bar, none below. Posted and
// estimated salaries share this one scale — the column tells them apart.
func (c Config) salaryTierFill(max float64) string {
	switch {
	case max >= float64(c.SalaryStrong):
		return fillSalaryHi
	case max >= float64(c.SalaryLight):
		return fillSalary
	default:
		return ""
	}
}

// isStartup reports whether a company is a small product company: headcount in
// (0, StartupMax) and not a staffing/consulting body-shop by industry.
func (c Config) isStartup(row []string, sizeCol, indCol int) bool {
	s := numOf(row, sizeCol)
	if !(s > 0 && s < float64(c.StartupMax)) {
		return false
	}
	return !isConsultingShop(strOf(row, indCol))
}

// recencyFill maps a posted date (yyyy-mm-dd) to a freshness colour, greenest
// when new and reddening as it ages past the aging bar.
func (c Config) recencyFill(posted string, now time.Time) string {
	if posted == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02", posted)
	if err != nil {
		return ""
	}
	switch days := int(now.Sub(t).Hours() / 24); {
	case days <= c.FreshDays:
		return fillFresh
	case days <= c.RecentDays:
		return fillRecent
	case days <= c.AgingDays:
		return fillAging
	default:
		return fillStale
	}
}

// writeLegend draws the colour-code key in columns U–Z, to the right of the data.
// Threshold labels reflect the active Config.
func (c Config) writeLegend(xl *excelize.File, sheet string, swatch func(fill, numFmt string) int, label, head int) {
	k := func(n int) string { return fmt.Sprintf("$%dk+", n/1000) }
	type lr struct {
		fill, text string
		header     bool
	}
	spec := []lr{
		{"", "COLOUR LEGEND", true},
		{"", "Company", true},
		{fillF500, "Fortune 500", false},
		{fillSoftware, "High-pay software (not Fortune 500)", false},
		{fillStartup, "Startup (small company)", false},
		{"", "", false},
		{"", "Salary max (posted or estimated)", true},
		{fillSalary, "Reaches " + k(c.SalaryLight), false},
		{fillSalaryHi, "Reaches " + k(c.SalaryStrong), false},
		{"", "", false},
		{"", "Posted (recency)", true},
		{fillFresh, fmt.Sprintf("Within %d days", c.FreshDays), false},
		{fillRecent, fmt.Sprintf("Within %d days", c.RecentDays), false},
		{fillAging, fmt.Sprintf("Within %d days", c.AgingDays), false},
		{fillStale, fmt.Sprintf("Older than %d days", c.AgingDays), false},
	}
	for i, r := range spec {
		rowNo := i + 1
		u, _ := excelize.CoordinatesToCellName(21, rowNo)
		v, _ := excelize.CoordinatesToCellName(22, rowNo)
		z, _ := excelize.CoordinatesToCellName(26, rowNo)
		switch {
		case r.header:
			_ = xl.MergeCell(sheet, u, z)
			_ = xl.SetCellValue(sheet, u, r.text)
			_ = xl.SetCellStyle(sheet, u, z, head)
		case r.text == "":
		default:
			_ = xl.SetCellStyle(sheet, u, u, swatch(r.fill, ""))
			_ = xl.MergeCell(sheet, v, z)
			_ = xl.SetCellValue(sheet, v, r.text)
			_ = xl.SetCellStyle(sheet, v, z, label)
		}
	}
	_ = xl.SetColWidth(sheet, "U", "U", 4)
}

func numOf(row []string, col int) float64 {
	if col < 0 || col >= len(row) {
		return 0
	}
	v, _ := strconv.ParseFloat(row[col], 64)
	return v
}

func strOf(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return row[col]
}

func trimURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// companySet matches company names against an embedded list.
type companySet struct{ names map[string]bool }

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

func loadCompanies(raw string) companySet {
	s := companySet{names: map[string]bool{}}
	for _, line := range strings.Split(raw, "\n") {
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

// match reports whether company is (or starts with, on a word boundary) a listed
// name — so "Amazon Web Services" still matches "Amazon".
func (s companySet) match(company string) bool {
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

// isConsultingShop reports whether an industry label marks an agency/body-shop
// rather than a product company. A company also tagged "Software Development" is
// treated as a product startup despite any consulting tag.
func isConsultingShop(industries string) bool {
	l := strings.ToLower(industries)
	if l == "" || strings.Contains(l, "software development") {
		return false
	}
	for _, kw := range []string{"staffing", "recruiting", "consulting", "human resources", "outsourcing"} {
		if strings.Contains(l, kw) {
			return true
		}
	}
	return false
}
