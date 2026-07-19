// Command gui serves a local web page for editing a search profile — every
// filter and highlight threshold — and previewing it against a cached verified
// result set. It never scrapes: it saves profile.yaml (which you run through
// jobsearch) and re-filters the cache for free, so you can explore job criteria
// without committing to any.
//
//	gui --addr localhost:8080 --profile profile.yaml --cache results.cache.csv
package main

import (
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/filter"
	"github.com/AndresI19/Job-Search-Go/internal/profile"
	"github.com/AndresI19/Job-Search-Go/internal/report"
)

//go:embed index.html
var indexHTML []byte

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	profPath := flag.String("profile", "profile.yaml", "profile YAML to load and save")
	cachePath := flag.String("cache", "results.cache.csv", "verified-result cache to preview against")
	flag.Parse()

	s := &server{profPath: *profPath, cachePath: *cachePath, jobs: map[string]*jobState{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/api/profile", s.profile)
	mux.HandleFunc("/api/preview", s.preview)
	mux.HandleFunc("/api/download", s.download)
	mux.HandleFunc("/api/run", s.run)

	fmt.Printf("job-search GUI: http://%s  (profile=%s, cache=%s)\n", *addr, *profPath, *cachePath)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type server struct {
	profPath, cachePath string
	jobsMu              sync.Mutex
	jobs                map[string]*jobState
	jobSeq              atomic.Int64
}

// jobState is one search run's live progress. The mock runner drives it; a real
// Apify+Claude runner would drive the same fields, so the API and UI don't change.
type jobState struct {
	mu          sync.Mutex
	id          string
	status      string // running | done | error
	phase       string // apify | verify | done
	apifyDone   int
	apifyTotal  int
	verifyDone  int
	verifyTotal int
	rateUsed    float64 // Apify budget spent, USD
	rateLimit   float64 // Apify budget cap, USD
	errMsg      string
	header      []string
	rows        [][]string // the run's result rows, populated on completion
	cfg         report.Config
}

// snapshot renders the job's progress as JSON-ready data. Once done it also
// carries the coloured results, so the page loads them exactly like a preview.
func (j *jobState) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	m := map[string]any{
		"id": j.id, "status": j.status, "phase": j.phase,
		"apify":  map[string]int{"done": j.apifyDone, "total": j.apifyTotal},
		"verify": map[string]int{"done": j.verifyDone, "total": j.verifyTotal},
		"rate":   map[string]float64{"used": j.rateUsed, "limit": j.rateLimit},
	}
	if j.errMsg != "" {
		m["error"] = j.errMsg
	}
	if j.status == "done" {
		cols, table := report.Preview(j.header, j.rows, j.cfg, time.Now())
		m["columns"], m["rows"] = cols, table
	}
	return m
}

const (
	suiteSize   = 10    // default jobs per run when the request names none
	maxJobCount = 10000 // hard ceiling on a run's job count
)

// runReq is a run's POST body: the profile plus the requested job count.
type runReq struct {
	profile.Profile
	JobCount int `json:"job_count"`
}

// run starts a search (POST) or reports a running one's progress (GET ?id=).
func (s *server) run(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		req := runReq{Profile: profile.Default(), JobCount: suiteSize}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, err)
			return
		}
		count := req.JobCount
		if count < 1 {
			count = suiteSize
		} else if count > maxJobCount {
			count = maxJobCount
		}
		p := req.Profile
		header, data, err := s.loadCache()
		if err != nil {
			httpErr(w, err)
			return
		}
		// The suite is the profile's filtered listings, capped at the job count
		// (and, for this mock, by however many cached rows exist to replay).
		rows := filter.Apply(header, data, p.Filters, p.EstimateSalary, time.Now())
		if len(rows) > count {
			rows = rows[:count]
		}
		id := "job-" + strconv.FormatInt(s.jobSeq.Add(1), 10)
		j := &jobState{
			id: id, status: "running", phase: "apify",
			apifyTotal: len(rows), verifyTotal: len(rows),
			rateUsed: 0.19, rateLimit: 5.00, // free-plan baseline
			header: header, cfg: report.ConfigFrom(p),
		}
		s.jobsMu.Lock()
		s.jobs[id] = j
		s.jobsMu.Unlock()
		go runMock(j, rows)
		writeJSON(w, map[string]string{"id": id})
	case http.MethodGet:
		s.jobsMu.Lock()
		j := s.jobs[r.URL.Query().Get("id")]
		s.jobsMu.Unlock()
		if j == nil {
			http.Error(w, "no such job", http.StatusNotFound)
			return
		}
		writeJSON(w, j.snapshot())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// runMock simulates a run against a $0 mock: it replays the suite's cached rows
// with realistic timing so the Apify-load and post-process bars animate, without
// touching Apify or Claude. Swapping in the real pipeline means replacing this
// body with ingest → verify calls that drive the same jobState fields.
func runMock(j *jobState, rows [][]string) {
	n := len(rows)
	// Pace each phase to roughly a few seconds regardless of n, so a large suite
	// still animates rather than crawling.
	pause := 350 * time.Millisecond
	if n > 0 {
		if p := time.Duration(3500/n) * time.Millisecond; p < pause {
			pause = p
		}
		if pause < 40*time.Millisecond {
			pause = 40 * time.Millisecond
		}
	}
	for i := 1; i <= n; i++ { // Apify scrape: item count climbs as it "scrapes".
		time.Sleep(pause)
		j.mu.Lock()
		j.apifyDone = i
		j.rateUsed += 0.002 // per-result cost, mocked
		j.mu.Unlock()
	}
	j.mu.Lock()
	j.phase = "verify"
	j.mu.Unlock()
	for i := 1; i <= n; i++ { // post-process: ATS + Claude verdict, per listing.
		time.Sleep(pause)
		j.mu.Lock()
		j.verifyDone = i
		j.mu.Unlock()
	}
	j.mu.Lock()
	j.status, j.phase, j.rows = "done", "done", rows
	j.mu.Unlock()
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

// profile GETs the current profile (file, else defaults) or POSTs a new one to
// disk — the "save profile" action.
func (s *server) profile(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p := profile.Default()
		if _, err := os.Stat(s.profPath); err == nil {
			if loaded, lerr := profile.Load(s.profPath); lerr == nil {
				p = loaded
			}
		}
		writeJSON(w, p)
	case http.MethodPost:
		p, err := decodeProfile(r)
		if err != nil {
			httpErr(w, err)
			return
		}
		if err := p.Save(s.profPath); err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"saved": s.profPath})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// preview applies the posted profile to the cache and returns the filtered,
// coloured table plus a kept/total count.
func (s *server) preview(w http.ResponseWriter, r *http.Request) {
	p, err := decodeProfile(r)
	if err != nil {
		httpErr(w, err)
		return
	}
	header, data, err := s.loadCache()
	if err != nil {
		httpErr(w, err)
		return
	}
	now := time.Now()
	kept := filter.Apply(header, data, p.Filters, p.EstimateSalary, now)
	cols, table := report.Preview(header, kept, report.ConfigFrom(p), now)
	writeJSON(w, map[string]any{"columns": cols, "rows": table, "kept": len(kept), "total": len(data)})
}

// download streams the posted profile applied to the cache as an .xlsx.
func (s *server) download(w http.ResponseWriter, r *http.Request) {
	p, err := decodeProfile(r)
	if err != nil {
		httpErr(w, err)
		return
	}
	header, data, err := s.loadCache()
	if err != nil {
		httpErr(w, err)
		return
	}
	now := time.Now()
	kept := filter.Apply(header, data, p.Filters, p.EstimateSalary, now)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="results.xlsx"`)
	if err := report.WriteXLSX(w, header, kept, report.ConfigFrom(p), now); err != nil {
		httpErr(w, err)
	}
}

func (s *server) loadCache() ([]string, [][]string, error) {
	f, err := os.Open(s.cachePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open cache %s: %w (run jobsearch first)", s.cachePath, err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(rows) < 1 {
		return nil, nil, fmt.Errorf("%s has no header row", s.cachePath)
	}
	return rows[0], rows[1:], nil
}

func decodeProfile(r *http.Request) (profile.Profile, error) {
	p := profile.Default() // unspecified fields keep defaults
	err := json.NewDecoder(r.Body).Decode(&p)
	return p, err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusBadRequest)
}
