// @ts-nocheck — Jobomancer shell. A purpose-built two-room shell (Scry / Conjure) that
// replaced the legacy tabbed single-page monolith. It owns only the chrome: the mode
// switch, the search popover, the scan run/progress, and Refresh. The two views are the
// typed modules web/src/{scry,conjure}.ts, which read the domain endpoints directly.
import '@platform/ui/tokens.css';
import '@platform/ui/base.css';
import '@platform/ui/gate.css';
import './app.css';
import { mountScry, scryStreamStart, scryStreamRows } from './scry';
import { mountConjure } from './conjure';
import { mountCodex } from './codex';
import { mountAccountFab, mountGate } from '@platform/ui/gate';
import { authFetch, isAdmin, isSignedIn, onIdentity } from '@platform/ui/auth';

const BASE = import.meta.env.BASE_URL;
const api = (p) => BASE + 'api/' + p;
const $ = (id) => document.getElementById(id);
const esc = (s) => (s == null ? '' : String(s)).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');

let mode = 'scry';                 // 'scry' | 'conjure'
let role = 'guest';                // 'guest' | 'admin' — derived from identity
let runCfg = { realReady: false, spends: false };
let scryMounted = false;
let pollTimer = 0;

// ---- status + toast ----
function setStatus(msg, err) { const s = $('status'); if (!s) return; s.innerHTML = msg || ''; s.className = 'status' + (err ? ' err' : ''); }
let toastTimer = 0;
function toast(html) { const t = $('toast'); if (!t) return; t.innerHTML = html; t.hidden = false; t.classList.add('show'); clearTimeout(toastTimer); toastTimer = setTimeout(() => { t.classList.remove('show'); }, 3200); }

// ---- mode switch (Scry / Conjure) ----
function setMode(m) {
  mode = m;
  for (const b of document.querySelectorAll('#jb-modes button')) {
    const on = b.dataset.mode === m;
    b.classList.toggle('on', on);
    if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
  }
  $('search-pop').hidden = true;
  if (m === 'scry') showScry(); else if (m === 'conjure') showConjure(); else showCodex();
  refreshTabCounts();
}

// Cross-room funnel feedback: the Conjure tab wears a badge with the shortlist size
// (consecrated + discerned = saved, not applied, still available), so pipeline state is
// visible from anywhere. It pulses when the count grows — e.g. right after you Consecrate.
async function refreshTabCounts() {
  const el = $('conjure-count'); if (!el) return;
  let n = 0;
  try {
    const res = await authFetch(api('saved'));
    if (res && res.ok) {
      const flags = (await res.json()).flags || {};
      for (const f of Object.values(flags)) if (f && f.pinned && !f.applied && f.available) n++;
    }
  } catch { /* best-effort — a missing count just hides the badge */ }
  const prev = Number(el.dataset.n || '0');
  el.dataset.n = String(n);
  el.textContent = String(n);
  el.hidden = n === 0;
  if (n > prev) { el.classList.remove('bump'); void el.offsetWidth; el.classList.add('bump'); }
}
function showScry() {
  $('runview').hidden = true;
  $('scry-root').hidden = false;
  $('conjure-root').hidden = true;
  $('codex-root').hidden = true;
  if (!scryMounted) { mountScry($('scry-root'), { authFetch, onNewSearch: openSearchAt, onRefresh: doRefresh, onShortlistChange: refreshTabCounts }); scryMounted = true; }
}
function showConjure() {
  $('runview').hidden = true;
  $('scry-root').hidden = true;
  $('conjure-root').hidden = false;
  $('codex-root').hidden = true;
  mountConjure($('conjure-root'), { api, authFetch, onShortlistChange: refreshTabCounts }); // re-fetch each show (reflect fresh Consecrates)
}
function showCodex() {
  $('runview').hidden = true;
  $('scry-root').hidden = true;
  $('conjure-root').hidden = true;
  $('codex-root').hidden = false;
  mountCodex($('codex-root'), { api, authFetch, isSignedIn }); // re-fetch each show
}
function reloadScry() { scryMounted = false; if (mode === 'scry') showScry(); }
document.querySelectorAll('#jb-modes button').forEach((b) => b.addEventListener('click', () => setMode(b.dataset.mode)));

// ---- search popover (opened from the Scry ctx bar's New search button) ----
function closeSearch() { $('search-pop').hidden = true; }
function openSearchAt(anchor) {
  const sp = $('search-pop');
  if (!sp.hidden) { sp.hidden = true; return; } // toggle closed
  const r = anchor.getBoundingClientRect();
  sp.style.top = r.bottom + 6 + 'px';
  sp.style.left = Math.max(8, r.left) + 'px';
  sp.style.right = 'auto';
  sp.hidden = false;
}
$('sp-close').addEventListener('click', closeSearch);
$('search-pop').addEventListener('click', (e) => e.stopPropagation());
document.addEventListener('click', (e) => { if (!$('search-pop').hidden && !e.target.closest('#ctx-newsearch')) closeSearch(); });
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeSearch(); });

// ---- search params: field + location catalogs (from api/config) ----
let fields = [];               // [{key,label,roles}]
let selectedField = null;
let locationsCatalog = [];     // [{key,label,match}]
let selectedLocations = new Set(['ma', 'ny', 'ca']);

function renderFields() {
  const el = $('fields'); if (!el) return;
  el.innerHTML = fields.map((f) => `<button type="button" class="pchip${f.key === selectedField ? ' sel' : ''}" data-key="${esc(f.key)}">${esc(f.label)}</button>`).join('');
  el.querySelectorAll('.pchip').forEach((b) => b.addEventListener('click', () => { selectedField = b.dataset.key; renderFields(); }));
}
function renderLocations() {
  const el = $('locsel'); if (!el) return;
  el.innerHTML = locationsCatalog.map((l) => `<button type="button" class="pchip${selectedLocations.has(l.key) ? ' sel' : ''}" data-key="${esc(l.key)}">${esc(l.label)}</button>`).join('');
  el.querySelectorAll('.pchip').forEach((b) => b.addEventListener('click', () => { const k = b.dataset.key; selectedLocations.has(k) ? selectedLocations.delete(k) : selectedLocations.add(k); renderLocations(); }));
}
function expandLocations() {
  const terms = [];
  for (const l of locationsCatalog) if (selectedLocations.has(l.key)) terms.push(...(l.match || []));
  return terms;
}

// ---- collect the run request (server fills unspecified profile fields with defaults) ----
function collect() {
  const num = (id) => { const v = ($(id)?.value || '').replace(/[^0-9.]/g, ''); return v === '' ? 0 : Number(v); };
  const chk = (id) => !!$(id)?.checked;
  return {
    filters: {
      locations: expandLocations(),
      remote_ok: chk('remote_ok'),
      max_age_days: num('max_age_days'),
      min_salary: num('min_salary'),
      min_score: num('min_score'),
      include_ghosts: chk('include_ghosts'),
    },
    estimate_salary: chk('estimate_salary'),
    field: selectedField,
    role,
  };
}

// ---- scan: run + poll. A slim live strip shows ABOVE Scry (results stay in view). ----
// The strip stays hidden until a run starts; showRun keeps Scry mounted beneath it rather
// than swapping to a full-page takeover, so the prior results remain visible while scanning.
function showRun(on) { if (on) showScry(); $('runview').hidden = !on; }
function setRunButtons(disabled) { for (const id of ['run', 'ctx-newsearch', 'ctx-refresh']) { const b = $(id); if (b) b.disabled = disabled; } }
function resetLive() {
  $('runview').classList.remove('done');
  $('run-title').textContent = 'Starting…';
  $('live-done').textContent = '0'; $('live-total').textContent = '0';
  $('live-bar').style.width = '0%';
  $('run-note').hidden = true;
  $('live-spend').textContent = '$0.00 / $5.00';
}

// Drive the live strip from a progress payload (shared by the SSE feed and the poll
// fallback). One track follows the ACTIVE phase (scrape → verify); the running spend
// rides on the right, folding in the old separate Budget bar.
function applyProgress(j) {
  const cur = j.phase === 'apify' ? j.apify : j.verify;
  $('run-title').textContent = j.phase === 'apify' ? 'Scraping LinkedIn + Indeed…' : j.phase === 'verify' ? 'Verifying (ATS + Claude)…' : 'Finishing…';
  $('live-done').textContent = (cur.done || 0).toLocaleString();
  $('live-total').textContent = (cur.total || 0).toLocaleString();
  $('live-bar').style.width = (cur.total ? Math.round((cur.done / cur.total) * 100) : 0) + '%';
  $('run-note').hidden = !j.spends; // the "Live" tag only when the run actually spends
  $('live-spend').textContent = '$' + j.rate.used.toFixed(2) + ' / $' + j.rate.limit.toFixed(2);
}
function finishRun(scanned, shown) {
  $('runview').classList.add('done');
  $('live-bar').style.width = '100%';
  $('run-title').textContent = `Verified ${scanned.toLocaleString()} · ${shown.toLocaleString()} matched`;
  toast(`Scanned <b>${scanned.toLocaleString()}</b> · <b>${shown.toLocaleString()}</b> matched`);
  setStatus('');
  setRunButtons(false);
  // Let the green "done" strip land, then swap the streamed rows for the authoritative
  // reload (full aggregate, correct new-flags) — which also hides the strip.
  clearTimeout(pollTimer);
  pollTimer = setTimeout(() => reloadScry(), 1100);
}
function failRun(msg) { setStatus(msg, true); showRun(false); reloadScry(); setRunButtons(false); }

async function run() {
  clearTimeout(pollTimer);
  closeSearch();
  setRunButtons(true);
  resetLive();
  showRun(true);
  scryStreamStart(); // clear the grid; verified rows stream into it live
  try {
    const body = JSON.stringify(collect());
    const res = role === 'admin' ? await authFetch(api('run'), { method: 'POST', body }) : await fetch(api('run'), { method: 'POST', body });
    if (!res) throw new Error('Sign in as an admin to run a live search.');
    if (res.status === 429) { // demo scan quota reached — informational, not an error
      toast(await res.text());
      showRun(false); reloadScry(); setRunButtons(false);
      return;
    }
    if (!res.ok) throw new Error(await res.text());
    openStream((await res.json()).id);
  } catch (e) { failRun(e.message); }
}

// Read the run's Server-Sent Events via a fetch body reader (so the admin bearer token
// rides along, unlike EventSource). Rows stream into Scry as they verify; progress drives
// the strip. Falls back to polling if the stream can't be opened.
async function openStream(id) {
  const url = api('run/stream') + '?id=' + encodeURIComponent(id);
  let res;
  try {
    res = role === 'admin' ? await authFetch(url) : await fetch(url);
  } catch { return poll(id); }
  if (!res || !res.ok || !res.body || !res.body.getReader) return poll(id);
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  let ended = false;
  const handle = (chunk) => {
    let event = 'message', data = '';
    for (const line of chunk.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim();
      else if (line.startsWith('data:')) data += line.slice(5).trim();
    }
    if (!data) return;
    try {
      if (event === 'rows') scryStreamRows(JSON.parse(data));
      else if (event === 'progress') applyProgress(JSON.parse(data));
      else if (event === 'done') { const d = JSON.parse(data); finishRun(d.scanned || 0, d.shown || 0); ended = true; }
      else if (event === 'error') { const d = JSON.parse(data); failRun(d.message || 'run failed'); ended = true; }
    } catch { /* skip a malformed frame */ }
  };
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let sep;
      while ((sep = buf.indexOf('\n\n')) >= 0) {
        handle(buf.slice(0, sep));
        buf = buf.slice(sep + 2);
        if (ended) { try { await reader.cancel(); } catch { /* already closing */ } return; }
      }
    }
  } catch (e) { if (!ended) failRun(e.message); }
}

// Poll fallback: the same strip, no row streaming (the grid fills on done via reloadScry).
async function poll(id) {
  try {
    const res = await fetch(api('run') + '?id=' + encodeURIComponent(id));
    if (!res.ok) throw new Error(await res.text());
    const j = await res.json();
    applyProgress(j);
    if (j.status === 'done') { finishRun((j.verify && j.verify.total) || 0, (j.rows || []).length); return; }
    if (j.status === 'error') { failRun(j.error || 'run failed'); return; }
    pollTimer = setTimeout(() => poll(id), 350);
  } catch (e) { failRun(e.message); }
}
$('run').addEventListener('click', run);

// ---- refresh: prune delisted jobs, then reload Scry ----
async function doRefresh() {
  const b = $('ctx-refresh'); const label = b ? b.textContent : '';
  if (b) { b.disabled = true; b.textContent = 'Checking…'; }
  try {
    const res = await authFetch(api('refresh'), { method: 'POST' });
    if (!res) throw new Error('Sign in as an admin to refresh.');
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    if (data.demo) { toast('Demo mode — the shortlist stays fresh automatically'); if (b) { b.disabled = false; b.textContent = label; } return; }
    toast(`🗑 Cleaned up <b>${data.removed}</b> old job${data.removed === 1 ? '' : 's'}`);
    reloadScry();
  } catch (e) { setStatus(e.message, true); if (b) { b.disabled = false; b.textContent = label; } }
}

// ---- identity → run mode (the shared platform account button) ----
// nudgeGuest keeps the FAB present for signed-out visitors (a guest identity is
// established silently), and onUpgrade opens the full gate — which carries the
// "I have an account" sign-in door. Without these, signing out left no way back in:
// the FAB rendered empty and a guest's only action was another sign-out. Reload on
// completion so Scry/Conjure refetch under the resolved identity's bearer token.
mountAccountFab({
  nudgeGuest: true,
  onUpgrade: () => mountGate({ onDone: () => location.reload() }),
});
// Disclose demo limits to non-admins: the search popover carries the "one scan per week,
// sample results" note so a visitor knows the deal before launching a scan.
function updateDemoUI() { const el = $('sp-demo'); if (el) el.hidden = role === 'admin'; }
onIdentity(() => { role = isAdmin() ? 'admin' : 'guest'; updateDemoUI(); refreshTabCounts(); });

// ---- boot: load the field/location catalogs, then show Scry ----
fetch(api('config')).then((r) => r.json()).then((c) => {
  runCfg = c;
  role = isAdmin() ? 'admin' : 'guest';
  updateDemoUI();
  fields = c.fields || [];
  selectedField = fields[0] ? fields[0].key : null;
  renderFields();
  locationsCatalog = c.locations || [];
  renderLocations();
}).catch(() => { /* catalogs are best-effort; Scry still loads */ });

// ---- first-run guide: decode the three rooms + the divination funnel ----
// Shown once on first visit (localStorage), and re-openable anytime via the header "?".
const GUIDE_SEEN = 'jobomancer:guide-seen';
function openGuide() {
  if (document.getElementById('jb-guide')) return;
  const host = document.createElement('div');
  host.id = 'jb-guide';
  host.className = 'jb-guide';
  host.innerHTML = `<div class="jbg-backdrop"><div class="jbg" role="dialog" aria-modal="true" aria-label="How Jobomancer works">
    <button class="jbg-x" aria-label="Close">✕</button>
    <h2>Welcome to Jobomancer</h2>
    <p class="jbg-sub">Scry the real jobs from the ghosts, then Conjure the applications.</p>
    <div class="jbg-rooms">
      <div class="jbg-room scry"><span class="jbg-emoji">🔮</span><b>Scry</b><span>Browse verified jobs in a sortable, searchable grid. Tap the <b>★</b> to <b>Consecrate</b> a job — save it to your shortlist.</span></div>
      <div class="jbg-room conjure"><span class="jbg-emoji">🪄</span><b>Conjure</b><span>Your shortlist, ready to apply. <b>Discern</b> reads each posting into an apply card; open it, then mark it <b>Manifested</b>.</span></div>
      <div class="jbg-room codex"><span class="jbg-emoji">📖</span><b>Codex</b><span>Reusable cover letters, snippets, and quick links — with <code>{{tokens}}</code> filled in when you copy.</span></div>
    </div>
    <div class="jbg-legend"><b>The funnel:</b> <span class="jbg-step s1">★ Consecrate</span> → <span class="jbg-step s2">✨ Discern</span> → <span class="jbg-step s3">✓ Manifest</span></div>
    <p class="jbg-tip">Shortcuts: <kbd>g</kbd> then <kbd>s</kbd>/<kbd>c</kbd>/<kbd>x</kbd> to switch rooms · <kbd>/</kbd> to search · <kbd>?</kbd> for this guide</p>
    <button class="jbg-go">Got it — start scrying</button>
  </div></div>`;
  document.body.appendChild(host);
  const close = () => { host.remove(); try { localStorage.setItem(GUIDE_SEEN, '1'); } catch { /* private mode */ } document.removeEventListener('keydown', onKey); };
  const onKey = (e) => { if (e.key === 'Escape') close(); };
  document.addEventListener('keydown', onKey);
  host.addEventListener('click', (e) => {
    const t = e.target;
    if (t.classList.contains('jbg-backdrop') || t.closest('.jbg-x') || t.closest('.jbg-go')) close();
  });
}
$('jb-help')?.addEventListener('click', openGuide);
try { if (!localStorage.getItem(GUIDE_SEEN)) openGuide(); } catch { /* private mode — just skip the first-run guide */ }

// ---- keyboard shortcuts: ? opens the guide, g then s/c/x switches rooms ----
let gPending = 0;
document.addEventListener('keydown', (e) => {
  const t = e.target;
  if (t && /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return; // don't hijack typing
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (e.key === '?') { e.preventDefault(); openGuide(); return; }
  if (document.getElementById('jb-guide')) return; // the guide modal owns the keyboard
  if (e.key === 'g') { gPending = Date.now(); return; }
  if (gPending && Date.now() - gPending < 1200) {
    const room = { s: 'scry', c: 'conjure', x: 'codex' }[e.key];
    if (room) { setMode(room); }
    gPending = 0;
  }
});

showScry();
