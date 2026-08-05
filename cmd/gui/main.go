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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AndresI19/Job-Search-Go/internal/apify"
	"github.com/AndresI19/Job-Search-Go/internal/ats"
	"github.com/AndresI19/Job-Search-Go/internal/auth"
	"github.com/AndresI19/Job-Search-Go/internal/db"
	"github.com/AndresI19/Job-Search-Go/internal/filter"
	"github.com/AndresI19/Job-Search-Go/internal/greenhouse"
	"github.com/AndresI19/Job-Search-Go/internal/judge"
	"github.com/AndresI19/Job-Search-Go/internal/lever"
	"github.com/AndresI19/Job-Search-Go/internal/model"
	"github.com/AndresI19/Job-Search-Go/internal/output"
	"github.com/AndresI19/Job-Search-Go/internal/pipeline"
	"github.com/AndresI19/Job-Search-Go/internal/profile"
	"github.com/AndresI19/Job-Search-Go/internal/report"
	"github.com/AndresI19/Job-Search-Go/internal/score"
	"github.com/AndresI19/Job-Search-Go/internal/secret"
	"github.com/AndresI19/Job-Search-Go/internal/source"
	"github.com/AndresI19/Job-Search-Go/internal/summarize"
	"github.com/AndresI19/Job-Search-Go/internal/watchlist"
)

//go:embed index.html
var indexHTML []byte

func main() {
	// Flags stay for local use; each falls back to an env var so the container can
	// configure the same knobs the platform way (the service chart sets env, not
	// args). ADDR must be 0.0.0.0:<port> in a pod — the localhost default would
	// refuse traffic arriving from the Service.
	addr := flag.String("addr", envOr("ADDR", "localhost:8080"), "listen address")
	profPath := flag.String("profile", envOr("PROFILE_PATH", "profile.yaml"), "profile YAML to load and save")
	cachePath := flag.String("cache", envOr("CACHE_PATH", "results.cache.csv"), "verified-result cache to preview against")
	flag.Parse()

	base := normBase(os.Getenv("BASE_PATH"))
	s := &server{
		profPath: *profPath, cachePath: *cachePath, base: base,
		appVersion: readVersion(), jobs: map[string]*jobState{}, appJobs: map[string]*applicatorJob{},
		demoRuns: map[string]time.Time{},
	}

	// Platform identity. In-cluster (AUTH_JWKS_URI set) admin is a verified signed
	// JWT claim; unset means local dev, where the corner-switch role is trusted
	// instead. A configured-but-unreachable JWKS returns a deny-all verifier (and
	// an error we log) rather than nil, so we never silently fall back to the
	// spoofable local path when auth was actually requested.
	v, err := auth.FromEnv(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth: JWKS load failed — real (admin) runs are locked until platform-auth is reachable and the pod restarts: %v\n", err)
	}
	s.auth = v

	// Persistence. With DATABASE_URL set (in-cluster) real runs accumulate into the
	// aggregate table and Saved is server-backed; without it (local dev / no-DB
	// deploy) the DB is disabled and the service behaves exactly as before — mock
	// preview + browser localStorage. A configured-but-unreachable DB is logged, not
	// fatal: the mock/preview must still serve.
	database, derr := db.Open(context.Background())
	if derr != nil {
		fmt.Fprintf(os.Stderr, "db: persistence unavailable — %v; running without it\n", derr)
	} else if merr := database.Migrate(context.Background()); merr != nil {
		fmt.Fprintf(os.Stderr, "db: migrate failed — %v; running without persistence\n", merr)
		database = nil
	}
	s.db = database

	// Wire the real pipeline if the environment allows it. Runs pick mock vs real
	// per request; this just makes the real path AVAILABLE. Best-effort — without
	// it, a real request falls back to the mock rather than the server failing.
	if err := s.enableLive(); err != nil {
		fmt.Fprintf(os.Stderr, "note: Admin (real) runs unavailable — %v; Admin will fall back to mock\n", err)
	}
	// Safety invariant: paid backends (Apify scrape, Gemini verify + summaries) may only run
	// when platform auth is configured — otherwise "admin" is just an unverified role claim
	// anyone could spoof to spend the keys. Without auth we stay mock-only, UNLESS an operator
	// explicitly opts in for trusted local testing (ALLOW_UNAUTHENTICATED_REAL=1).
	if s.realReady && s.auth == nil && !unauthedRealAllowed() {
		fmt.Fprintf(os.Stderr, "SECURITY: real pipeline disabled — AUTH_JWKS_URI is not configured, so admin cannot be verified. Set it to enable live runs (or ALLOW_UNAUTHENTICATED_REAL=1 for trusted local testing).\n")
		s.realReady = false
	} else if s.realReady && s.auth == nil {
		fmt.Fprintf(os.Stderr, "WARNING: real pipeline enabled WITHOUT auth (ALLOW_UNAUTHENTICATED_REAL=1) — admin is unverified; trusted local testing only.\n")
	}

	// Applicator summaries (Gemini). Independent of Apify — needs only the DB and a
	// summarizer backend (SUMMARIZE_BACKEND, else JUDGE_BACKEND). Best-effort: a
	// launch just reports "unavailable" if this failed to wire.
	s.sumModel = envOr("SUMMARIZE_MODEL", envOr("JUDGE_MODEL", "claude-haiku-4-5"))
	if sm, serr := summarize.FromEnv(); serr != nil {
		fmt.Fprintf(os.Stderr, "note: Applicator summaries unavailable — %v\n", serr)
	} else {
		s.summarizer = sm
	}

	mux := http.NewServeMux()
	mux.HandleFunc(base+"api/config", s.config)
	mux.HandleFunc(base+"api/profile", s.profile)
	mux.HandleFunc(base+"api/preview", s.preview)
	mux.HandleFunc(base+"api/download", s.download)
	mux.HandleFunc(base+"api/run", s.run)
	mux.HandleFunc(base+"api/run/stream", s.runStream)
	mux.HandleFunc(base+"api/export", s.export)
	mux.HandleFunc(base+"api/import", s.importResults)
	mux.HandleFunc(base+"api/listings", s.listings)
	mux.HandleFunc(base+"api/results", s.results) // typed domain-model contract (Jobomancer Scry grid)
	mux.HandleFunc(base+"api/refresh", s.refresh)
	mux.HandleFunc(base+"api/saved", s.saved)
	mux.HandleFunc(base+"api/codex", s.codex)
	mux.HandleFunc(base+"api/applicator/launch", s.applicatorLaunch)
	mux.HandleFunc(base+"api/applicator/status", s.applicatorStatus)
	mux.HandleFunc(base+"api/applicator/trash", s.trash)
	mux.HandleFunc(base+"api/applicator/restore", s.restore)
	mux.HandleFunc(base+"api/applicator", s.applicator)
	mux.HandleFunc(base+"api/health", s.health)
	mux.HandleFunc(base+"version", s.version)
	mux.Handle(base, s.static())

	fmt.Printf("job-search GUI: http://%s%s  (cache=%s, %s)\n", *addr, base, *cachePath, s.modeLine())
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// normBase normalises BASE_PATH to a leading+trailing-slash prefix ("/" when
// unset). Every route and the static handler mount beneath it, so the app serves
// identically at "/" locally and at "/job-searcher/" behind the platform router.
func normBase(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// readVersion returns the deployed version: APP_VERSION env, else the VERSION file
// the Docker build writes (/app/VERSION), else "" — a dev build, reported as
// "snapshot" so it can never claim to be a release.
func readVersion() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	for _, p := range []string{"/app/VERSION", "VERSION"} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return ""
}

// modeLine describes, for the startup log, what Guest and Admin runs will do.
func (s *server) modeLine() string {
	switch {
	case !s.realReady:
		return "Guest & Admin both mock ($0) — set APIFY_TOKEN for real Admin runs"
	case s.spends:
		return "Guest=mock ($0); signed-in=REAL (1 scan/wk, SPENDS Apify); Admin=REAL unbounded"
	default:
		return "Guest=mock ($0); signed-in & Admin=real path via mock backends ($0)"
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
	// Env first (local / .env), else the mounted secret file (in-cluster), so the
	// Apify token never has to live in the pod's environment.
	token := secret.Value("APIFY_TOKEN", "APIFY_TOKEN_FILE", "/etc/.secrets/apify-token")
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
	base                string         // normalised BASE_PATH ("/" or "/job-searcher/")
	appVersion          string         // deployed version, served from <base>version
	auth                *auth.Verifier // nil = no platform auth (local dev)
	db                  *db.DB         // nil/disabled = no persistence (mock + localStorage only)
	// Real-run dependencies — nil unless the environment enabled them.
	realReady  bool
	spends     bool // true only when real runs use real (non-mock) backends
	apify      *apify.Client
	resolver   *ats.Resolver
	judge      judge.Judge
	summarizer summarize.Summarizer // nil = Applicator summaries unavailable
	sumModel   string               // model id summaries are tagged with

	jobsMu sync.Mutex
	jobs   map[string]*jobState
	jobSeq atomic.Int64

	// Demo scan quota: non-admins get one scan per demoRunWindow (keyed by user id or
	// IP). In-memory and per-process — a restart or a second replica resets it, which is
	// fine: non-admin scans are the $0 mock, so this bounds load, not spend.
	demoMu   sync.Mutex
	demoRuns map[string]time.Time

	appMu   sync.Mutex
	appJobs map[string]*applicatorJob

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
// Apify+Gemini runner would drive the same fields, so the API and UI don't change.
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
	rows        [][]string  // the run's result rows, populated on completion
	stream      []resultDTO // verified rows appended AS they complete, for live SSE streaming
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

// perBoardMax is how many results each board is asked for per run. Both LinkedIn
// and Indeed cap a public search near this anyway, so every run just pulls the
// ceiling from every source — there is no user-facing job-count knob.
const perBoardMax = 1000

// runReq is a run's POST body: the profile, the requested job count, the selected
// field (mapped to a curated all-roles keyword query), the role, and how many
// locations were selected (drives the per-location job-count ceiling).
type runReq struct {
	profile.Profile
	Field string `json:"field"`
	Role  string `json:"role"` // "admin" → real pipeline (if available); anything else → mock
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

// locationCatalog is the explicitly-supported locations, keyed by STATE. Each maps
// a set of raw-location substrings to one state label — the location select box, the
// filter, and the display normalization all key off it. A state matches on its full
// name, its ", XX" code (comma-prefixed so it can't false-match a city like Tacoma),
// or any of its metros; all substrings are lowercase (raw values are lowercased
// before compare).
var locationCatalog = []struct {
	Key, Label string
	Match      []string
}{
	{"ma", "Massachusetts", []string{"massachusetts", ", ma", "boston", "cambridge", "somerville"}},
	{"ny", "New York", []string{"new york", ", ny", "nyc", "manhattan", "brooklyn"}},
	{"ca", "California", []string{"california", ", ca", "los angeles", "san francisco", "bay area", "palo alto", "mountain view", "menlo park", "san jose", "silicon valley", "marina del rey", "huntington beach", "oakland", "san diego"}},
	// Last, as the catch-all: a country-only "United States" tag (or an explicit
	// "remote") is a nationwide/remote role, not a state — so it is handled as such,
	// only after every specific state above has had its chance to match.
	{"us-remote", "US - Remote", []string{"united states", "usa", "remote", "anywhere"}},
}

// run starts a search (POST) or reports a running one's progress (GET ?id=).
func (s *server) run(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		req := runReq{Profile: profile.Default()}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, err)
			return
		}
		// No job-count knob: every run pulls the per-board ceiling from every source.
		count := perBoardMax
		p := req.Profile
		// Admin decision. In-cluster (platform auth configured) it is a verified,
		// signed JWT claim; locally it falls back to the corner-switch role in the
		// body. The mock/dataset path stays open to everyone, anonymous included.
		admin := false
		if s.auth != nil {
			admin = s.auth.IsAdmin(r)
		} else {
			admin = strings.EqualFold(req.Role, "admin")
		}
		// Under configured auth, a request that asked to be real but isn't from an
		// admin is refused outright, not silently downgraded to the mock — a silent
		// downgrade hides a permission problem behind a $0 result. 401 nudges the UI
		// to send a bearer token (or sign in).
		if s.auth != nil && strings.EqualFold(req.Role, "admin") && !admin {
			w.Header().Set("WWW-Authenticate", `Bearer realm="job-searcher"`)
			http.Error(w, "sign in as an admin to run a real search", http.StatusUnauthorized)
			return
		}
		// A signed-in (non-admin) user gets the REAL pipeline too, not the mock. Guests stay on the
		// mock. Admins are unbounded; everyone below admin is held to ONE scan per demoRunWindow.
		runUser := s.userID(r) // the run's OWNER — scopes its persisted results
		signedIn := runUser != ""
		// Weekly scan quota: for a signed-in user this bounds their REAL Apify SPEND to one scan a
		// week; for a guest it bounds mock load. Runs BEFORE the real/mock split. Disclosed to the
		// visitor via the 429 message and the search popover.
		if !admin {
			if wait, ok := s.allowDemoRun(s.demoKey(r)); !ok {
				days := int(wait.Hours())/24 + 1
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
				lede := "Demo mode — one scan per week; sign in for live scans."
				if signedIn {
					lede = "One live scan per week."
				}
				http.Error(w, fmt.Sprintf("%s Your next scan unlocks in %d day%s.", lede, days, plural(days)), http.StatusTooManyRequests)
				return
			}
		}
		// The real pipeline runs for any SIGNED-IN user when the environment wired it (APIFY_TOKEN
		// etc.); guests get the mock. Signed-in non-admins are bounded to one real scan/week by the
		// quota above; admins are unbounded. `admin` is OR'd in for the local no-auth path, where it
		// comes from the request-body role while signedIn (DEV_USER_ID) is usually unset.
		real := s.realReady && (admin || signedIn)
		id := "job-" + strconv.FormatInt(s.jobSeq.Add(1), 10)
		j := &jobState{
			id: id, spends: real && s.spends, status: "running", phase: "apify",
			// Real runs fan out to every board, so the target is the ceiling × sources;
			// the mock path overrides these with the actual cached-row count.
			apifyTotal: count * len(source.All()), verifyTotal: count * len(source.All()),
			rateUsed: 0.19, rateLimit: 5.00, // free-plan baseline
			cfg: report.ConfigFrom(p),
		}

		if real {
			j.header = output.Header()
			s.jobsMu.Lock()
			s.jobs[id] = j
			s.jobsMu.Unlock()
			go s.runReal(j, runUser, fieldQuery(req.Field), p, count)
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

// runStream is the live-scan Server-Sent Events feed for one run. It emits `rows`
// events (newly-verified listings, as DTOs) the instant each is appended, plus a
// `progress` event with the phase/counts/spend, and a terminal `done` or `error`.
// Unauthenticated like the GET snapshot it complements — the run id is the capability
// — so the browser can read it with a plain fetch (no EventSource auth limitation).
func (s *server) runStream(w http.ResponseWriter, r *http.Request) {
	s.jobsMu.Lock()
	j := s.jobs[r.URL.Query().Get("id")]
	s.jobsMu.Unlock()
	if j == nil {
		http.Error(w, "no such job", http.StatusNotFound)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok { // no streaming support behind this writer: degrade to a single snapshot
		writeJSON(w, j.snapshot())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // tell nginx not to buffer the stream
	fl.Flush()

	emit := func(event string, data any) bool {
		b, err := json.Marshal(data)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}

	ctx := r.Context()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	cursor := 0 // how many streamed rows the client already has
	for {
		select {
		case <-ctx.Done(): // the browser navigated away / aborted
			return
		case <-ticker.C:
		}
		j.mu.Lock()
		status, errMsg := j.status, j.errMsg
		prog := map[string]any{
			"phase": j.phase, "status": status, "spends": j.spends,
			"apify":  map[string]int{"done": j.apifyDone, "total": j.apifyTotal},
			"verify": map[string]int{"done": j.verifyDone, "total": j.verifyTotal},
			"rate":   map[string]float64{"used": j.rateUsed, "limit": j.rateLimit},
		}
		var fresh []resultDTO
		if cursor < len(j.stream) {
			fresh = append(fresh, j.stream[cursor:]...)
			cursor = len(j.stream)
		}
		shown := len(j.rows)
		j.mu.Unlock()

		if len(fresh) > 0 && !emit("rows", fresh) {
			return
		}
		if !emit("progress", prog) {
			return
		}
		if status == "done" {
			emit("done", map[string]any{"scanned": prog["verify"].(map[string]int)["total"], "shown": shown})
			return
		}
		if status == "error" {
			emit("error", map[string]any{"message": errMsg})
			return
		}
	}
}

// runMock simulates a run against a $0 mock: it replays the suite's cached rows
// with realistic timing so the Apify-load and post-process bars animate, without
// touching Apify or Gemini. Swapping in the real pipeline means replacing this
// body with ingest → verify calls that drive the same jobState fields.
func (s *server) runMock(j *jobState, rows [][]string) {
	n := len(rows)
	// The mock streams the CANNED demo cache (never any real user's results): a guest with no
	// identity sees a fixed sample, capped to the animated count and marked new so it pins to top.
	demo := s.cachedDTOs()
	var streamSrc []resultDTO
	for i := 0; i < len(demo) && i < n; i++ {
		d := demo[i]
		d.New = true
		streamSrc = append(streamSrc, d)
	}
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
	for i := 1; i <= n; i++ { // post-process: ATS + Gemini verdict, per listing.
		time.Sleep(pause)
		j.mu.Lock()
		j.verifyDone = i
		if i-1 < len(streamSrc) { // stream this row as it "verifies"
			j.stream = append(j.stream, streamSrc[i-1])
		}
		j.mu.Unlock()
	}
	j.mu.Lock()
	j.status, j.phase, j.rows = "done", "done", rows
	j.mu.Unlock()
	s.setLast(j.header, rows)
}

// runReal drives the actual pipeline, updating the same jobState the mock does:
// build the search URL from keywords + filters, start the Apify scrape and poll
// its dataset item-count for the Apify-load bar, normalize, verify (ATS + Gemini)
// with a per-listing callback for the post-process bar, apply the profile's
// filters, and read the account's Apify usage for the rate bar.
// scrapeSource runs one board's Apify actor to completion, streaming its item
// count into `done` (capped at count) for the aggregate progress bar, then returns
// its normalized listings. Concurrency-safe: called once per source goroutine.
func (s *server) scrapeSource(ctx context.Context, src source.Source, q watchlist.Query, count int, done *int64, onProgress func()) ([]model.Listing, error) {
	started, err := s.apify.StartRun(ctx, src.ActorID(), src.Input(q, count))
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	for {
		if cnt, e := s.apify.DatasetInfo(ctx, started.DefaultDatasetID); e == nil {
			if cnt > count {
				cnt = count
			}
			atomic.StoreInt64(done, int64(cnt))
			onProgress()
		}
		st, e := s.apify.RunStatus(ctx, started.ID)
		if e != nil {
			return nil, fmt.Errorf("poll: %w", e)
		}
		if st.Status == "SUCCEEDED" {
			break
		}
		if st.Status == "FAILED" || st.Status == "ABORTED" || st.Status == "TIMED-OUT" {
			return nil, fmt.Errorf("ended %s", st.Status)
		}
		time.Sleep(2 * time.Second)
	}
	raw, err := s.apify.DatasetItems(ctx, started.DefaultDatasetID)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	atomic.StoreInt64(done, int64(count)) // this source's slot is complete
	onProgress()
	return src.Normalize(raw), nil
}

func (s *server) runReal(j *jobState, userID, keywords string, p profile.Profile, count int) {
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

	// Fan out to every board concurrently, each at the per-board cap (`count`). The
	// Apify-load bar totals across sources; each source updates its own slot.
	srcs := source.All()
	dones := make([]int64, len(srcs))
	progress := func() {
		var sum int64
		for i := range dones {
			sum += atomic.LoadInt64(&dones[i])
		}
		j.mu.Lock()
		j.apifyDone = int(sum)
		j.mu.Unlock()
	}
	type srcResult struct {
		listings []model.Listing
		err      error
		name     string
	}
	res := make([]srcResult, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func(i int, src source.Source) {
			defer wg.Done()
			ls, err := s.scrapeSource(ctx, src, q, count, &dones[i], progress)
			res[i] = srcResult{listings: ls, err: err, name: src.Name()}
		}(i, src)
	}
	wg.Wait()

	// Merge successful sources; fail only if EVERY source errored (one board being
	// down shouldn't sink a run the other board answered).
	var merged []model.Listing
	var errs []string
	for _, r := range res {
		if r.err != nil {
			errs = append(errs, r.name+": "+r.err.Error())
			continue
		}
		merged = append(merged, r.listings...)
	}
	if len(merged) == 0 && len(errs) > 0 {
		fail("scrape failed — " + strings.Join(errs, "; "))
		return
	}
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "scrape: partial results — %s\n", strings.Join(errs, "; "))
	}

	// Collapse the same job cross-posted to both boards before verifying.
	listings, _ := source.Dedup(merged)
	j.mu.Lock()
	j.apifyDone, j.phase, j.verifyTotal, j.verifyDone = j.apifyTotal, "verify", len(listings), 0
	j.mu.Unlock()

	var done int64
	results := pipeline.Verify(ctx, listings, s.resolver, s.judge, score.DefaultWeights(), 8, nil, func(res model.Result) {
		n := atomic.AddInt64(&done, 1)
		j.mu.Lock()
		j.verifyDone = int(n)
		// Stream the verified row the instant it's ready (marked new — it's arriving now).
		// The full verified set persists to the aggregate below; the client reloads the
		// authoritative set on done, so streaming the unfiltered result here is correct.
		j.stream = append(j.stream, toResultDTO(res, true))
		j.mu.Unlock()
	})

	// Persist the FULL verified set (not the profile-filtered view) into the deduped
	// aggregate, so the accumulated table holds everything found and the client filters
	// it later. Best-effort: a persistence error must not fail an otherwise-good run.
	if s.db.Enabled() {
		if runID, rerr := s.db.StartRun(ctx, userID, keywords, count); rerr == nil {
			if uerr := s.db.UpsertResults(ctx, userID, runID, results); uerr != nil {
				fmt.Fprintf(os.Stderr, "db: upsert run results failed: %v\n", uerr)
			}
		} else {
			fmt.Fprintf(os.Stderr, "db: start run failed: %v\n", rerr)
		}
	}

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

// static serves the web UI beneath base. In production a built Vite frontend is
// copied into WEB_DIR and served as files — its asset URLs are already baked with
// BASE_PATH at build time, so they resolve correctly behind the router. With no
// WEB_DIR (local dev / `go run`), the embedded legacy single-page index.html is
// served instead, so the binary builds and runs with no frontend build present —
// the existing local workflow is untouched.
func (s *server) static() http.Handler {
	if dir := os.Getenv("WEB_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			// Strip the base prefix so paths resolve against the dir root; FileServer
			// then serves index.html for the base path and assets beneath it.
			return http.StripPrefix(strings.TrimSuffix(s.base, "/"), http.FileServer(http.Dir(dir)))
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != s.base {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
}

// health is the Kubernetes probe target (served at <base>api/health).
func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": "ok", "realReady": s.realReady})
}

// version reports the deployed image version (served at <base>version), so the
// running container can say what it is — "snapshot" for a dev build.
func (s *server) version(w http.ResponseWriter, r *http.Request) {
	v := s.appVersion
	if v == "" {
		v = "snapshot"
	}
	writeJSON(w, map[string]string{"version": v})
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

// listings serves the persisted aggregate (or the latest-scan "new" subset),
// rendered through the same report pipeline as preview so the table is identical.
// Empty when persistence is off.
func (s *server) listings(w http.ResponseWriter, r *http.Request) {
	view := db.Aggregate
	if strings.EqualFold(r.URL.Query().Get("view"), "new") {
		view = db.New
	}
	// Scope to the caller. A guest (no identity) gets the canned demo cache, never the per-user pool.
	userID := s.userID(r)
	var header []string
	var data [][]string
	if userID == "" {
		var lerr error
		if header, data, lerr = s.loadCache(); lerr != nil {
			httpErr(w, lerr)
			return
		}
	} else {
		results, err := s.db.Listings(r.Context(), userID, view)
		if err != nil {
			httpErr(w, err)
			return
		}
		header = output.Header()
		data = output.Rows(results)
	}
	s.setLast(header, data)
	cols, table := report.Preview(header, data, report.ConfigFrom(profile.Default()), time.Now())
	writeJSON(w, map[string]any{"columns": cols, "rows": table, "kept": len(data), "total": len(data)})
}

// refresh re-checks each persisted listing's apply URL and soft-deletes the ones
// that are definitively gone. Admin-only under configured auth (it mutates the
// aggregate); a no-op without a DB.
func (s *server) refresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Refresh sweeps the admin's OWN listings for dead apply URLs. A non-admin gets a friendly
	// no-op rather than a 401 — the button exists for them in the demo, it just reports that upkeep
	// is handled automatically.
	if s.auth != nil && !s.auth.IsAdmin(r) {
		writeJSON(w, map[string]any{"checked": 0, "removed": 0, "demo": true})
		return
	}
	userID := s.userID(r)
	cands, err := s.db.AvailabilityCandidates(r.Context(), userID)
	if err != nil {
		httpErr(w, err)
		return
	}
	removed, err := s.db.MarkUnavailable(r.Context(), userID, deadURLs(r.Context(), cands))
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]int{"checked": len(cands), "removed": int(removed)})
}

// saved reads or writes the signed-in identity's pin/applied state. GET returns the
// user's flags keyed by URL (empty for a guest or without a DB); PUT upserts one URL
// and requires an identity — a guest keeps its saved state in the browser instead.
func (s *server) saved(w http.ResponseWriter, r *http.Request) {
	userID := s.userID(r)
	switch r.Method {
	case http.MethodGet:
		// Return each saved job WITH its row data (not just the flag), so the Saved/
		// Applied tabs reconstruct from the server and survive a refresh or a fresh
		// browser — the client no longer needs a localStorage snapshot from the run
		// that saved them. `flags` is keyed by the same trimmed URL the rows expose,
		// and carries `available` so a job the Refresh sweep retired renders as
		// "no longer listed" instead of vanishing.
		saved, err := s.db.SavedListings(r.Context(), userID)
		if err != nil {
			httpErr(w, err)
			return
		}
		results := make([]model.Result, len(saved))
		for i, sl := range saved {
			results[i] = sl.Result
		}
		header := output.Header()
		cols, table := report.Preview(header, output.Rows(results), report.ConfigFrom(profile.Default()), time.Now())
		flags := make(map[string]map[string]bool, len(table))
		for i, pr := range table {
			flags[pr.URL] = map[string]bool{
				"pinned": saved[i].Pinned, "applied": saved[i].Applied, "available": saved[i].Available,
			}
		}
		writeJSON(w, map[string]any{"flags": flags, "columns": cols, "rows": table})
	case http.MethodPut:
		if userID == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="job-searcher"`)
			http.Error(w, "sign in to save listings to your account", http.StatusUnauthorized)
			return
		}
		var body struct {
			URL     string `json:"url"`
			Pinned  bool   `json:"pinned"`
			Applied bool   `json:"applied"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, err)
			return
		}
		if err := s.db.SetSaved(r.Context(), userID, body.URL, db.SavedFlags{Pinned: body.Pinned, Applied: body.Applied}); err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// codex is the Codex template store: a user's reusable copy-paste application text.
// GET lists the caller's templates (empty for a guest/no-DB — guests keep theirs in
// localStorage); POST upserts one; DELETE removes one. Writes require a signed-in
// identity, mirroring api/saved.
func (s *server) codex(w http.ResponseWriter, r *http.Request) {
	userID := s.userID(r)
	switch r.Method {
	case http.MethodGet:
		ts, err := s.db.Templates(r.Context(), userID)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"templates": ts})
	case http.MethodPost:
		if userID == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="job-searcher"`)
			http.Error(w, "sign in to save templates to your account", http.StatusUnauthorized)
			return
		}
		var t db.Template
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			httpErr(w, err)
			return
		}
		if strings.TrimSpace(t.ID) == "" {
			http.Error(w, "template id required", http.StatusBadRequest)
			return
		}
		if err := s.db.UpsertTemplate(r.Context(), userID, t); err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, t)
	case http.MethodDelete:
		if userID == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="job-searcher"`)
			http.Error(w, "sign in to manage templates", http.StatusUnauthorized)
			return
		}
		if err := s.db.DeleteTemplate(r.Context(), userID, r.URL.Query().Get("id")); err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// unauthedRealAllowed reports the explicit opt-in that lets paid backends run without platform
// auth — trusted LOCAL testing only. A real deploy must set AUTH_JWKS_URI instead.
func unauthedRealAllowed() bool { return os.Getenv("ALLOW_UNAUTHENTICATED_REAL") == "1" }

// demoRunWindow is how often a non-admin (demo) visitor may launch a scan.
const demoRunWindow = 7 * 24 * time.Hour

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// demoKey identifies a demo caller for the weekly scan quota: their signed-in user id,
// else the client IP (first X-Forwarded-For hop behind the proxy, else RemoteAddr).
func (s *server) demoKey(r *http.Request) string {
	if id := s.userID(r); id != "" {
		return "u:" + id
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		return "ip:" + strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return "ip:" + host
}

// allowDemoRun enforces one scan per demoRunWindow for a non-admin caller. It returns the
// wait until the next scan when blocked; when it allows one it records the time atomically,
// so two rapid requests can't both slip through.
func (s *server) allowDemoRun(key string) (time.Duration, bool) {
	s.demoMu.Lock()
	defer s.demoMu.Unlock()
	now := time.Now()
	if last, ok := s.demoRuns[key]; ok {
		if wait := demoRunWindow - now.Sub(last); wait > 0 {
			return wait, false
		}
	}
	s.demoRuns[key] = now
	return 0, true
}

// userID resolves the caller's platform identity: the verified auth claim when
// auth is configured (in-cluster), else DEV_USER_ID for local dev ("" when unset,
// which the saved/applicator queries treat as no user).
func (s *server) userID(r *http.Request) string {
	if s.auth != nil {
		id, _ := s.auth.Identity(r)
		return id
	}
	return os.Getenv("DEV_USER_ID")
}

// applicatorJob is one Applicator batch's live progress: how many of the
// saved-not-applied listings have been summarized so far.
type applicatorJob struct {
	mu     sync.Mutex
	id     string
	status string // running | done | error
	done   int
	total  int
	errMsg string
}

func (a *applicatorJob) snapshot() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := map[string]any{"id": a.id, "status": a.status, "done": a.done, "total": a.total}
	if a.errMsg != "" {
		m["error"] = a.errMsg
	}
	return m
}

// applicatorLaunch starts a batch summarizing every saved-but-not-applied listing
// that lacks a cached summary, storing each in job_summaries (so re-launch is
// incremental). Admin-gated under configured auth — it spends Gemini tokens.
func (s *server) applicatorLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Discern gives any SIGNED-IN user real Gemini summaries — it runs on the free-tier lite-flash
	// ($0/token), so there is no per-use cost to meter and it stays unlimited. Guests get the $0
	// MockSummarizer so the demo funnel (Consecrate → Discern → apply cards) still works end to end.
	// (Locally, ALLOW_UNAUTHENTICATED_REAL opts in without configured auth.)
	real := (s.auth != nil && s.userID(r) != "") || (s.auth == nil && unauthedRealAllowed())
	var summarizer summarize.Summarizer = summarize.MockSummarizer{}
	modelTag := "mock"
	if real {
		if s.summarizer == nil {
			http.Error(w, "summaries are unavailable — no summary backend configured", http.StatusServiceUnavailable)
			return
		}
		summarizer, modelTag = s.summarizer, s.sumModel
	}
	userID := s.userID(r)
	saved, err := s.db.SavedNotApplied(r.Context(), userID)
	if err != nil {
		httpErr(w, err)
		return
	}
	urls := make([]string, 0, len(saved))
	for _, sl := range saved {
		urls = append(urls, sl.Result.Listing.URL)
	}
	have, err := s.db.SummariesFor(r.Context(), urls)
	if err != nil {
		httpErr(w, err)
		return
	}
	var todo []model.Listing
	for _, sl := range saved {
		if _, ok := have[sl.Result.Listing.URL]; !ok {
			todo = append(todo, sl.Result.Listing)
		}
	}
	// Optional selection: {"urls":[...]} limits the batch to the chosen Consecrated jobs
	// (multiselect Discern). An empty/absent body summarizes all un-discerned saved jobs.
	var sel struct {
		URLs []string `json:"urls"`
	}
	_ = json.NewDecoder(r.Body).Decode(&sel)
	if len(sel.URLs) > 0 {
		want := make(map[string]bool, len(sel.URLs))
		for _, u := range sel.URLs {
			want[u] = true
		}
		kept := todo[:0]
		for _, l := range todo {
			if want[l.URL] {
				kept = append(kept, l)
			}
		}
		todo = kept
	}

	id := "app-" + strconv.FormatInt(s.jobSeq.Add(1), 10)
	job := &applicatorJob{id: id, status: "running", total: len(todo)}
	s.appMu.Lock()
	s.appJobs[id] = job
	s.appMu.Unlock()

	// Summarize concurrently; the summarizer is already Bounded, so goroutines queue
	// at its semaphore rather than flooding Gemini.
	go func() {
		ctx := context.Background()
		var wg sync.WaitGroup
		for _, l := range todo {
			wg.Add(1)
			go func(l model.Listing) {
				defer wg.Done()
				sum, serr := summarizer.Summarize(ctx, l)
				if serr != nil {
					fmt.Fprintf(os.Stderr, "applicator: summarize %s: %v\n", l.URL, serr)
				} else if uerr := s.db.UpsertSummary(ctx, l.URL, sum, modelTag); uerr != nil {
					fmt.Fprintf(os.Stderr, "applicator: store summary %s: %v\n", l.URL, uerr)
				}
				job.mu.Lock()
				job.done++
				job.mu.Unlock()
			}(l)
		}
		wg.Wait()
		job.mu.Lock()
		job.status = "done"
		job.mu.Unlock()
	}()

	writeJSON(w, map[string]string{"id": id})
}

// trash marks the given saved listings unavailable (dismissed): they leave Scry and the
// shortlist and appear in the Trash view. Requires an identity; MarkUnavailable never
// touches a manifested (applied) job.
func (s *server) trash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.userID(r) == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="job-searcher"`)
		http.Error(w, "sign in to trash listings", http.StatusUnauthorized)
		return
	}
	var body struct {
		URLs []string `json:"urls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err)
		return
	}
	// Manual trash can dismiss ANY card, including a manifested one: clear the caller's
	// applied flag first (so the availability sweep's manifested-guard doesn't block it),
	// then retire the listing. (The automatic Refresh sweep still spares manifested jobs.)
	uid := s.userID(r)
	for _, u := range body.URLs {
		_ = s.db.SetSaved(r.Context(), uid, u, db.SavedFlags{Pinned: true, Applied: false})
	}
	n, err := s.db.MarkUnavailable(r.Context(), uid, body.URLs)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]int{"trashed": int(n)})
}

// restore un-retires the given listings — bringing a trashed job back into Scry and the
// shortlist. Requires an identity.
func (s *server) restore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.userID(r) == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="job-searcher"`)
		http.Error(w, "sign in to restore listings", http.StatusUnauthorized)
		return
	}
	var body struct {
		URLs []string `json:"urls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err)
		return
	}
	n, err := s.db.MarkAvailable(r.Context(), s.userID(r), body.URLs)
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]int{"restored": int(n)})
}

// applicatorStatus reports a launch's progress for the loading screen.
func (s *server) applicatorStatus(w http.ResponseWriter, r *http.Request) {
	s.appMu.Lock()
	job := s.appJobs[r.URL.Query().Get("id")]
	s.appMu.Unlock()
	if job == nil {
		http.Error(w, "no such applicator job", http.StatusNotFound)
		return
	}
	writeJSON(w, job.snapshot())
}

// applyRow is one Applicator table row: the listing's key fields plus its cached
// summary and a contract flag (contract work is not comparable to a salaried role).
type applyRow struct {
	U          string `json:"u"`     // canonical URL — the applied-sync key
	Apply      string `json:"apply"` // where the browser opens to apply
	Company    string `json:"c"`
	Title      string `json:"t"`
	Loc        string `json:"lp"`
	Remote     bool   `json:"r"`
	SalMin     int    `json:"smin"`
	SalMax     int    `json:"smax"`
	EstMin     int    `json:"emin"`
	EstMax     int    `json:"emax"`
	Required   string `json:"required"`
	Preferred  string `json:"preferred"`
	Role       string `json:"role"`
	Does       string `json:"does"`
	PayNote    string `json:"payNote"`
	Employment string `json:"employment"`
	Contract   bool   `json:"contract"`
	Available  bool   `json:"a"`
}

// applicator returns the current user's saved-not-applied jobs joined with their
// cached summaries — the Applicator table data. Rows with no summary yet show
// blanks (a launch fills them in).
func (s *server) applicator(w http.ResponseWriter, r *http.Request) {
	userID := s.userID(r)
	saved, err := s.db.SavedNotApplied(r.Context(), userID)
	if err != nil {
		httpErr(w, err)
		return
	}
	// view=applied → the Manifested set (saved AND applied); view=trashed → the Trash
	// (saved, not applied, no longer available: manually trashed or swept by Refresh).
	// Same row shape, so the client renders each through the same card.
	if view := strings.ToLower(r.URL.Query().Get("view")); view == "applied" || view == "trashed" {
		all, aerr := s.db.SavedListings(r.Context(), userID)
		if aerr != nil {
			httpErr(w, aerr)
			return
		}
		saved = saved[:0]
		for _, sl := range all {
			keep := sl.Applied
			if view == "trashed" {
				keep = sl.Pinned && !sl.Applied && !sl.Available
			}
			if keep {
				saved = append(saved, sl)
			}
		}
	}
	urls := make([]string, 0, len(saved))
	for _, sl := range saved {
		urls = append(urls, sl.Result.Listing.URL)
	}
	summaries, err := s.db.SummariesFor(r.Context(), urls)
	if err != nil {
		httpErr(w, err)
		return
	}
	rows := make([]applyRow, 0, len(saved))
	summarized := 0
	for _, sl := range saved {
		l := sl.Result.Listing
		sum, ok := summaries[l.URL]
		if ok {
			summarized++
		}
		contract := sum.Employment == "contract" || strings.Contains(strings.ToLower(l.EmploymentType), "contract")
		apply := l.URL
		if l.ExternalApplyURL != "" {
			apply = l.ExternalApplyURL
		}
		rows = append(rows, applyRow{
			U: l.URL, Apply: apply, Company: l.Company, Title: l.Title,
			Loc: locPrefix(l.Location), Remote: l.Remote,
			SalMin: l.SalaryMin, SalMax: l.SalaryMax, EstMin: l.SalaryEstMin, EstMax: l.SalaryEstMax,
			Required: sum.Required, Preferred: sum.Preferred, Role: sum.Role, Does: sum.Company,
			PayNote: sum.PayNote, Employment: sum.Employment, Contract: contract, Available: sl.Available,
		})
	}
	writeJSON(w, map[string]any{"jobs": rows, "total": len(rows), "summarized": summarized})
}

// locPrefix is the label before the first comma of a location ("Boston, MA" → "Boston").
func locPrefix(s string) string {
	if i := strings.IndexByte(s, ','); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// deadURLs HEAD-checks each candidate's apply URL and returns the dedup URLs that
// are definitively gone. Only 404/410 count as dead — a timeout, bot-block (LinkedIn
// 999), 403 or network error is "uncertain" and left available, so a refresh never
// wrongly prunes a live listing. Bounded concurrency, short timeout.
func deadURLs(ctx context.Context, cands []db.Candidate) []string {
	const workers = 8
	sem := make(chan struct{}, workers)
	client := &http.Client{Timeout: 6 * time.Second}
	var mu sync.Mutex
	var dead []string
	var wg sync.WaitGroup
	for _, c := range cands {
		target := c.ApplyURL
		if target == "" {
			target = c.URL
		}
		if target == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(dedupURL, target string) {
			defer wg.Done()
			defer func() { <-sem }()
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", "job-searcher-refresh/1")
			resp, err := client.Do(req)
			if err != nil {
				return // uncertain → leave available
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
				mu.Lock()
				dead = append(dead, dedupURL)
				mu.Unlock()
			}
		}(c.URL, target)
	}
	wg.Wait()
	return dead
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
