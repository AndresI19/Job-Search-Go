# Jobomancer — product & build spec

> Divine the real jobs from the ghosts, then make the application real.
> This document is the **goal-spec**: the target the reimagining builds toward. It is written
> against `origin/main` (currently #87) and supersedes the ad-hoc "Job Searcher" UI.

Interactive reference mockup (behavior + visuals): the published Jobomancer preview artifact.

---

## 1. Thesis

Between a quarter and a third of online listings are **ghost jobs**. Jobomancer treats
**verification as the product**: it ingests broadly, scores each listing's legitimacy from
whatever signals are available (coverage-aware), and helps you work only the real ones through
to an application. The old UI buried that behind a five-tab spreadsheet; Jobomancer reframes the
app around the funnel it actually is.

## 2. Identity & lexicon

The divination theme is load-bearing, not decoration — each ritual verb names the real mechanic
beneath it. **Display strings only**; internal keys, endpoints, and DB columns keep their
existing names (`discover`/`apply`, `saved`/`prepped`/`applied`) so a re-theme is pure copy.

| Surface | Name | Mechanic underneath |
|---|---|---|
| Product | **Jobomancer** | the whole tool |
| Glyph | circular zodiac wheel (inline SVG, gradient stroke) | brand mark |
| Mode — discover | **Scry** 🔮 | scan + verify the market |
| Mode — apply | **Conjure** 🪄 | work your shortlist to an application |
| Stage — saved | **Consecrated** ★ | starred / set apart |
| Stage — prepped | **Discerned** | summarized by the LLM (required-vs-preferred) |
| Stage — applied | **Manifested** ✓ | you self-reported that you applied |
| Action — summarize | **Discern** | the batch LLM read of saved postings |
| Verdict | a **reading** | likely-real / uncertain / likely-ghost |

## 3. The two-room model

Scanning and applying are opposite activities and get opposite surfaces, joined by **Save**
(Consecrate). A `Scry / Conjure` switch in the top bar is the only room-level nav — no funnel
bar of clickable stage buttons; counts live where they are actionable (the Conjure sub-tabs and
a quiet Scry status line).

### Scry (Discover)
- **Default view = every persisted, verified job**, shown immediately. No "run" needed to see
  your jobs. The old **Aggregate** tab and the word **"corpus"** are removed (jargon deleted, not
  relabeled).
- **A dense, sortable, filterable data-grid** — the right tool for scanning hundreds of rows.
  Columns and their order are kept; confidence stays a **footnote** (a small dot by the Score
  column), not a headline.
  - **Per-column sort**: classic ▲▼ on every header.
  - **Per-column filter**: a dropdown per header — categorical (role, location, company tier,
    remote) as checkbox lists; numeric (pay, score, posted) as ranges. Replaces the #81 faceted
    menu and the resbar company chips.
  - **Posted** cell hover → the actual date.
  - **New-first, always.** Jobs added by the latest scan pin to the **top** of the grid under a
    clear `✨ New since last scan` divider (with an `Earlier` divider below), and stay first
    **even when a column sort is applied** — the active sort orders *within* the New and Earlier
    groups, never across them. This replaces the removed "New" tab: freshness is a persistent
    grouping, not a separate view you switch to.
- **Top bar**: `▶ Run` opens the **search panel** (configure, then launch from inside it — the
  launch is never separated from its inputs); `↻ Refresh` stands alone and works in **both**
  rooms (re-check + prune delisted, for a visit that's only about your shortlist).
- **Search panel**: field, locations, and keep-only filters. **No job-count field** — every run
  pulls a fixed **1,000 × LinkedIn + 1,000 × Indeed** (#85); the filters decide what is *kept*,
  not how many are *fetched*.
- **Streaming (SSE)**: on Run, verified results **stream into the same table** as each clears
  ATS + Claude — no full-screen progress takeover. Wire to `pipeline.Verify`'s existing
  `onDone` hook.

### Conjure (Apply)
- **Cards, not rows** — the Discern output (role summary, company summary, required-vs-preferred,
  employment, pay) is prose and dies in a spreadsheet cell.
- **Sub-stages in funnel order**: `★ Consecrated → Discerned → ✓ Manifested`.
- **Consecrated** cards carry the listing facts that would otherwise be lost leaving the grid
  (role, tier, pay range, posted date, score). They are **not** prepped — the *absence* of the
  summary lines signals that, no instructional copy. **Select by clicking the tile** (no
  checkbox); `✨ Discern (n)` acts on the selection (multiselect, never "prep all" — Discern
  spends tokens per job, so the spend must be intentional).
- **Discerned** cards show the summary + **pay provenance** (below).
- **Honest apply**: applying happens on an external site, so the app can't know you did it.
  Split the action: **`Open posting ↗`** is pure navigation (moves nothing); **`Mark
  manifested`** is an explicit honor-system toggle (the existing applied flag) that *you* set.

## 4. Pay provenance (posted vs estimated)

Never show an estimate as if it were posted. Three honest states, tracked as `payState`:

- **Posted** — the listing states pay (`SalaryMin/Max`). Green badge, solid figure.
- **Estimated** — `internal/comp` heuristic (seniority band × metro multiplier, software roles
  only). Amber badge, `~` prefix, muted/italic, "estimated · no pay in posting". This is a
  **deterministic heuristic, not an LLM call**, and it runs at ingest time so pay shows in the
  Scry grid immediately.
- **No pay** — neither available. Grey.

Scry grid marks estimated cells muted+italic+`~` with no "strong salary" tint. Conjure filter is
`All / Posted / Estimated`.

**Tabled (future):** let Discern's LLM `pay_note` (it reads the full description) **promote**
`estimated → posted` when a real figure is stated in prose the structured field missed. Cheap
heuristic up front, LLM correction on the shortlist. Not in the initial build.

## 5. What already exists (reuse, don't rebuild)

The backend largely supports this today — the work is mostly frontend + a typed contract + SSE:
- Persistence, server-side Saved, Refresh/prune, the aggregate set (`internal/db`, #75–#79).
- The **Applicator** summarizer (`internal/summarize`, cli/api/mock/**gemini** backends, #84/#87)
  — the engine behind **Discern**.
- Salary estimation (`internal/comp`) and posted salary fields on the model.
- Multi-source ingest: LinkedIn + Indeed, both maxed (#85).
- Platform identity (auth), the `@platform/ui` gate/tokens.

## 6. Design invariants

- **Display strings ≠ identifiers.** Re-themes touch copy only; logic keys off stable
  `data-*`/endpoint names. Proven across the mockup's rename passes.
- **Kill the spreadsheet contract.** The `{columns, rows:[{cells:[{value,fill}]}]}` payload is an
  xlsx artifact; the API should speak typed `Result` (Listing + Verdict + Summary) and the client
  owns presentation (tinting, provenance styling).
- **One activity per surface.** Table for scanning; cards for applying.
- **Truth-in-state.** The UI only claims what it can verify — hence the Open/Manifest split and
  pay provenance. Same coverage-aware honesty the `Verdict` already applies to legitimacy.
- **Estimation stays a cheap heuristic**, run broadly at ingest, not folded into the paid LLM step.

## 7. Build plan (staged PRs, no auto-merge, snapshot-tested on live first)

- **Phase 0 — this doc** + sync to `origin/main`, live baseline snapshot.
- **Phase 1 — typed data contract.** JSON `Result` endpoints; `model.ts` + a small store; retire
  the cells/fill payload. Foundational; everything downstream depends on it.
- **Phase 2 — Scry room.** Two-room shell + mode switch; the data-grid (per-column sort/filter,
  posted-hover date, estimated styling); SSE streaming run; top-bar Run-opens-panel + standalone
  Refresh; drop Aggregate/corpus/job-count.
- **Phase 3 — Conjure room.** Card renderers; sub-stages; multiselect Discern; honest
  Open/Manifest split; pay-provenance badges. *Live prereq:* `ANTHROPIC_API_KEY` sealed in-cluster
  (shared with the judge) or the Gemini backend configured, else Discern batches return blank.
- **Phase 4 — rebrand & lexicon.** Jobomancer name, zodiac SVG glyph, favicon, title, README/wiki;
  the divination copy pass. Display-only.
- **Phase 5 — polish & deploy.** reduced-motion, keyboard focus, mobile grid+cards, both themes;
  collapse the two front doors (`cmd/gui` serves the `web/` build); SSE-through-nginx
  (`proxy_buffering off` / `X-Accel-Buffering: no`, respect `BASE_URL`); FVT; deploy (branch
  protection: PR + 1, solo merge `--admin`).

Sequencing: **data contract first** (unlocks grid + cards), **rename last** (safest, most
reversible). Strangler-fig throughout — new surfaces land behind existing endpoints so
`/job-searcher` stays up the whole time.
