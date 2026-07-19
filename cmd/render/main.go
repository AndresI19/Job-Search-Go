// Command render turns a results CSV (as written by jobsearch) into a formatted
// .xlsx for review. The colours are driven by a profile (--profile); without one
// it uses the built-in defaults.
//
//	render --in results.csv --out results.xlsx [--profile profile.yaml]
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/profile"
	"github.com/AndresI19/Job-Search-Go/internal/report"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	in := flag.String("in", "results.csv", "input results CSV")
	out := flag.String("out", "results.xlsx", "output xlsx path")
	profPath := flag.String("profile", "", "profile YAML (default: built-in defaults)")
	flag.Parse()

	prof := profile.Default()
	if *profPath != "" {
		p, err := profile.Load(*profPath)
		if err != nil {
			return err
		}
		prof = p
	}

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

	outF, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer outF.Close()
	return report.WriteXLSX(outF, rows[0], rows[1:], report.ConfigFrom(prof), time.Now())
}
