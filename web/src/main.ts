// @ts-nocheck — a faithful port of the original self-contained index.html page (untyped
// vanilla JS: getElementById().value and friends). It is transpiled by Vite/esbuild, not
// type-checked; the @platform/ui wiring below is exercised by the build and E2E, not tsc.
import '@platform/ui/tokens.css';
import '@platform/ui/base.css';
import '@platform/ui/gate.css';
import './app.css';
import { mountScry } from './scry'; // the typed Scry data-grid (reimagined Discover view)
import { mountConjure } from './conjure'; // the typed Conjure card board (reimagined Apply view)
import { mountAccountFab } from '@platform/ui/gate';
import { authFetch, current, isAdmin, onIdentity } from '@platform/ui/auth';

// The URL prefix this app is mounted under ("/" on its own, "/job-searcher/" behind
// the platform router). Vite bakes it into import.meta.env.BASE_URL at build time, so
// every same-origin API call is built from it and the app works at either mount point.
const BASE = import.meta.env.BASE_URL;
const api = (p) => BASE + 'api/' + p;

const $ = id => document.getElementById(id);

// Thousands-separated number inputs (class="numsep"): the field shows "10,000" but
// the user never types a comma, and every reader strips them back out. Applies to the
// large-integer fields (job count, salaries, headcount) that routinely reach 4+ digits.
const stripSep = s => String(s ?? '').replace(/,/g, '');
const digitsOnly = s => stripSep(s).replace(/\D/g, '');
const groupThousands = d => (d ? Number(d).toLocaleString('en-US') : '');
// Comma-safe numeric read: use everywhere a menu input's number is needed.
const numOf = id => Number(stripSep($(id).value));
// Reformat one field to grouped digits while keeping the caret in the right place
// (count digits left of the caret, restore after the same count past any commas).
function formatSepInput(el) {
  const digitsLeft = digitsOnly(el.value.slice(0, el.selectionStart ?? el.value.length)).length;
  el.value = groupThousands(digitsOnly(el.value));
  let pos = 0, seen = 0;
  while (pos < el.value.length && seen < digitsLeft) { if (/\d/.test(el.value[pos])) seen++; pos++; }
  try { el.setSelectionRange(pos, pos); } catch (e) { /* not focused — ignore */ }
}
function formatAllSep() { for (const el of document.querySelectorAll('.numsep')) el.value = groupThousands(digitsOnly(el.value)); }
// There is no job-count knob any more: every run pulls each board's ~1,000 ceiling
// and merges them (see the server's perBoardMax). The remaining numsep fields are the
// salary bounds and startup max — they just comma-format live, with no cap logic.
function wireThousands() {
  for (const el of document.querySelectorAll('.numsep')) {
    el.addEventListener('input', () => formatSepInput(el));
  }
  formatAllSep();
}

// Transient bottom-centre toast, auto-dismissed. Used for the post-run scan summary.
let toastTimer = 0;
function toast(html) {
  const el = $('toast'); if (!el) return;
  el.innerHTML = html;
  el.hidden = false;
  void el.offsetWidth;        // reflow so the fade-in transition actually runs
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    el.classList.remove('show');
    setTimeout(() => { el.hidden = true; }, 300);
  }, 6500);
}

let sortState = { col: -1, dir: 1 };
let last = null;
try { const s = localStorage.getItem('jobsearch.last'); if (s) last = JSON.parse(s); } catch (e) { /* ignore */ }
// Set the current Results and persist them so a refresh keeps the table.
function setLast(v) {
  last = v;
  if (v && v.columns && v.columns.length) savedColumns = v.columns;
  try { localStorage.setItem('jobsearch.last', v ? JSON.stringify(v) : ''); } catch (e) { /* quota — ignore */ }
}
let page = 0;
const PAGE_SIZE = 40;
// Run mode, DERIVED from the platform identity (not a manual toggle): 'admin' once a
// signed-in admin is present, else 'guest' (the open $0 mock). onIdentity below keeps
// it in step with the account FAB's sign-in / sign-out.
let role = 'guest';
let runCfg = { realReady: false, spends: false };    // from /api/config

// Pinned ("liked") jobs — keyed by listing URL (falls back to title), persisted so
// pins survive reloads. A pinned row is highlighted across its whole width.
let pinned = new Set();
try { pinned = new Set(JSON.parse(localStorage.getItem('jobsearch.pins') || '[]')); } catch (e) { /* ignore */ }
function savePins() { try { localStorage.setItem('jobsearch.pins', JSON.stringify([...pinned])); } catch (e) { /* ignore */ } }

// Applied ("I applied to this") jobs — same keying as pins, persisted, and included
// in the CSV export so the applied set is portable.
let applied = new Set();
try { applied = new Set(JSON.parse(localStorage.getItem('jobsearch.applied') || '[]')); } catch (e) { /* ignore */ }
function saveApplied() { try { localStorage.setItem('jobsearch.applied', JSON.stringify([...applied])); } catch (e) { /* ignore */ } }

// Saved jobs the availability sweep has retired (the posting 404s now). Reported by
// the server per Saved-tab load; the row stays visible, tagged "no longer listed",
// rather than disappearing. Session-only — it's re-derived from the server each load.
let unavailable = new Set();
const rowKeyOf = (r, idxTitle) => r.url || (r.cells[idxTitle] ? r.cells[idxTitle].value : '');
const RUN_YEAR = new Date().getFullYear(); // drop the year on postings from this same year

// Saved jobs (favorites + applied) are kept as full row SNAPSHOTS so the Saved tab
// survives a refresh even without re-running. The current Results table is persisted
// too (see setLast), so refreshing no longer loses it.
let savedRows = [], savedColumns = [], activeTab = 'results';
// The Aggregate tab's data (persisted listings from the server) and its "New only"
// filter. Fetched on demand from /api/listings; empty when persistence is off.
let aggregateData = { columns: [], rows: [] };
let aggNewOnly = false;
// The data the table renders for the active tab: saved snapshots, the persisted
// aggregate, or the current run/preview set.
function currentData() {
  // Saved (starred) and Applied are INDEPENDENT views over the same snapshot pool —
  // a job can be starred, applied, both, or neither. Aggregate is the persisted set;
  // otherwise the current run/preview.
  const idx = savedColumns.indexOf('title');
  const keyOf = r => r.__key || rowKeyOf(r, idx);
  if (activeTab === 'saved') return { columns: savedColumns, rows: savedRows.filter(r => pinned.has(keyOf(r))) };
  if (activeTab === 'applied') return { columns: savedColumns, rows: savedRows.filter(r => applied.has(keyOf(r))) };
  if (activeTab === 'aggregate') return aggregateData;
  return last;
}
try { savedRows = JSON.parse(localStorage.getItem('jobsearch.savedRows') || '[]'); } catch (e) { /* ignore */ }
try { savedColumns = JSON.parse(localStorage.getItem('jobsearch.savedCols') || '[]'); } catch (e) { /* ignore */ }
function saveStore() {
  try {
    localStorage.setItem('jobsearch.savedRows', JSON.stringify(savedRows));
    localStorage.setItem('jobsearch.savedCols', JSON.stringify(savedColumns));
  } catch (e) { /* quota — ignore */ }
}
function findRowByKey(key) {
  if (last && last.rows) { const it = last.columns.indexOf('title'); const r = last.rows.find(x => rowKeyOf(x, it) === key); if (r) return r; }
  return savedRows.find(x => x.__key === key) || null;
}
// Keep the saved-snapshot registry in step with the pin/applied sets.
function reconcileSaved(key) {
  const isSaved = pinned.has(key) || applied.has(key);
  const idx = savedRows.findIndex(x => x.__key === key);
  if (isSaved && idx < 0) {
    const row = findRowByKey(key);
    if (row) { savedRows.push({ ...row, __key: key }); if (last && last.columns && last.columns.length) savedColumns = last.columns; }
  } else if (!isSaved && idx >= 0) {
    savedRows.splice(idx, 1);
  }
  saveStore();
}

function populate(p) {
  $('remote_ok').checked = p.filters.remote_ok;
  $('max_age_days').value = p.filters.max_age_days;
  $('min_salary').value = p.filters.min_salary;
  $('min_score').value = p.filters.min_score;
  $('include_ghosts').checked = p.filters.include_ghosts;
  $('salary_light').value = p.highlight.salary_light;
  $('salary_strong').value = p.highlight.salary_strong;
  $('startup_max').value = p.highlight.startup_max;
  $('fresh').value = p.highlight.recency_fresh_days;
  $('recent').value = p.highlight.recency_recent_days;
  $('aging').value = p.highlight.recency_aging_days;
  $('estimate_salary').checked = p.estimate_salary;
  formatAllSep(); // the values just set are raw numbers — group them with commas
}

function collect() {
  const n = numOf;
  const c = id => $(id).checked;
  return {
    filters: {
      locations: expandLocations(),
      remote_ok: c('remote_ok'), max_age_days: n('max_age_days'),
      min_salary: n('min_salary'), min_score: n('min_score'), include_ghosts: c('include_ghosts'),
    },
    highlight: {
      salary_light: n('salary_light'), salary_strong: n('salary_strong'), startup_max: n('startup_max'),
      recency_fresh_days: n('fresh'), recency_recent_days: n('recent'), recency_aging_days: n('aging'),
    },
    estimate_salary: c('estimate_salary'),
    field: selectedField,
    role: role,
  };
}

function setStatus(msg, err) { const s = $('status'); s.textContent = msg; s.className = 'status' + (err ? ' err' : ''); }

function esc(s) { return (s == null ? '' : String(s)).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;'); }
function isNumericCol(name) {
  return ['company_size', 'years_experience', 'salary_min', 'salary_max', 'salary_est_min', 'salary_est_max', 'score', 'applicants'].includes(name);
}
// Short header labels for the widest columns — trims the empty space between a long header and its
// short value. Only the displayed <th> text changes; the real column names still drive the logic.
const SHORT = {
  company_size: 'size', years_experience: 'yrs', apply_type: 'apply',
  salary_min: 'pay min', salary_max: 'pay max', salary_est_min: 'est min', salary_est_max: 'est max',
  applicants: 'appl', // caps at 200, so the column stays narrow
};
const titleCase = s => s.replace(/_/g, ' ').replace(/\b\w/g, m => m.toUpperCase());
const label = c => titleCase(SHORT[c] || c);
const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
function fmt(name, v) {
  if (v === '' || v == null) return '';
  if (name.startsWith('salary')) { const n = Number(v); return isNaN(n) ? esc(v) : '$' + n.toLocaleString(); }
  if (name === 'applicants') { const n = Number(v); return isNaN(n) ? esc(v) : (n >= 200 ? '200+' : String(n)); }
  if (name === 'posted') {
    const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(String(v));
    if (m) { const lab = MON[Number(m[2]) - 1] + ' ' + Number(m[3]); return Number(m[1]) === RUN_YEAR ? lab : lab + ' ' + m[1]; }
  }
  return esc(v);
}

// Column-specific colour legends, sitting above the column they explain. Labels
// reflect the current threshold inputs.
function chip(hex, label) { return `<span class="chip"><span class="sw" style="background:#${hex}"></span>${label}</span>`; }
function legendChips(col) {
  const n = numOf;
  const k = v => '$' + Math.round(v / 1000) + 'k';
  if (col === 'company') return chip('FFE699', 'F500') + chip('F4B183', 'F1000') + chip('9BC2E6', 'Software') + chip('D9C2E9', 'Startup');
  if (col === 'salary_min') return chip('E2EFDA', '≥' + k(n('salary_light'))) + chip('A9D08E', '≥' + k(n('salary_strong')));
  // Wrapped 2×2 so the recency legend doesn't stretch the narrow posted column.
  if (col === 'posted') return `<span class="chips2">${chip('92D050', '≤' + n('fresh') + 'd')}${chip('FFEB9C', '≤' + n('recent') + 'd')}${chip('FFC000', '≤' + n('aging') + 'd')}${chip('FF9999', '>' + n('aging') + 'd')}</span>`;
  return '';
}

// Faceted result filters, both behind the Filters popover. Company categories map a
// company-cell fill colour to a key; locations are the normalized labels present in the
// current result set (built exhaustively on each panel open). Both empty = show all.
const FILL_CAT = { FFE699: 'f500', F4B183: 'f1000', '9BC2E6': 'software', D9C2E9: 'startup' };
let catFilter = new Set(); // active company categories
let locFilter = new Set(); // active location labels

// Rebuild the Location chips from the CURRENT view's rows, so the facet is exhaustive
// and always reflects what's actually on screen. Preserves any still-valid selections.
function buildLocOptions() {
  const el = $('loc-filters'); if (!el) return;
  const d = currentData();
  const lc = d && d.columns ? d.columns.indexOf('location') : -1;
  const labels = new Set();
  if (lc >= 0) for (const r of (d.rows || [])) { const v = normalizeLocation(r.cells[lc] ? r.cells[lc].value : ''); if (v) labels.add(v); }
  // Drop selections that no longer exist in this view so the badge stays truthful.
  for (const l of [...locFilter]) if (!labels.has(l)) locFilter.delete(l);
  if (!labels.size) { el.innerHTML = '<span class="filt-empty">Run a search to filter by location.</span>'; return; }
  el.innerHTML = [...labels].sort((a, b) => a.localeCompare(b)).map(l =>
    `<button class="fchip${locFilter.has(l) ? ' sel' : ''}" data-loc="${esc(l)}"><i style="background:${hashColor(l)}"></i>${esc(l)}</button>`).join('');
}

function updateFilterBadge() {
  const n = catFilter.size + locFilter.size;
  const badge = $('filter-badge'); if (badge) { badge.textContent = n; badge.hidden = n === 0; }
  $('filter-btn')?.classList.toggle('on', n > 0);
}

function emptyMsg() {
  if (activeTab === 'saved') return 'No starred jobs yet — hit ☆ on a job and it lands here, kept across refreshes.';
  if (activeTab === 'applied') return 'No applied jobs yet — tick a job\'s checkbox to track what you applied to.';
  if (activeTab === 'aggregate') return 'No persisted listings yet — an admin live run fills this.';
  return 'Set your filters, then press <b>Run</b> to fetch a suite of jobs.';
}
// The active facet filter (company + location): OR within a facet, AND across facets;
// an empty facet imposes no constraint. Shared by the table render AND the tab counts,
// so a badge can never claim more than the table actually shows.
const anyFacet = () => catFilter.size > 0 || locFilter.size > 0;
function facetFilter(rows, columns) {
  if (!anyFacet()) return rows;
  const cc = columns.indexOf('company');
  const lc = columns.indexOf('location');
  return rows.filter(r => {
    const okCat = !catFilter.size || (cc >= 0 && catFilter.has(FILL_CAT[r.cells[cc] && r.cells[cc].fill]));
    const okLoc = !locFilter.size || (lc >= 0 && locFilter.has(normalizeLocation(r.cells[lc] ? r.cells[lc].value : '')));
    return okCat && okLoc;
  });
}
function renderTabs() {
  const t = $('tabs'); if (!t) return;
  for (const btn of t.querySelectorAll('.tab-btn')) btn.classList.toggle('active', btn.dataset.tab === activeTab);
  // Badge shows the total you saved, or "shown / total" whenever fewer are on screen than
  // saved — so an active filter (or any hidden row) can never make the count a lie.
  const idx = savedColumns.indexOf('title');
  const keyOf = r => r.__key || rowKeyOf(r, idx);
  const badge = (el, total, set) => {
    if (!el) return;
    const shown = facetFilter(savedRows.filter(r => set.has(keyOf(r))), savedColumns).length;
    el.textContent = shown < total ? `${shown} / ${total}` : String(total);
  };
  badge($('saved-count'), pinned.size, pinned);   // starred
  badge($('applied-count'), applied.size, applied); // applied
  // The Aggregate-only controls (New-only + Refresh) show only on that tab.
  const ctl = $('agg-controls'); if (ctl) ctl.hidden = activeTab !== 'aggregate';
  const sc2 = $('saved-controls'); if (sc2) sc2.hidden = activeTab !== 'saved';
  const ac2 = $('applicator-controls'); if (ac2) ac2.hidden = activeTab !== 'applicator';
}
function render() {
  renderTabs();
  // Leaving the Scry / Conjure views: hide their containers and restore the legacy table.
  // render() only runs for legacy tabs (never during a run), so unhiding is safe here.
  const sr = $('scry-root'); if (sr) sr.hidden = true;
  const cr = $('conjure-root'); if (cr) cr.hidden = true;
  const tw = document.querySelector('.tablewrap'); if (tw) tw.style.display = '';
  const data = currentData();
  if (!data || !data.columns || !data.columns.length || !data.rows) {
    $('table').outerHTML = '<div class="empty" id="table">' + emptyMsg() + '</div>';
    $('pager').innerHTML = '';
    return;
  }
  const { columns } = data;
  // Faceted filter (not sort), via the shared helper so the tab counts stay in lock-step.
  // F500 is matched before F1000, so the F1000 chip is the 501–1000 tier; combine to widen.
  let rows = facetFilter(data.rows, columns);
  if (!rows.length) {
    $('table').outerHTML = '<div class="empty" id="table">' + (anyFacet() ? 'No listings match the selected filters.' : 'No listings match this profile.') + '</div>';
    $('pager').innerHTML = '';
    return;
  }

  // Paginate: 40 to a page. Clamp so a shrunk result set (after a sort/re-filter) can't strand us on
  // a page that no longer exists.
  const pages = Math.ceil(rows.length / PAGE_SIZE);
  page = Math.max(0, Math.min(page, pages - 1));
  const start = page * PAGE_SIZE;
  const pageRows = rows.slice(start, start + PAGE_SIZE);

  // No native title= attributes: the delayed tooltip (below) covers every clipped cell, and two
  // tooltip systems on one cell fight.
  const idxTitle = columns.indexOf('title');
  const body = pageRows.map(r => {
    const key = rowKeyOf(r, idxTitle);
    const isPinned = !!key && pinned.has(key);
    const isApplied = !!key && applied.has(key);
    const pin = `<td class="pin"><button class="pinbtn${isPinned ? ' on' : ''}" data-key="${esc(key)}" title="${isPinned ? 'Unpin' : 'Pin'} this job" aria-label="pin job">${isPinned ? '★' : '☆'}</button></td>`;
    const app = `<td class="applied"><input type="checkbox" class="appliedbox" data-key="${esc(key)}"${isApplied ? ' checked' : ''} title="Mark as applied" aria-label="applied"></td>`;
    const info = r.info ? `<td class="info" data-info="${esc(r.info)}">ⓘ</td>` : '<td class="info"></td>';
    const tds = info + r.cells.map((cell, i) => {
      const name = columns[i];
      let bg = cell.fill ? ` style="background:#${cell.fill}"` : '';
      let val = fmt(name, cell.value), cls = '';
      if (i === idxTitle) {
        cls = 'title';
        if (r.url) val = `<a href="${esc(r.url)}" target="_blank" rel="noopener">${val}</a>`;
        // A saved job the availability sweep retired: keep it, but flag it so the
        // link isn't mistaken for a still-open posting.
        if (key && unavailable.has(key)) val += ' <span class="gone" title="This posting was not reachable at the last refresh">no longer listed</span>';
      } else if (name === 'remote') {
        const yes = cell.value === 'true';
        val = yes ? '✓' : (cell.value === 'false' ? '✗' : '');
        cls = 'remote ' + (yes ? 'yes' : 'no');
      } else if (name === 'location') {
        cls = 'location';
        const norm = normalizeLocation(cell.value); // collapse to a supported label
        val = esc(norm);
        bg = norm ? ` style="background:${hashColor(norm)}"` : '';
      } else if (name === 'reasoning') {
        cls = 'reason';
      } else if (isNumericCol(name)) {
        cls = 'num';
      }
      if (name.startsWith('salary_est')) cls += (cls ? ' ' : '') + 'est'; // estimates read as less confident
      let td = `<td class="${cls}"${bg}>${val}</td>`;
      if (i === idxTitle) { // inject the Role column right after title
        const rl = classifyRole(cell.value);
        td += `<td class="role" style="background:${hashColor(rl)}">${esc(rl)}</td>`;
      }
      return td;
    }).join('');
    return `<tr${isPinned ? ' class="pinned"' : ''}>${pin}${app}${tds}</tr>`;
  }).join('');

  const legrow = '<th></th><th></th><th></th>' + columns.map((c, i) => `<th class="legcell">${legendChips(c)}</th>` + (i === idxTitle ? '<th></th>' : '')).join('');
  const head = '<th class="pinh"></th><th class="appliedh">✓</th><th class="infoh"></th>' + columns.map((c, i) => `<th data-col="${i}" class="${isNumericCol(c) ? 'num' : ''}${c.startsWith('salary_est') ? ' est' : ''}">${label(c)}</th>` + (i === idxTitle ? '<th class="roleh">Role</th>' : '')).join('');
  $('table').outerHTML = `<table id="table"><thead><tr class="legrow">${legrow}</tr><tr>${head}</tr></thead><tbody>${body}</tbody></table>`;
  document.querySelectorAll('#table thead tr:last-child th[data-col]').forEach(th =>
    th.addEventListener('click', () => sortBy(Number(th.dataset.col))));
  document.querySelectorAll('#table .pinbtn').forEach(b => b.addEventListener('click', e => {
    e.stopPropagation();
    const key = b.dataset.key; if (!key) return;
    pinned.has(key) ? pinned.delete(key) : pinned.add(key);
    savePins(); reconcileSaved(key); pushSaved(key);
    render();
  }));
  document.querySelectorAll('#table .appliedbox').forEach(b => b.addEventListener('change', () => {
    const key = b.dataset.key; if (!key) return;
    b.checked ? applied.add(key) : applied.delete(key);
    saveApplied(); reconcileSaved(key); pushSaved(key); renderTabs();
    if (activeTab === 'saved' || activeTab === 'applied') render(); // Results shows the live checkbox already
  }));

  renderPager(rows.length, pages, start);
}

function renderPager(total, pages, start) {
  const end = Math.min(start + PAGE_SIZE, total);
  if (pages <= 1) { $('pager').innerHTML = `<span>${total} listing${total === 1 ? '' : 's'}</span>`; return; }
  $('pager').innerHTML =
    `<button ${page === 0 ? 'disabled' : ''} data-d="-1">‹ Prev</button>` +
    `<span class="tab">${start + 1}–${end} of ${total}  ·  page ${page + 1} of ${pages}</span>` +
    `<button ${page >= pages - 1 ? 'disabled' : ''} data-d="1">Next ›</button>`;
  $('pager').querySelectorAll('button[data-d]').forEach(b =>
    b.addEventListener('click', () => { page += Number(b.dataset.d); render(); document.querySelector('.tablewrap').scrollTop = 0; }));
}

function sortBy(col) {
  const data = currentData();
  if (!data || !data.rows) return;
  sortState.dir = sortState.col === col ? -sortState.dir : 1;
  sortState.col = col;
  page = 0; // a re-sort re-orders the whole set — start from the top of it
  const num = isNumericCol(data.columns[col]);
  const cmp = (a, b) => {
    const x = a.cells[col] ? a.cells[col].value : '', y = b.cells[col] ? b.cells[col].value : '';
    if (num) return ((Number(x) || 0) - (Number(y) || 0)) * sortState.dir;
    return String(x).localeCompare(String(y)) * sortState.dir;
  };
  // Sort the BACKING array, not the view. currentData() hands the Saved/Applied tabs a
  // fresh savedRows.filter(...) copy each render, so sorting that copy would be thrown
  // away on the next render — the reorder has to land on savedRows itself to stick.
  const saved = activeTab === 'saved' || activeTab === 'applied';
  const backing = saved ? savedRows : activeTab === 'aggregate' ? aggregateData.rows : (last && last.rows);
  if (!backing) return;
  backing.sort(cmp);
  if (saved) saveStore(); else persistLastRows();
  render();
}
function persistLastRows() { try { if (last) localStorage.setItem('jobsearch.last', JSON.stringify(last)); } catch (e) { /* quota */ } }

async function preview() {
  setStatus('Filtering cached results…');
  try {
    const res = await fetch(api('preview'), { method: 'POST', body: JSON.stringify(collect()) });
    if (!res.ok) throw new Error(await res.text());
    setLast(await res.json());
    sortState = { col: -1, dir: 1 };
    page = 0;
    render();
    $('status').innerHTML = `<span class="count">${last.kept}</span> of ${last.total} verified listings match.`;
    $('status').className = 'status';
  } catch (e) { setStatus(e.message, true); }
}

// --- Run: fetch a fresh suite of jobs and watch its progress -----------------------------------
let pollTimer = 0;
function setBar(name, done, total) {
  $('bar-' + name).style.width = (total ? Math.round(done / total * 100) : 0) + '%';
  $('num-' + name).textContent = done + ' / ' + total;
}
function showRunview(on) {
  $('runview').hidden = !on;
  document.querySelector('.tablewrap').style.display = on ? 'none' : '';
}
function resetRun() { const b = $('run'); b.disabled = false; b.innerHTML = '▶&nbsp;&nbsp;Run search'; }

async function run() {
  clearTimeout(pollTimer);
  const b = $('run'); b.disabled = true; b.textContent = 'Running…';
  $('pager').innerHTML = '';
  setBar('apify', 0, 10); setBar('verify', 0, 10);
  $('run-title').textContent = 'Starting…';
  showRunview(true);
  try {
    // An admin's real run must carry the platform bearer so the server can verify the
    // signed admin claim; a guest's run is the open $0 mock and needs no token.
    // authFetch attaches a fresh token and returns null when there is no identity.
    const body = JSON.stringify(collect());
    const res = role === 'admin'
      ? await authFetch(api('run'), { method: 'POST', body })
      : await fetch(api('run'), { method: 'POST', body });
    if (!res) throw new Error('Sign in as an admin to run a live search.');
    if (!res.ok) throw new Error(await res.text());
    poll((await res.json()).id);
  } catch (e) { setStatus(e.message, true); showRunview(false); resetRun(); }
}

async function poll(id) {
  try {
    const res = await fetch(api('run') + '?id=' + encodeURIComponent(id));
    if (!res.ok) throw new Error(await res.text());
    const j = await res.json();
    setBar('apify', j.apify.done, j.apify.total);
    setBar('verify', j.verify.done, j.verify.total);
    $('run-target').textContent = j.apify.total ? `Target: up to ${groupThousands(String(j.apify.total))} jobs · LinkedIn + Indeed` : '';
    $('bar-rate').style.width = Math.min(100, j.rate.used / j.rate.limit * 100) + '%';
    $('num-rate').textContent = '$' + j.rate.used.toFixed(2) + ' / $' + j.rate.limit.toFixed(2);
    $('run-title').textContent =
      j.phase === 'apify' ? 'Scraping via Apify…' : j.phase === 'verify' ? 'Verifying (ATS + Claude)…' : 'Done';
    $('run-note').textContent = j.spends ? 'LIVE · spends Apify + Claude' : 'mock · $0 (no Apify / Claude)';
    if (j.status === 'done') {
      const rows = j.rows || [];
      setLast({ columns: j.columns || [], rows: rows, kept: rows.length, total: rows.length });
      sortState = { col: -1, dir: 1 }; page = 0;
      showRunview(false);
      render();
      // Honest scan summary: how many the scrape actually returned (verify.total),
      // how many the profile filters dropped before rendering, and how many remain.
      const shown = rows.length;
      const scanned = (j.verify && j.verify.total) || shown;
      const filtered = Math.max(0, scanned - shown);
      const g = v => groupThousands(String(v));
      toast(filtered > 0
        ? `Scanned <b>${g(scanned)}</b> · filtered out <b>${g(filtered)}</b> · showing <b>${g(shown)}</b>`
        : `Scanned <b>${g(scanned)}</b> · showing <b>${g(shown)}</b>`);
      $('status').innerHTML = shown
        ? `Run complete — <span class="count">${g(shown)}</span> shown.`
        : `Run complete — <span class="count">0</span> matched your filters (try loosening them).`;
      $('status').className = 'status';
      resetRun();
      return;
    }
    if (j.status === 'error') { setStatus(j.error || 'run failed', true); showRunview(false); resetRun(); return; }
    pollTimer = setTimeout(() => poll(id), 350);
  } catch (e) { setStatus(e.message, true); resetRun(); }
}

$('run').addEventListener('click', run);

// Export the current results as CSV; import a previously-exported one back.
// Export the current results client-side so the applied/pinned flags travel with the
// data (the server export doesn't know about them — they're UI state).
const csvCell = v => { v = (v == null ? '' : String(v)); return /[",\r\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v; };
$('export').addEventListener('click', () => {
  const data = currentData();
  if (!data || !data.rows || !data.rows.length) { setStatus('Nothing to export yet', true); return; }
  try {
    const { columns, rows } = data;
    const idxTitle = columns.indexOf('title');
    const out = [['applied', 'pinned', ...columns]];
    for (const r of rows) {
      const key = rowKeyOf(r, idxTitle);
      out.push([applied.has(key) ? 'yes' : 'no', pinned.has(key) ? 'yes' : 'no', ...r.cells.map(c => c.value)]);
    }
    const csv = '﻿' + out.map(row => row.map(csvCell).join(',')).join('\r\n');
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }));
    a.download = activeTab === 'saved' ? 'job-search-saved.csv' : 'job-search-results.csv'; a.click();
    URL.revokeObjectURL(a.href);
    setStatus(`Exported ${rows.length} ${activeTab === 'saved' ? 'saved' : ''} rows (with applied/pinned)`);
  } catch (e) { setStatus(e.message, true); }
});
document.querySelectorAll('#cat-filters .fchip').forEach(b => b.addEventListener('click', () => {
  const cat = b.dataset.cat;
  catFilter.has(cat) ? catFilter.delete(cat) : catFilter.add(cat);
  b.classList.toggle('sel');
  page = 0; updateFilterBadge(); render();
}));
// Location chips are rebuilt on each panel open, so bind the toggle by delegation.
$('loc-filters')?.addEventListener('click', e => {
  const b = e.target.closest('.fchip'); if (!b) return;
  const loc = b.dataset.loc;
  locFilter.has(loc) ? locFilter.delete(loc) : locFilter.add(loc);
  b.classList.toggle('sel');
  page = 0; updateFilterBadge(); render();
});
// The Filters popover: open builds the (exhaustive) location list; clear resets both facets.
function setFilterPanel(open) {
  const panel = $('filter-panel'), btn = $('filter-btn');
  if (!panel || !btn) return;
  if (open) buildLocOptions();
  panel.hidden = !open;
  btn.setAttribute('aria-expanded', String(open));
}
$('filter-btn')?.addEventListener('click', e => { e.stopPropagation(); setFilterPanel($('filter-panel').hidden); });
$('filter-panel')?.addEventListener('click', e => e.stopPropagation()); // clicks inside stay open
$('filter-clear')?.addEventListener('click', () => {
  catFilter.clear(); locFilter.clear();
  document.querySelectorAll('#cat-filters .fchip.sel, #loc-filters .fchip.sel').forEach(c => c.classList.remove('sel'));
  page = 0; updateFilterBadge(); render();
});
document.addEventListener('click', () => { if (!$('filter-panel')?.hidden) setFilterPanel(false); });
document.addEventListener('keydown', e => { if (e.key === 'Escape' && !$('filter-panel')?.hidden) setFilterPanel(false); });
document.querySelectorAll('#tabs .tab-btn').forEach(b => b.addEventListener('click', () => {
  activeTab = b.dataset.tab;
  sortState = { col: -1, dir: 1 }; page = 0;
  if (activeTab === 'aggregate') loadAggregate();
  else if (activeTab === 'applicator') loadApplicator();
  else if (activeTab === 'scry') showScry();
  else if (activeTab === 'conjure') showConjure();
  else render();
}));

// Scry / Conjure views: typed modules, each mounted once into its own container. They
// read the typed endpoints directly, independent of the legacy table's data plumbing —
// switching to one hides the table; any other tab (via render) restores it.
let scryMounted = false, conjureMounted = false;
function showPanel(rootId) {
  renderTabs();
  const tw = document.querySelector('.tablewrap'); if (tw) tw.style.display = 'none';
  $('pager').innerHTML = '';
  $('scry-root').hidden = rootId !== 'scry-root';
  $('conjure-root').hidden = rootId !== 'conjure-root';
  return $(rootId);
}
function showScry() {
  const root = showPanel('scry-root');
  if (!scryMounted) { mountScry(root); scryMounted = true; }
}
function showConjure() {
  const root = showPanel('conjure-root');
  if (!conjureMounted) { mountConjure(root, { api, authFetch }); conjureMounted = true; }
}

// Aggregate tab: fetch the persisted listings (all, or just the latest scan when
// "New only" is ticked) and render them through the same table.
async function loadAggregate() {
  try {
    const res = await fetch(api('listings') + '?view=' + (aggNewOnly ? 'new' : 'aggregate'));
    if (!res.ok) throw new Error(await res.text());
    aggregateData = await res.json();
    const n = aggregateData.rows ? aggregateData.rows.length : 0;
    setStatus(n ? `${n} ${aggNewOnly ? 'new' : 'persisted'} listing${n === 1 ? '' : 's'}.` : 'No persisted listings yet — an admin live run fills this.');
  } catch (e) { aggregateData = { columns: [], rows: [] }; setStatus(e.message, true); }
  render();
}
$('agg-new-only')?.addEventListener('change', e => { aggNewOnly = e.target.checked; loadAggregate(); });
$('refresh-btn')?.addEventListener('click', async () => {
  const b = $('refresh-btn'); const label = b.textContent; b.disabled = true; b.textContent = 'Checking…';
  try {
    const res = await authFetch(api('refresh'), { method: 'POST' });
    if (!res) throw new Error('Sign in as an admin to refresh.');
    if (!res.ok) throw new Error(await res.text());
    const { checked, removed } = await res.json();
    setStatus(`Refreshed — checked ${checked}, removed ${removed} no longer available.`);
    await loadAggregate();
  } catch (e) { setStatus(e.message, true); }
  finally { b.disabled = false; b.textContent = label; }
});

// Server-side Saved: for a signed-in identity, pins/applied live in Postgres and
// follow the user across browsers. Guests keep the localStorage sets, unchanged.
async function loadServerSaved() {
  if (current()?.mode !== 'user') return;
  try {
    const res = await authFetch(api('saved'));
    if (!res || !res.ok) return;
    // The server now returns the saved ROWS, not just the flags — so the Saved/
    // Applied tabs reconstruct even on a browser that never ran the search that
    // saved them (and after a Refresh sweep retired the listing). Adopt those rows
    // as the durable snapshot pool; local-only pins (saved off the mock preview,
    // never persisted server-side) keep their existing localStorage snapshot.
    const payload = await res.json();
    const flags = payload.flags || {};
    if (payload.columns && payload.columns.length) savedColumns = payload.columns;
    unavailable = new Set();
    for (const [url, f] of Object.entries(flags)) {
      if (f.pinned) pinned.add(url); if (f.applied) applied.add(url);
      if (f.available === false) unavailable.add(url);
    }
    for (const row of (payload.rows || [])) {
      const key = row.url;
      if (!key) continue;
      const snap = { ...row, __key: key };
      const idx = savedRows.findIndex(x => x.__key === key);
      if (idx >= 0) savedRows[idx] = snap; else savedRows.push(snap);
    }
    savePins(); saveApplied(); saveStore(); renderTabs(); render();
  } catch (e) { /* saved sync is best-effort */ }
}
function pushSaved(key) {
  if (!key || current()?.mode !== 'user') return; // guests persist only in the browser
  authFetch(api('saved'), {
    method: 'PUT', headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ url: key, pinned: pinned.has(key), applied: applied.has(key) }),
  }).catch(() => { /* best-effort */ });
}
$('import-btn').addEventListener('click', () => $('import-file').click());
$('import-file').addEventListener('change', async e => {
  const f = e.target.files[0];
  if (!f) return;
  setStatus('Importing ' + f.name + '…');
  try {
    const res = await fetch(api('import'), { method: 'POST', body: await f.text() });
    if (!res.ok) throw new Error(await res.text());
    setLast(await res.json());
    sortState = { col: -1, dir: 1 }; page = 0;
    render();
    $('status').innerHTML = `Imported <span class="count">${last.rows.length}</span> results from ${esc(f.name)}.`;
    $('status').className = 'status';
  } catch (err) { setStatus(err.message, true); }
  e.target.value = ''; // let the same file be re-imported
});

// The mode pill reflects the DERIVED run mode. Guest → the $0 mock; a signed-in admin
// → the real pipeline (which spends only when the server reports real backends). It is
// a readout of identity, not a control — sign-in happens through the shared account FAB.
function applyMode() {
  const admin = role === 'admin';
  const spends = admin && runCfg.spends;
  const el = $('idmode');
  if (!el) return;
  el.textContent = !admin ? 'mock · $0' : (spends ? 'LIVE · spends' : 'real · $0');
  el.className = 'idmode' + (spends ? ' spend' : '');
  const hint = $('idhint');
  if (hint) hint.textContent = admin ? 'Admin' : 'Guest';
}

// The shared platform account button (sign in / sign out / who am I), identical to the
// home page and quiz. Signing in as an admin is what unlocks the real pipeline.
mountAccountFab();

// Identity drives the mode. On every sign-in / sign-out, re-derive the role and, when an
// admin newly signs in against a real-capable server, refresh the run affordance.
onIdentity(() => {
  role = isAdmin() ? 'admin' : 'guest';
  applyMode();
  loadServerSaved(); // pull this identity's saved pins/applied from the server
});
loadServerSaved(); // and once on load, for an identity already in localStorage
// Field selector — single-select (only Software for now). A field searches ALL its
// roles; the Role column classifies each result into one of them.
let fields = [];              // [{key,label,roles:[{key,label,match}]}]
let selectedField = null;
function currentRoles() { const f = fields.find(x => x.key === selectedField); return f ? f.roles : []; }

function renderFields() {
  const el = $('fields');
  el.innerHTML = fields.map(f =>
    `<button type="button" class="pchip${f.key === selectedField ? ' sel' : ''}" data-key="${esc(f.key)}">${esc(f.label)}</button>`).join('');
  el.querySelectorAll('.pchip').forEach(b => b.addEventListener('click', () => {
    selectedField = b.dataset.key;
    renderFields();
    if (last) render(); // re-classify the Role column for the new field
  }));
}

// classifyRole maps a listing title to one of the field's roles by its match
// substrings — specific roles first, the generic one (roles[0]) last.
function classifyRole(title) {
  const t = (title || '').toLowerCase();
  const roles = currentRoles();
  for (const r of roles.slice(1)) {
    if ((r.match || []).some(m => t.includes(m))) return r.label;
  }
  if (roles[0] && (roles[0].match || []).some(m => t.includes(m))) return roles[0].label;
  return 'Other';
}
// hashColor tints a label its own pastel by a stable FNV-1a hash of its text, so the
// same label is always the same colour and different ones separate well. Shared by
// the Role and Location columns.
function hashColor(label) {
  let h = 2166136261 >>> 0;
  for (let i = 0; i < label.length; i++) { h ^= label.charCodeAt(i); h = Math.imul(h, 16777619) >>> 0; }
  return `hsl(${h % 360},55%,86%)`;
}

// Location select box — the supported, explicitly-mapped places (from /api/config).
// The same catalog drives three things: the chips, the filter (a selected place
// expands to its match substrings), and the display normalization.
let locationsCatalog = [];                                   // [{key,label,match}]
let selectedLocations = new Set(['ma', 'ny', 'ca']);
function renderLocations() {
  const el = $('locsel');
  el.innerHTML = locationsCatalog.map(l =>
    `<button type="button" class="pchip${selectedLocations.has(l.key) ? ' sel' : ''}" data-key="${esc(l.key)}">${esc(l.label)}</button>`).join('');
  el.querySelectorAll('.pchip').forEach(b => b.addEventListener('click', () => {
    const k = b.dataset.key;
    selectedLocations.has(k) ? selectedLocations.delete(k) : selectedLocations.add(k);
    renderLocations();
  }));
}
// expandLocations turns the selected places into the raw match substrings the filter
// keys off — so "New York" filters in "New York, NY", "NYC", "Brooklyn", etc.
function expandLocations() {
  const terms = [];
  for (const l of locationsCatalog) if (selectedLocations.has(l.key)) terms.push(...(l.match || []));
  return terms;
}
// normalizeLocation collapses a raw location to its supported label, or the text
// before the first comma when no supported place matches.
function normalizeLocation(raw) {
  const t = (raw || '').toLowerCase().trim();
  if (!t) return '';
  for (const l of locationsCatalog) if ((l.match || []).some(m => t.includes(m))) return l.label;
  const i = raw.indexOf(',');
  return (i > 0 ? raw.slice(0, i) : raw).trim();
}

fetch(api('config')).then(r => r.json()).then(c => {
  runCfg = c;
  // The run mode follows identity (onIdentity above); this only refreshes the pill once
  // the server has reported whether a real run would spend. A signed-in admin stays admin
  // even if the server can't spend — the server then transparently serves the mock.
  role = isAdmin() ? 'admin' : 'guest';
  fields = c.fields || [];
  selectedField = fields[0] ? fields[0].key : null;
  renderFields();
  locationsCatalog = c.locations || [];
  renderLocations();
  applyMode();
  render(); // restore the persisted table + saved-count now that the role catalog is loaded
}).catch(() => { render(); });

// Fold the sidebar to give the listings the full width. The toggle lives in main, so it is still
// there to un-fold with.
$('toggle').addEventListener('click', () => {
  const collapsed = $('layout').classList.toggle('collapsed');
  $('toggle').setAttribute('aria-expanded', String(!collapsed));
});

// Delayed tooltip: the ⓘ info icon shows the folded apply/source/verified/coverage detail; any other
// CLIPPED cell shows its full value. Delegated on .tablewrap (persists across table re-renders). The
// dwell timer restarts ONLY when the pointer crosses into a DIFFERENT cell, so a micro-movement inside
// one cell doesn't keep resetting it. Dwell is 800ms — 20% faster than the previous 1s.
(function tooltips() {
  const tip = document.createElement('div');
  tip.className = 'tip';
  document.body.appendChild(tip);
  const wrap = document.querySelector('.tablewrap');
  let timer = 0, cur = null;
  const hide = () => { clearTimeout(timer); tip.style.display = 'none'; };
  wrap.addEventListener('mouseover', e => {
    const td = e.target.closest('td');
    if (td === cur) return;
    cur = td;
    hide();
    let text = null;
    if (td && td.classList.contains('info')) text = td.dataset.info || null;           // the info icon
    else if (td && td.scrollWidth > td.clientWidth + 1) text = td.textContent.trim() || null; // clipped cell
    if (!text) return;
    timer = setTimeout(() => {
      tip.textContent = text;
      tip.style.display = 'block';
      const r = td.getBoundingClientRect();
      const x = Math.max(8, Math.min(r.left, window.innerWidth - tip.offsetWidth - 8));
      const below = r.bottom + 6;
      const y = below + tip.offsetHeight > window.innerHeight ? r.top - tip.offsetHeight - 6 : below;
      tip.style.left = x + 'px';
      tip.style.top = Math.max(8, y) + 'px';
    }, 800);
  });
  wrap.addEventListener('mouseleave', () => { cur = null; hide(); });
})();

// ===== Applicator: batch-summarized apply page for saved, not-yet-applied jobs =====
let applicatorData = { jobs: [], summarized: 0 };
let empFilter = '';   // '' | 'salary' | 'nosalary'
let appPollTimer = 0;
const kApp = n => '$' + Math.round(n / 1000) + 'k';
// A posting is "Salary" iff it carries a concrete scraped pay range (not the
// always-present estimate). Everything else is treated as contract/unlisted — this
// hard signal replaces the flaky LLM permanent/contract guess and works retroactively.
const hasSalary = j => !!(j.smin || j.smax);

function showApplicatorLoading(on) {
  $('applicator-loading').hidden = !on;
  document.querySelector('.tablewrap').style.display = on ? 'none' : '';
}
async function launchApplicator() {
  clearTimeout(appPollTimer);
  const btn = $('launch-applicator'); if (btn) btn.disabled = true;
  setBar('applicator', 0, 0);
  showApplicatorLoading(true);
  try {
    const res = role === 'admin'
      ? await authFetch(api('applicator/launch'), { method: 'POST' })
      : await fetch(api('applicator/launch'), { method: 'POST' });
    if (!res) throw new Error('Sign in as an admin to launch the Applicator.');
    if (!res.ok) throw new Error(await res.text());
    pollApplicator((await res.json()).id);
  } catch (e) { setStatus(e.message, true); showApplicatorLoading(false); if (btn) btn.disabled = false; }
}
async function pollApplicator(id) {
  try {
    const res = await fetch(api('applicator/status') + '?id=' + encodeURIComponent(id));
    if (!res.ok) throw new Error(await res.text());
    const j = await res.json();
    setBar('applicator', j.done, j.total);
    if (j.status === 'done') {
      showApplicatorLoading(false);
      const btn = $('launch-applicator'); if (btn) btn.disabled = false;
      activeTab = 'applicator'; renderTabs();
      await loadApplicator();
      setStatus(`Applicator ready — ${applicatorData.jobs.length} jobs to apply to.`);
      return;
    }
    if (j.status === 'error') { setStatus(j.error || 'summarize failed', true); showApplicatorLoading(false); return; }
    appPollTimer = setTimeout(() => pollApplicator(id), 500);
  } catch (e) { setStatus(e.message, true); showApplicatorLoading(false); }
}
async function loadApplicator() {
  try {
    let res = await authFetch(api('applicator'));
    if (!res) res = await fetch(api('applicator'));
    applicatorData = (res && res.ok) ? await res.json() : { jobs: [], summarized: 0 };
  } catch (e) { applicatorData = { jobs: [], summarized: 0 }; }
  renderApplicator();
}
// Comp keys off the posted salary: a concrete range shows as-is; otherwise the role
// is treated as contract/unlisted, so we surface the extracted rate note if Claude
// found one, else the estimate, rather than a misleading annualized figure.
function compHTML(j) {
  if (hasSalary(j)) return `<span class="sal">${j.smin && j.smax ? kApp(j.smin) + '–' + kApp(j.smax) : kApp(j.smin || j.smax)}</span>`;
  if (j.payNote) return `<span class="sal contract">${esc(j.payNote)}</span>`;
  if (j.emin || j.emax) return `<span class="sal est">~${kApp(j.emin || j.emax)}–${kApp(j.emax || j.emin)}</span>`;
  return '<span class="muted">rate n/a</span>';
}
function applyRowHTML(j) {
  const badge = hasSalary(j) ? '<span class="emp-badge perm">Salary</span>' : '<span class="emp-badge contract">No salary</span>';
  const loc = j.lp ? `<span class="loc">${esc(j.lp)}</span>` : '';
  return `<tr>
    <td class="chk"><input type="checkbox" class="app-applied" data-u="${esc(j.u)}" title="Mark applied — syncs to your Applied tab and removes it here" aria-label="mark applied"></td>
    <td class="co">${esc(j.c) || '—'}${loc}</td>
    <td class="ti"><a href="${esc(j.apply)}" target="_blank" rel="noopener">${esc(j.t) || '—'}</a>${badge}</td>
    <td class="sal">${compHTML(j)}</td>
    <td class="txt exp"><span class="exline req"><span class="exlabel">Req</span>${esc(j.required) || '—'}</span><span class="exline pref"><span class="exlabel">Pref</span>${esc(j.preferred) || '—'}</span></td>
    <td class="txt">${esc(j.role) || '—'}</td>
    <td class="txt">${esc(j.does) || '—'}</td>
    <td class="ap"><a class="apply" href="${esc(j.apply)}" target="_blank" rel="noopener">Apply ↗</a></td>
  </tr>`;
}
function renderApplicator() {
  renderTabs();
  const all = applicatorData.jobs || [];
  const jobs = all.filter(j => !empFilter || (empFilter === 'salary' ? hasSalary(j) : !hasSalary(j)));
  document.querySelectorAll('#applicator-controls .fchip').forEach(c => c.classList.toggle('sel', c.dataset.emp === empFilter));
  if (!jobs.length) {
    const msg = all.length ? 'No jobs match the pay filter.' : 'No saved jobs to apply to yet — star some jobs, then hit ⚡ Launch Applicator.';
    $('table').outerHTML = '<div class="empty" id="table">' + msg + '</div>';
    $('pager').innerHTML = '';
    return;
  }
  const head = '<th class="chk" title="Mark applied">✓</th><th>Company</th><th>Title</th><th>Comp</th><th>Experience — required / preferred</th><th>What the role does</th><th>What the company does</th><th>Apply</th>';
  $('table').outerHTML = `<table id="table"><thead><tr>${head}</tr></thead><tbody>${jobs.map(applyRowHTML).join('')}</tbody></table>`;
  document.querySelectorAll('#table .app-applied').forEach(cb => cb.addEventListener('change', () => markApplied(cb)));
  const pending = all.length - (applicatorData.summarized || 0);
  $('pager').innerHTML = `<span>${jobs.length} to apply${pending > 0 ? ` · ${pending} not yet summarized — hit Update` : ''}</span>`;
}
async function markApplied(cb) {
  const u = cb.dataset.u; if (!u) return;
  cb.disabled = true;
  try {
    // Keep it pinned (stays in Saved) and set applied — the server writes both flags.
    const opts = { method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ url: u, pinned: true, applied: true }) };
    const res = role === 'admin' ? await authFetch(api('saved'), opts) : await fetch(api('saved'), opts);
    if (!res || !res.ok) throw new Error('Sign in to sync applied.');
    applied.add(u); saveApplied();
    applicatorData.jobs = applicatorData.jobs.filter(j => j.u !== u);
    renderApplicator();
    setStatus('Marked applied — moved to your Applied tab.');
  } catch (e) { cb.disabled = false; cb.checked = false; setStatus(e.message, true); }
}
$('launch-applicator')?.addEventListener('click', launchApplicator);
$('relaunch-applicator')?.addEventListener('click', launchApplicator);
document.querySelectorAll('#applicator-controls .fchip').forEach(c => c.addEventListener('click', () => {
  empFilter = empFilter === c.dataset.emp ? '' : c.dataset.emp;
  renderApplicator();
}));

wireThousands(); // comma-format the salary/startup numeric inputs
fetch(api('profile')).then(r => r.json()).then(p => {
  populate(p);
  preview();
}).catch(e => setStatus('Could not load profile: ' + e.message, true));
