# CLAUDE.md — Job-Search-Go (Jobomancer)

Guidance for Claude Code working in this repo. Written from the original code as the refactor goal-spec:
every claim here is an invariant the code must keep honoring.

## What this is

A job-search and application helper. It **verifies which postings are real** (vs "ghost jobs") and
**drafts tailored cover letters** so you apply in one pass. The repo name stays `Job-Search-Go`; the
deployed web app is branded **Jobomancer** and lives at `/job-searcher/` on the platform.

Two halves:
- **Go backend** — a CLI pipeline (ingest → verify → score) plus an HTTP server (`cmd/gui`) that drives
  it live and serves the SPA.
- **Vanilla-TS/Vite frontend** (`web/`) — three rooms: **Scry** 🔮 (the results grid), **Conjure** 🪄
  (shortlist → Discern into apply cards → mark Manifested), **Codex** 📖 (reusable cover-letter templates
  + Personal Info tokens). Reuses `@platform/ui` (auth, gate) vendored under `web/vendor/`.

## The verification pipeline (the product)

`internal/pipeline.Verify` runs per listing, concurrently (bounded workers):
1. **Ingest** — Apify Actors scrape LinkedIn + Indeed (`internal/apify`, `source/*`); records normalize to
   `model.Listing`.
2. **ATS cross-reference** — resolve the company to its Greenhouse/Lever board (`internal/ats`,
   `source/greenhouse|lever`) and look for a matching open requisition. A match is the strongest real signal.
3. **Judge** — an LLM decides `matched` + a categorical `verdict` (likely-real / uncertain / likely-ghost)
   + `confidence`. See "One model" below.
4. **Score** — `internal/score` blends the arms into a 0..1 legitimacy `Score`; `model.Verdict.Coverage`
   records **which signals actually ran**, so an `uncertain` verdict from thin coverage stays distinct from
   a real `likely-ghost`. `legitimacyScore` maps (verdict, confidence) → score: a confident likely-ghost
   scores LOW — never invert this.

## Public surface

- **CLI** (`cmd/`): `jobsearch` (watch-list → scored CSV), `render` (CSV → formatted table), `gui` (the
  server), `capturefixture` (one-shot Apify ingest → offline fixture).
- **HTTP** (`cmd/gui`, under `BASE_PATH`): `api/run` + `api/run/stream` (launch a scan, SSE rows),
  `api/results` (typed Scry rows), `api/listings` (table view), `api/refresh`, `api/saved`,
  `api/applicator/*` (Discern/apply: launch, status, trash, restore), `api/codex` (templates),
  `api/config`, `api/health`, `api/profile`, `api/preview`, `api/export`/`download`/`import`.
- **Frontend** (`web/src`): `main.ts` (entry, @ts-nocheck), `scry.ts`, `conjure.ts`, `codex.ts` + their CSS.

## Invariants that must not regress

- **One model — Gemini.** Judge AND summaries both run on Gemini's free-tier lite-flash ($0/token); the
  `cli` (claude subprocess) and `api` (Anthropic) backends remain selectable for local dev but prod is
  `JUDGE_BACKEND=gemini`. Judge picks a backend by env; there is no per-user model.
- **Access tiers.** Guest = the $0 **mock** (canned cache) + `MockSummarizer`. Signed-in user = the **REAL**
  pipeline, scan capped to **one per week** (bounds Apify spend), **Discern unlimited** (Gemini is free).
  Admin = real, unbounded. `real := realReady && (admin || signedIn)`; the weekly quota gates non-admins.
- **Per-identity data.** `listings` and `runs` are **owner-scoped** (`user_id`, dedup `(user_id, url)`);
  every read/write/join scopes to the caller. A guest (no identity) is served the **canned demo cache**,
  never the pool — one user's runs never reach another.
- **Auth is the gate.** Under configured auth (`AUTH_JWKS_URI` set) identity/admin come from the verified
  JWT; without it (`s.auth == nil`) `userID` falls back to `DEV_USER_ID` and admin to the request-body role
  — trust the token only when auth is wired. `ALLOW_UNAUTHENTICATED_REAL=1` is local-only.
- **Secrets are files, not env.** `internal/secret.Value(env, fileEnv, defaultFile)` reads a mounted
  read-only file first, keeping keys (Apify, Gemini, DB URL) out of the process environment in-cluster.
- **URL dedup is canonical.** `canonicalURL` strips LinkedIn tracking params so one posting is one row.
- **Best-effort persistence.** A DB error must never fail an otherwise-good run.

## Pitfalls

- **DB integration tests SKIP in CI** — `.github/workflows/ci.yml` has no Postgres service, so
  `internal/db/*_test.go` (gated on `TEST_DATABASE_URL`) don't run there. Verify DB changes locally against
  the `js-pgtest` container: `TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55433/jobsearch?sslmode=disable`.
  `testDB()` only drops `listings, runs, saved`; wipe the schema when a test sees stale rows.
- **Schema migration runs at image boot** (`schemaSQL`, idempotent) — the `ALTER`s must precede the index
  that keys on the new column. During a rolling deploy an old pod's `ON CONFLICT (url)` may transiently
  fail after the new pod drops that constraint; harmless because upserts are best-effort.
- **The server is bundled for the image** (esbuild), not run from source; a new runtime dep must survive
  bundling. The frontend is a Vite build under `BASE_PATH`.
- **Cloudflare caches `public/` assets ~4h** — replacing a same-named asset serves stale; bump the filename.

## Pre-PR checks (the gate — must be green)

```bash
go build ./... && go vet ./... && gofmt -l cmd/ internal/   # (empty = clean)
go test ./...                                                # non-DB packages
TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55433/jobsearch?sslmode=disable go test ./internal/db/
cd web && npx tsc --noEmit && npx biome check src && npm run build
```
CI runs build-test, web, CodeQL, gitleaks, dep-scan, image-scan. `image-scan` can flake on a transient
base-image pull (`gcr.io … 403`) — re-run, it's not a code failure.
