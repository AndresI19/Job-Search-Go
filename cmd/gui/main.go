// Command gui serves a local web page for editing a search profile — every
// filter and highlight threshold — and previewing it against a cached verified
// result set. It never scrapes: it saves profile.yaml (which you run through
// jobsearch) and re-filters the cache for free, so you can explore job criteria
// without committing to any.
//
//	gui --addr localhost:8080 --profile profile.yaml --cache results.cache.csv
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/apify"
	"github.com/AndresI19/Job-Search-Go/internal/ats"
	"github.com/AndresI19/Job-Search-Go/internal/filter"
	"github.com/AndresI19/Job-Search-Go/internal/greenhouse"
	"github.com/AndresI19/Job-Search-Go/internal/judge"
	"github.com/AndresI19/Job-Search-Go/internal/lever"
	"github.com/AndresI19/Job-Search-Go/internal/linkedin"
	"github.com/AndresI19/Job-Search-Go/internal/output"
	"github.com/AndresI19/Job-Search-Go/internal/pipeline"
	"github.com/AndresI19/Job-Search-Go/internal/profile"
	"github.com/AndresI19/Job-Search-Go/internal/report"
	"github.com/AndresI19/Job-Search-Go/internal/score"
	"github.com/AndresI19/Job-Search-Go/internal/watchlist"
)

//go:embed index.html
var indexHTML []byte

// defaultLinkedInActor is the public LinkedIn scraper Actor used when
// APIFY_ACTOR_ID is unset (matches the CLI).
const defaultLinkedInActor = "hKByXkMQaC5Qt9UMN"

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	profPath := flag.String("profile", "profile.yaml", "profile YAML to load and save")
	cachePath := flag.String("cache", "results.cache.csv", "verified-result cache to preview against")
	flag.Parse()

	s := &server{profPath: *profPath, cachePath: *cachePath, jobs: map[string]*jobState{}}
	// Wire the real pipeline if the environment allows it. Runs pick mock vs real
	// per request from the corner profile switch (Guest=mock, Admin=real); this
	// just makes the real path AVAILABLE. Best-effort — without it, Admin falls
	// back to the mock rather than the server failing to start.
	if err := s.enableLive(); err != nil {
		fmt.Fprintf(os.Stderr, "note: Admin (real) runs unavailable — %v; Admin will fall back to mock\n", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/api/config", s.config)
	mux.HandleFunc("/api/profile", s.profile)
	mux.HandleFunc("/api/preview", s.preview)
	mux.HandleFunc("/api/download", s.download)
	mux.HandleFunc("/api/run", s.run)
	mux.HandleFunc("/api/export", s.export)
	mux.HandleFunc("/api/import", s.importResults)

	fmt.Printf("job-search GUI: http://%s  (cache=%s, %s)\n", *addr, *cachePath, s.modeLine())
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// modeLine describes, for the startup log, what Guest and Admin runs will do.
func (s *server) modeLine() string {
	switch {
	case !s.realReady:
		return "Guest & Admin both mock ($0) — set APIFY_TOKEN for real Admin runs"
	case s.spends:
		return "Guest=mock ($0), Admin=REAL Apify+Claude (SPENDS)"
	default:
		return "Guest=mock ($0), Admin=real path via mock backends ($0)"
	}
}

// config tells the UI whether Admin (real) runs are available and whether they
// spend (for the corner profile switch), plus the profession catalog the search
// multiselect renders.
func (s *server) config(w http.ResponseWriter, r *http.Request) {
	var fields []map[string]any
	for _, f := range fieldCatalog {
		roles := make([]map[string]any, len(f.Roles))
		for i, r := range f.Roles {
			roles[i] = map[string]any{"key": r.Key, "label": r.Label, "match": r.Match}
		}
		fields = append(fields, map[string]any{"key": f.Key, "label": f.Label, "roles": roles})
	}
	locs := make([]map[string]any, len(locationCatalog))
	for i, l := range locationCatalog {
		locs[i] = map[string]any{"key": l.Key, "label": l.Label, "match": l.Match}
	}
	writeJSON(w, map[string]any{"realReady": s.realReady, "spends": s.spends, "fields": fields, "locations": locs})
}

// enableLive wires the real ingest+verify dependencies from the environment:
// APIFY_TOKEN (required), APIFY_BASE_URL (optional mock/proxy), APIFY_ACTOR_ID
// (optional), and the JUDGE_* config (JUDGE_BACKEND=mock keeps it $0 for testing).
// It also records whether real runs would actually spend (real, non-mock backends).
func (s *server) enableLive() error {
	token := os.Getenv("APIFY_TOKEN")
	if token == "" {
		return fmt.Errorf("APIFY_TOKEN is not set")
	}
	jd, err := judge.FromEnv()
	if err != nil {
		return err
	}
	var opts []apify.Option
	if base := os.Getenv("APIFY_BASE_URL"); base != "" {
		opts = append(opts, apify.WithBaseURL(base))
	}
	s.realReady = true
	s.spends = os.Getenv("APIFY_BASE_URL") == "" && os.Getenv("JUDGE_BACKEND") != "mock"
	s.actorID = envOr("APIFY_ACTOR_ID", defaultLinkedInActor)
	s.apify = apify.New(token, opts...)
	s.resolver = ats.NewResolver(ats.NewCached(greenhouse.New()), ats.NewCached(lever.New()))
	s.judge = jd
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type server struct {
	profPath, cachePath string
	// Real-run dependencies — nil unless the environment enabled them.
	realReady bool
	spends    bool // true only when real runs use real (non-mock) backends
	actorID   string
	apify     *apify.Client
	resolver  *ats.Resolver
	judge     judge.Judge

	jobsMu sync.Mutex
	jobs   map[string]*jobState
	jobSeq atomic.Int64

	// lastRows is the most recent result set (preview or run), kept so it can be
	// exported to a portable CSV and re-imported later.
	lastMu     sync.Mutex
	lastHeader []string
	lastRows   [][]string
}

// setLast records the current result set for a later export.
func (s *server) setLast(header []string, rows [][]string) {
	s.lastMu.Lock()
	s.lastHeader, s.lastRows = header, rows
	s.lastMu.Unlock()
}

// export writes the last result set as a CSV download — the full verified rows,
// so an exported file re-imports (and could be fed to the CLI) without loss.
func (s *server) export(w http.ResponseWriter, r *http.Request) {
	s.lastMu.Lock()
	rows := s.lastRows
	s.lastMu.Unlock()
	if len(rows) == 0 {
		http.Error(w, "no results to export yet — preview or run first", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="job-search-results.csv"`)
	if err := output.WriteRows(w, rows); err != nil {
		httpErr(w, err)
	}
}

// importResults reads a previously-exported results CSV (raw body), makes it the
// current set, and returns it through the same preview pipeline so it renders
// exactly like a fresh result.
func (s *server) importResults(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		httpErr(w, err)
		return
	}
	recs, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		httpErr(w, fmt.Errorf("not a valid results CSV: %w", err))
		return
	}
	if len(recs) < 1 {
		httpErr(w, fmt.Errorf("the file has no header row"))
		return
	}
	header, rows := recs[0], recs[1:]
	s.setLast(header, rows)
	cols, table := report.Preview(header, rows, report.ConfigFrom(profile.Default()), time.Now())
	writeJSON(w, map[string]any{"columns": cols, "rows": table, "kept": len(rows), "total": len(rows)})
}

// jobState is one search run's live progress. The mock runner drives it; a real
// Apify+Claude runner would drive the same fields, so the API and UI don't change.
type jobState struct {
	mu          sync.Mutex
	id          string
	spends      bool   // whether this run actually spends (real backends)
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
		"id": j.id, "status": j.status, "phase": j.phase, "spends": j.spends,
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

// runReq is a run's POST body: the profile, the requested job count, the selected
// field (mapped to a curated all-roles keyword query), and the role.
type runReq struct {
	profile.Profile
	JobCount int    `json:"job_count"`
	Field    string `json:"field"`
	Role     string `json:"role"` // "admin" → real pipeline (if available); anything else → mock
}

// profRole is one role within a field: the LinkedIn keyword query it contributes
// to the field's search, and Match — lowercase substrings that classify a listing
// title into this role for the results' Role column.
type profRole struct {
	Key, Label, Query string
	Match             []string
}

// fieldCatalog groups roles under a field. A field's search is ALL its roles OR'd
// together — you pick the field, not individual roles. Only Software is supported
// for now; Legal (and others) slot in as additional entries with their own roles,
// and the UI, query, and classification handle them without further change.
var fieldCatalog = []struct {
	Key, Label string
	Roles      []profRole
}{
	{"software", "Software", []profRole{
		{"software-engineer", "Software Engineer", `"Software Engineer"`, []string{"software engineer", "swe", "developer", "engineer"}},
		{"backend", "Backend", `"Backend Engineer"`, []string{"backend", "back-end", "back end"}},
		{"frontend", "Frontend", `"Frontend Engineer"`, []string{"frontend", "front-end", "front end"}},
		{"fullstack", "Full-Stack", `"Full Stack Engineer"`, []string{"full stack", "full-stack", "fullstack"}},
		{"platform", "Platform / Infra", `"Platform Engineer" OR "Infrastructure Engineer"`, []string{"platform", "infrastructure", "infra"}},
		{"devops-sre", "DevOps / SRE", `"DevOps Engineer" OR "Site Reliability Engineer"`, []string{"devops", "site reliability", "sre"}},
		{"data-engineer", "Data Engineer", `"Data Engineer"`, []string{"data engineer"}},
		{"ml", "ML / AI", `"Machine Learning Engineer" OR "AI Engineer"`, []string{"machine learning", "ml engineer", "ai engineer"}},
		{"data-scientist", "Data Scientist", `"Data Scientist"`, []string{"data scientist"}},
		{"security", "Security", `"Security Engineer"`, []string{"security"}},
		{"mobile", "Mobile", `"iOS Engineer" OR "Android Engineer"`, []string{"ios", "android", "mobile"}},
	}},
	// {"legal", "Legal", []profRole{ {"attorney","Attorney",`"Attorney"`,[]string{"attorney"}}, ... }},
}

// fieldQuery is one LinkedIn keyword search covering ALL of a field's roles (OR'd),
// defaulting to the first field when the key is unknown so a run is never empty.
func fieldQuery(key string) string {
	roles := fieldCatalog[0].Roles
	for _, f := range fieldCatalog {
		if f.Key == key {
			roles = f.Roles
			break
		}
	}
	parts := make([]string, len(roles))
	for i, r := range roles {
		parts[i] = r.Query
	}
	return strings.Join(parts, " OR ")
}

// locationCatalog is the explicitly-supported locations. Each maps a set of
// raw-location substrings to one label — the location select box, the filter, and
// the display normalization all key off it, so a place is handled by explicit
// support rather than an arbitrary heuristic.
var locationCatalog = []struct {
	Key, Label string
	Match      []string
}{
	{"new-york", "New York", []string{"new york", "nyc", "manhattan", "brooklyn"}},
	{"boston", "Boston", []string{"boston", "cambridge"}},
	{"los-angeles", "Los Angeles", []string{"los angeles", "marina del rey", "huntington beach"}},
	{"san-francisco", "San Francisco", []string{"san francisco", "bay area", "palo alto", "mountain view", "menlo park", "san jose", "silicon valley"}},
	{"seattle", "Seattle", []string{"seattle", "bellevue", "redmond"}},
	{"chicago", "Chicago", []string{"chicago"}},
	{"austin", "Austin", []string{"austin"}},
	{"denver", "Denver", []string{"denver"}},
	{"washington-dc", "Washington DC", []string{"washington", "arlington", "baltimore"}},
	{"atlanta", "Atlanta", []string{"atlanta"}},
	{"united-states", "United States", []string{"united states"}},
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
		// The corner profile switch picks the path: Admin → the real pipeline (only
		// when the environment made it available), everyone else → the mock.
		real := s.realReady && strings.EqualFold(req.Role, "admin")
		id := "job-" + strconv.FormatInt(s.jobSeq.Add(1), 10)
		j := &jobState{
			id: id, spends: real && s.spends, status: "running", phase: "apify",
			apifyTotal: count, verifyTotal: count,
			rateUsed: 0.19, rateLimit: 5.00, // free-plan baseline
			cfg: report.ConfigFrom(p),
		}

		if real {
			j.header = output.Header()
			s.jobsMu.Lock()
			s.jobs[id] = j
			s.jobsMu.Unlock()
			go s.runReal(j, fieldQuery(req.Field), p, count)
		} else {
			header, data, lerr := s.loadCache()
			if lerr != nil {
				httpErr(w, lerr)
				return
			}
			// The mock replays the profile's filtered cached rows, capped at the
			// job count and bounded by the cache size.
			rows := filter.Apply(header, data, p.Filters, p.EstimateSalary, time.Now())
			if len(rows) > count {
				rows = rows[:count]
			}
			j.header = header
			j.apifyTotal, j.verifyTotal = len(rows), len(rows)
			s.jobsMu.Lock()
			s.jobs[id] = j
			s.jobsMu.Unlock()
			go s.runMock(j, rows)
		}
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
func (s *server) runMock(j *jobState, rows [][]string) {
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
	s.setLast(j.header, rows)
}

// runReal drives the actual pipeline, updating the same jobState the mock does:
// build the search URL from keywords + filters, start the Apify scrape and poll
// its dataset item-count for the Apify-load bar, normalize, verify (ATS + Claude)
// with a per-listing callback for the post-process bar, apply the profile's
// filters, and read the account's Apify usage for the rate bar.
func (s *server) runReal(j *jobState, keywords string, p profile.Profile, count int) {
	ctx := context.Background()
	fail := func(msg string) {
		j.mu.Lock()
		j.status, j.errMsg = "error", msg
		j.mu.Unlock()
	}

	q := watchlist.Query{
		Field: keywords, MaxAgeDays: p.Filters.MaxAgeDays,
		Remote: p.Filters.RemoteOK, SalaryMin: p.Filters.MinSalary,
	}
	if len(p.Filters.Locations) > 0 {
		q.Location = p.Filters.Locations[0]
	}
	input := map[string]any{"urls": []string{q.SearchURL()}, "count": count, "scrapeCompany": true}

	started, err := s.apify.StartRun(ctx, s.actorID, input)
	if err != nil {
		fail("start scrape: " + err.Error())
		return
	}
	// Poll the dataset item-count for the Apify-load bar while the run runs.
	for {
		if cnt, e := s.apify.DatasetInfo(ctx, started.DefaultDatasetID); e == nil {
			if cnt > count {
				cnt = count
			}
			j.mu.Lock()
			j.apifyDone = cnt
			j.mu.Unlock()
		}
		st, e := s.apify.RunStatus(ctx, started.ID)
		if e != nil {
			fail("poll run: " + e.Error())
			return
		}
		if st.Status == "SUCCEEDED" {
			break
		}
		if st.Status == "FAILED" || st.Status == "ABORTED" || st.Status == "TIMED-OUT" {
			fail("scrape ended " + st.Status)
			return
		}
		time.Sleep(2 * time.Second)
	}

	raw, err := s.apify.DatasetItems(ctx, started.DefaultDatasetID)
	if err != nil {
		fail("fetch dataset: " + err.Error())
		return
	}
	listings := linkedin.Normalize(raw)
	j.mu.Lock()
	j.apifyDone, j.phase, j.verifyTotal, j.verifyDone = j.apifyTotal, "verify", len(listings), 0
	j.mu.Unlock()

	var done int64
	results := pipeline.Verify(ctx, listings, s.resolver, s.judge, score.DefaultWeights(), 8, nil, func() {
		n := atomic.AddInt64(&done, 1)
		j.mu.Lock()
		j.verifyDone = int(n)
		j.mu.Unlock()
	})

	rows := filter.Apply(output.Header(), output.Rows(results), p.Filters, p.EstimateSalary, time.Now())
	used, limit, _ := s.apify.Usage(ctx)

	j.mu.Lock()
	j.rows = rows
	if limit > 0 {
		j.rateUsed, j.rateLimit = used, limit
	}
	j.status, j.phase = "done", "done"
	j.mu.Unlock()
	s.setLast(j.header, rows)
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
	s.setLast(header, kept)
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
