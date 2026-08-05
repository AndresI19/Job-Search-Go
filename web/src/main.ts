// @ts-nocheck — Jobomancer shell. A purpose-built two-room shell (Scry / Conjure) that
// replaced the legacy tabbed single-page monolith. It owns only the chrome: the mode
// switch, the search popover, the scan run/progress, and Refresh. The two views are the
// typed modules web/src/{scry,conjure}.ts, which read the domain endpoints directly.
import '@platform/ui/tokens.css';
import '@platform/ui/base.css';
import '@platform/ui/gate.css';
import './app.css';
import { mountScry } from './scry';
import { mountConjure } from './conjure';
import { mountAccountFab, mountGate } from '@platform/ui/gate';
import { authFetch, isAdmin, onIdentity } from '@platform/ui/auth';

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
  for (const b of document.querySelectorAll('#jb-modes button')) b.classList.toggle('on', b.dataset.mode === m);
  $('search-pop').hidden = true;
  if (m === 'scry') showScry(); else showConjure();
}
function showScry() {
  $('runview').hidden = true;
  $('scry-root').hidden = false;
  $('conjure-root').hidden = true;
  if (!scryMounted) { mountScry($('scry-root'), { authFetch, onNewSearch: openSearchAt, onRefresh: doRefresh }); scryMounted = true; }
}
function showConjure() {
  $('runview').hidden = true;
  $('scry-root').hidden = true;
  $('conjure-root').hidden = false;
  mountConjure($('conjure-root'), { api, authFetch }); // re-fetch each show (reflect fresh Consecrates)
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

async function run() {
  clearTimeout(pollTimer);
  closeSearch();
  setRunButtons(true);
  resetLive();
  showRun(true);
  try {
    const body = JSON.stringify(collect());
    const res = role === 'admin' ? await authFetch(api('run'), { method: 'POST', body }) : await fetch(api('run'), { method: 'POST', body });
    if (!res) throw new Error('Sign in as an admin to run a live search.');
    if (!res.ok) throw new Error(await res.text());
    poll((await res.json()).id);
  } catch (e) { setStatus(e.message, true); showRun(false); showScry(); setRunButtons(false); }
}
async function poll(id) {
  try {
    const res = await fetch(api('run') + '?id=' + encodeURIComponent(id));
    if (!res.ok) throw new Error(await res.text());
    const j = await res.json();
    // One track follows the ACTIVE phase (scrape → verify); the label names it, and the
    // running spend rides on the right. The Budget bar is folded into that single readout.
    const cur = j.phase === 'apify' ? j.apify : j.verify;
    $('run-title').textContent = j.phase === 'apify' ? 'Scraping LinkedIn + Indeed…' : j.phase === 'verify' ? 'Verifying (ATS + Claude)…' : 'Finishing…';
    $('live-done').textContent = (cur.done || 0).toLocaleString();
    $('live-total').textContent = (cur.total || 0).toLocaleString();
    $('live-bar').style.width = (cur.total ? Math.round((cur.done / cur.total) * 100) : 0) + '%';
    $('run-note').hidden = !j.spends; // the "Live" tag only when the run actually spends
    $('live-spend').textContent = '$' + j.rate.used.toFixed(2) + ' / $' + j.rate.limit.toFixed(2);
    if (j.status === 'done') {
      const shown = (j.rows || []).length;
      const scanned = (j.verify && j.verify.total) || shown;
      $('runview').classList.add('done');
      $('live-bar').style.width = '100%';
      $('run-title').textContent = `Verified ${scanned.toLocaleString()} · ${shown.toLocaleString()} matched`;
      toast(`Scanned <b>${scanned.toLocaleString()}</b> · <b>${shown.toLocaleString()}</b> matched`);
      setStatus('');
      setRunButtons(false);
      // Let the green "done" strip land, then swap in the fresh rows (which hides the strip).
      clearTimeout(pollTimer);
      pollTimer = setTimeout(() => reloadScry(), 1100);
      return;
    }
    if (j.status === 'error') { setStatus(j.error || 'run failed', true); showRun(false); showScry(); setRunButtons(false); return; }
    pollTimer = setTimeout(() => poll(id), 350);
  } catch (e) { setStatus(e.message, true); showRun(false); showScry(); setRunButtons(false); }
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
    const { removed } = await res.json();
    toast(`🗑 Cleaned up <b>${removed}</b> old job${removed === 1 ? '' : 's'}`);
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
onIdentity(() => { role = isAdmin() ? 'admin' : 'guest'; });

// ---- boot: load the field/location catalogs, then show Scry ----
fetch(api('config')).then((r) => r.json()).then((c) => {
  runCfg = c;
  role = isAdmin() ? 'admin' : 'guest';
  fields = c.fields || [];
  selectedField = fields[0] ? fields[0].key : null;
  renderFields();
  locationsCatalog = c.locations || [];
  renderLocations();
}).catch(() => { /* catalogs are best-effort; Scry still loads */ });

showScry();
