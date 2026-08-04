// The Scry data-grid — the reimagined Discover surface. Renders the typed api/results
// contract as a dense, sortable, per-column-filterable table with New-first grouping,
// company-tier colour, posted-vs-estimated pay provenance, and a Consecrate (★) action
// that saves a job into the Conjure funnel. Self-contained and typed (not under
// main.ts's @ts-nocheck); platform auth (authFetch) is injected so it type-checks
// without the untyped @platform/ui surface.
import './scry.css';
import type { Result, ResultsResponse } from './model';
import { confidenceTone } from './model';

const BASE = import.meta.env.BASE_URL;
const api = (p: string) => BASE + 'api/' + p;

interface ScryDeps {
  authFetch: (url: string, init?: RequestInit) => Promise<Response | null>;
}

const esc = (s: unknown) =>
  (s == null ? '' : String(s)).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
const money = (n: number) => '$' + n.toLocaleString();

function hashColor(label: string): string {
  let h = 2166136261 >>> 0;
  for (let i = 0; i < label.length; i++) {
    h ^= label.charCodeAt(i);
    h = Math.imul(h, 16777619) >>> 0;
  }
  return `hsl(${h % 360},52%,84%)`;
}

// Company-tier fills — the same encoding the legacy table used, now driven by the
// server-classified companyTier field.
const TIER_FILL: Record<string, string> = { f500: '#FFE699', software: '#9BC2E6', startup: '#D9C2E9' };
const TIER_LABEL: Record<string, string> = { f500: 'Fortune 500', software: 'Software', startup: 'Startup' };

const ROLE_RULES: Array<[string, string[]]> = [
  ['Backend', ['backend', 'back-end', 'back end']],
  ['Frontend', ['frontend', 'front-end', 'front end']],
  ['Full-stack', ['full stack', 'full-stack', 'fullstack']],
  ['Platform', ['platform', 'infrastructure', 'infra']],
  ['DevOps / SRE', ['devops', 'site reliability', 'sre']],
  ['Data', ['data engineer', 'data scientist', 'machine learning', 'ml engineer', 'ai engineer']],
  ['Security', ['security']],
  ['Mobile', ['ios', 'android', 'mobile']],
];
function classifyRole(title: string): string {
  const t = (title || '').toLowerCase();
  for (const [label, subs] of ROLE_RULES) if (subs.some((s) => t.includes(s))) return label;
  return /engineer|developer|swe|software/.test(t) ? 'Software Engineer' : 'Other';
}

function daysAgo(iso?: string): number | null {
  if (!iso) return null;
  const then = Date.parse(iso);
  if (isNaN(then)) return null;
  return Math.max(0, Math.floor((Date.now() - then) / 86_400_000));
}
function postedDate(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? '' : d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
}
const recTint = (d: number | null) =>
  d == null ? '' : d <= 7 ? 'var(--fresh)' : d <= 21 ? 'var(--recent)' : d <= 45 ? 'var(--aging)' : 'var(--stale)';
// Posted salary earns the green "strong pay" tint; estimates are rendered in blue
// (see payCells) so they're never mistaken for the employer's number.
const payTintPosted = (max: number) => (max >= 200000 ? 'var(--pay-strong)' : max >= 150000 ? 'var(--pay-light)' : '');

type FilterKind = 'role' | 'location' | 'remote' | 'pay' | 'score' | 'days';
interface Col {
  label: string;
  sort: (r: Result) => string | number;
  filter?: FilterKind;
  num?: boolean;
}

interface State {
  rows: Result[];
  pinned: Set<string>; // consecrated URLs
  sortIdx: number;
  sortDir: 1 | -1;
  page: number;
  pay: 'all' | 'has' | 'none'; // salary presence (lives in the Filters menu)
  newOnly: boolean; // show only latest-scan jobs (Filters menu)
  roles: Set<string>;
  locs: Set<string>;
  remote: Set<string>;
  payLo: number | null;
  payHi: number | null;
  scoreMin: number | null;
  daysMax: number | null;
}

const PAGE_SIZE = 100;

export function mountScry(root: HTMLElement, deps: ScryDeps): void {
  const st: State = {
    rows: [], pinned: new Set(), sortIdx: -1, sortDir: -1, page: 0, pay: 'all', newOnly: false,
    roles: new Set(), locs: new Set(), remote: new Set(), payLo: null, payHi: null, scoreMin: null, daysMax: null,
  };

  const COLS: Col[] = [
    { label: 'Title', sort: (r) => r.title, filter: 'role' },
    { label: 'Role', sort: (r) => classifyRole(r.title), filter: 'role' },
    { label: 'Company', sort: (r) => r.company, filter: undefined },
    { label: 'Loc', sort: (r) => r.location, filter: 'location' },
    { label: 'Rem', sort: (r) => (r.remote ? 1 : 0), filter: 'remote' },
    { label: 'Pay min', sort: (r) => r.salaryMin || r.salaryEstMin, filter: 'pay', num: true },
    { label: 'Pay max', sort: (r) => r.salaryMax || r.salaryEstMax, filter: 'pay', num: true },
    { label: 'Posted', sort: (r) => daysAgo(r.posted) ?? 1e9, filter: 'days', num: true },
    { label: 'Score', sort: (r) => r.score, filter: 'score', num: true },
  ];
  const SPAN = COLS.length + 1; // + the ★ column

  const anyFilter = () =>
    st.pay !== 'all' || st.newOnly || st.roles.size || st.locs.size || st.remote.size || st.payLo != null || st.payHi != null || st.scoreMin != null || st.daysMax != null;
  // count of active filter GROUPS, for the Filters button badge
  const filterCount = () =>
    (st.pay !== 'all' ? 1 : 0) + (st.newOnly ? 1 : 0) + (st.roles.size ? 1 : 0) + (st.locs.size ? 1 : 0) + (st.remote.size ? 1 : 0) + (st.payLo != null || st.payHi != null ? 1 : 0) + (st.scoreMin != null ? 1 : 0) + (st.daysMax != null ? 1 : 0);
  const filterActive = (k: FilterKind) =>
    ({ role: st.roles.size > 0, location: st.locs.size > 0, remote: st.remote.size > 0, pay: st.payLo != null || st.payHi != null, score: st.scoreMin != null, days: st.daysMax != null }[k]);

  function view(): Result[] {
    let rows = st.rows.slice();
    if (st.newOnly) rows = rows.filter((r) => r.new);
    if (st.pay === 'has') rows = rows.filter((r) => r.payState !== 'none');
    else if (st.pay === 'none') rows = rows.filter((r) => r.payState === 'none');
    if (st.roles.size) rows = rows.filter((r) => st.roles.has(classifyRole(r.title)));
    if (st.locs.size) rows = rows.filter((r) => st.locs.has(r.location));
    if (st.remote.size) rows = rows.filter((r) => st.remote.has(r.remote ? 'yes' : 'no'));
    if (st.payLo != null) rows = rows.filter((r) => (r.salaryMax || r.salaryEstMax) >= st.payLo!);
    if (st.payHi != null) rows = rows.filter((r) => (r.salaryMin || r.salaryEstMin) <= st.payHi!);
    if (st.scoreMin != null) rows = rows.filter((r) => r.score >= st.scoreMin!);
    if (st.daysMax != null) rows = rows.filter((r) => (daysAgo(r.posted) ?? 1e9) <= st.daysMax!);
    const col = COLS[st.sortIdx];
    if (col) {
      const key = col.sort;
      rows.sort((a, b) => {
        const x = key(a), y = key(b);
        if (typeof x === 'string') return (x as string).localeCompare(y as string) * st.sortDir;
        return (((x as number) - (y as number)) || 0) * st.sortDir;
      });
    }
    return rows;
  }

  function payCells(r: Result): string {
    if (r.payState === 'none') return `<td class="num muted">—</td><td class="num muted">—</td>`;
    if (r.payState === 'estimated') {
      // Estimates in distinct shades of blue, with ~ — never the posted-pay green.
      const cell = (v: number, shade: string) => `<td class="num estcell" style="background:${shade}" title="Estimated — no pay stated in the posting"><span class="tint">~${money(v)}</span></td>`;
      return cell(r.salaryEstMin, 'var(--est-light)') + cell(r.salaryEstMax, 'var(--est-strong)');
    }
    const tint = payTintPosted(r.salaryMax);
    const cell = (v: number) => `<td class="num"${tint ? ` style="background:${tint}"` : ''}><span class="${tint ? 'tint' : ''}">${money(v)}</span></td>`;
    return cell(r.salaryMin) + cell(r.salaryMax);
  }

  function rowHTML(r: Result): string {
    const role = classifyRole(r.title);
    const d = daysAgo(r.posted);
    const tone = confidenceTone(r.confidence);
    const on = st.pinned.has(r.url);
    const title = r.applyUrl || r.url
      ? `<a href="${esc(r.applyUrl || r.url)}" target="_blank" rel="noopener">${esc(r.title)}</a>`
      : esc(r.title);
    const tier = r.companyTier ? ` style="background:${TIER_FILL[r.companyTier]}" title="${TIER_LABEL[r.companyTier]}"` : '';
    const companyCell = r.companyTier ? `<td class="tint"${tier}>${esc(r.company)}</td>` : `<td>${esc(r.company)}</td>`;
    return `<tr class="${r.new ? 'newrow' : ''}">
      <td class="starcell"><button class="starbtn${on ? ' on' : ''}" data-u="${esc(r.url)}" title="${on ? 'Consecrated — click to remove' : 'Consecrate (save to your shortlist)'}" aria-label="consecrate">${on ? '★' : '☆'}</button></td>
      <td class="title">${r.new ? '<span class="nwtag" title="New since last scan">✨</span> ' : ''}${title}</td>
      <td><span class="pill" style="background:${hashColor(role)}">${esc(role)}</span></td>
      ${companyCell}
      <td><span class="pill" style="background:${hashColor(r.location || '—')}">${esc(r.location || '—')}</span></td>
      <td class="remote ${r.remote ? 'yes' : 'no'}">${r.remote ? '✓' : '✗'}</td>
      ${payCells(r)}
      <td class="num posted ${d != null ? 'tint' : ''}"${d != null ? ` style="background:${recTint(d)}"` : ''} title="${r.posted ? 'Posted ' + esc(postedDate(r.posted)) : 'Posted date not given'}">${d != null ? d + 'd' : '—'}</td>
      <td class="num"><span class="score">${r.score.toFixed(2)}<span class="cdot ${tone}" title="${esc(r.confidence)}"></span></span></td>
    </tr>`;
  }

  function headHTML(): string {
    const dataCols = COLS.map((c, i) => {
      const on = i === st.sortIdx;
      const arrows = `<span class="sar" data-sort="${i}"><i class="up${on && st.sortDir === 1 ? ' act' : ''}">▲</i><i class="dn${on && st.sortDir === -1 ? ' act' : ''}">▼</i></span>`;
      const funnel = c.filter ? `<button class="thf${filterActive(c.filter) ? ' act' : ''}" data-filter="${c.filter}" title="Filter">▾</button>` : '';
      return `<th class="th ${c.num ? 'num' : ''}"><span class="th-in"><span class="thl" data-sort="${i}">${esc(c.label)}</span>${arrows}${funnel}</span></th>`;
    }).join('');
    return `<th class="th starh" title="Consecrate">★</th>` + dataCols;
  }

  // reset to the first page and re-render — for any change that alters the row SET.
  const refilter = () => { st.page = 0; render(); };

  function render(): void {
    const rows = view();
    const fresh = rows.filter((r) => r.new);
    const older = rows.filter((r) => !r.new);
    const combined = fresh.concat(older); // New-first, then Earlier
    const total = combined.length;
    const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));
    st.page = Math.min(Math.max(0, st.page), pages - 1);
    const start = st.page * PAGE_SIZE;
    const pageRows = combined.slice(start, start + PAGE_SIZE);
    const pf = pageRows.filter((r) => r.new);
    const po = pageRows.filter((r) => !r.new);

    const grp = (label: string, n: number, cls: string) => `<tr class="grouprow ${cls}"><td colspan="${SPAN}">${label} <span class="gcnt">${n}</span></td></tr>`;
    let body: string;
    if (!total) body = `<tr class="emptyrow"><td colspan="${SPAN}">${anyFilter() ? 'No jobs match these filters.' : 'No verified jobs yet — run a scan to fill this.'}</td></tr>`;
    else {
      body = '';
      if (pf.length) body += grp('✨ New since last scan', fresh.length, 'new') + pf.map(rowHTML).join('');
      if (po.length) body += (pf.length ? grp('Earlier', older.length, 'old') : '') + po.map(rowHTML).join('');
    }
    const fc = filterCount();
    const pager = pages > 1
      ? `<div class="scry-pager"><button ${st.page === 0 ? 'disabled' : ''} data-pg="-1">‹ Prev</button><span>${start + 1}–${start + pageRows.length} of ${total} · page ${st.page + 1} of ${pages}</span><button ${st.page >= pages - 1 ? 'disabled' : ''} data-pg="1">Next ›</button></div>`
      : `<div class="scry-pager"><span>${total} listing${total === 1 ? '' : 's'}</span></div>`;

    root.innerHTML = `
      <div class="scry-ctx"><b>${total}</b> verified job${total === 1 ? '' : 's'}${fresh.length ? ` · <span class="newpill">✨ ${fresh.length} new</span>` : ''}
        <button class="filt-open" id="scry-filters">⏷ Filters${fc ? ` <span class="filt-badge">${fc}</span>` : ''}</button>
        ${anyFilter() ? '<button class="clearf" id="scry-clear">Clear all</button>' : ''}</div>
      <div class="scry-wrap"><table class="scry"><thead><tr>${headHTML()}</tr></thead><tbody>${body}</tbody></table></div>
      ${pager}`;

    root.querySelectorAll<HTMLElement>('[data-sort]').forEach((el) =>
      el.addEventListener('click', () => {
        const i = Number(el.dataset.sort);
        if (st.sortIdx === i) st.sortDir = (st.sortDir * -1) as 1 | -1;
        else { st.sortIdx = i; st.sortDir = 1; }
        refilter();
      })
    );
    root.querySelectorAll<HTMLElement>('.thf').forEach((el) =>
      el.addEventListener('click', (e) => { e.stopPropagation(); openFilter(el.dataset.filter as FilterKind, el); })
    );
    root.querySelectorAll<HTMLButtonElement>('.starbtn').forEach((b) =>
      b.addEventListener('click', () => consecrate(b))
    );
    root.querySelectorAll<HTMLElement>('[data-pg]').forEach((b) =>
      b.addEventListener('click', () => { st.page += Number(b.dataset.pg); render(); root.querySelector('.scry-wrap')?.scrollTo(0, 0); })
    );
    const fo = root.querySelector('#scry-filters');
    if (fo) fo.addEventListener('click', (e) => { e.stopPropagation(); openFiltersMenu(fo as HTMLElement); });
    const clear = root.querySelector('#scry-clear');
    if (clear) clear.addEventListener('click', () => {
      st.pay = 'all'; st.newOnly = false; st.roles.clear(); st.locs.clear(); st.remote.clear();
      st.payLo = st.payHi = st.scoreMin = st.daysMax = null; refilter();
    });
  }

  // The broader Filters menu — salary presence and New-only, consolidated in one place.
  function openFiltersMenu(anchor: HTMLElement): void {
    closePop();
    const cp = document.createElement('div');
    cp.className = 'scry-colpop filt-menu';
    const sal = (['all', 'has', 'none'] as const).map((k) => `<label><input type="radio" name="fm-sal" data-sal="${k}" ${st.pay === k ? 'checked' : ''}>${k === 'all' ? 'All jobs' : k === 'has' ? 'Has a salary' : 'No salary listed'}</label>`).join('');
    cp.innerHTML = `<div class="ct">Salary</div>${sal}<div class="ct" style="margin-top:9px">Recency</div><label><input type="checkbox" id="fm-new" ${st.newOnly ? 'checked' : ''}> ✨ New</label><div class="row2"><button data-act="clear">Clear</button><button class="app" data-act="apply">Apply</button></div>`;
    document.body.appendChild(cp);
    pop = cp;
    const rect = anchor.getBoundingClientRect();
    cp.style.top = rect.bottom + 6 + 'px';
    cp.style.left = Math.max(8, Math.min(rect.left, window.innerWidth - cp.offsetWidth - 10)) + 'px';
    cp.querySelector('[data-act=apply]')!.addEventListener('click', () => {
      const picked = cp.querySelector<HTMLInputElement>('input[name=fm-sal]:checked');
      st.pay = (picked?.dataset.sal as State['pay']) || 'all';
      st.newOnly = cp.querySelector<HTMLInputElement>('#fm-new')!.checked;
      closePop(); refilter();
    });
    cp.querySelector('[data-act=clear]')!.addEventListener('click', () => { st.pay = 'all'; st.newOnly = false; closePop(); refilter(); });
  }

  // Consecrate = save into the shortlist (Conjure's Consecrated stage), via the saved
  // endpoint's pinned flag. Requires an identity; a guest is nudged to sign in.
  function consecrate(btn: HTMLButtonElement): void {
    const u = btn.dataset.u!;
    const willPin = !st.pinned.has(u);
    deps
      .authFetch(api('saved'), {
        method: 'PUT', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ url: u, pinned: willPin, applied: false }),
      })
      .then((res) => {
        if (!res) { flash('Sign in to consecrate jobs'); return; }
        if (willPin) st.pinned.add(u); else st.pinned.delete(u);
        btn.classList.toggle('on', willPin);
        btn.textContent = willPin ? '★' : '☆';
        flash(willPin ? '★ Consecrated — added to your shortlist' : 'Removed from your shortlist');
      })
      .catch(() => flash('Could not save'));
  }

  let pop: HTMLElement | null = null;
  const closePop = () => { if (pop) { pop.remove(); pop = null; } };
  document.addEventListener('click', (e) => {
    if (pop && !(e.target as HTMLElement).closest('.scry-colpop') && !(e.target as HTMLElement).closest('.thf')) closePop();
  });
  const uniq = (pick: (r: Result) => string) => [...new Set(st.rows.map(pick))].filter(Boolean).sort();

  function openFilter(kind: FilterKind, anchor: HTMLElement): void {
    closePop();
    const cp = document.createElement('div');
    cp.className = 'scry-colpop';
    const checks = (title: string, opts: Array<[string, string]>, has: (v: string) => boolean) =>
      `<div class="ct">${title}</div>` + opts.map(([v, lab]) => `<label><input type="checkbox" data-v="${esc(v)}" ${has(v) ? 'checked' : ''}>${esc(lab)}</label>`).join('');
    let inner = '';
    if (kind === 'role') inner = checks('Filter by role', uniq((r) => classifyRole(r.title)).map((v) => [v, v]), (v) => st.roles.has(v));
    else if (kind === 'location') inner = checks('Filter by location', uniq((r) => r.location).map((v) => [v, v]), (v) => st.locs.has(v));
    else if (kind === 'remote') inner = checks('Remote', [['yes', 'Remote OK'], ['no', 'On-site']], (v) => st.remote.has(v));
    else if (kind === 'pay') inner = `<div class="ct">Pay range ($)</div><div class="rng"><input id="f-lo" placeholder="min" value="${st.payLo ?? ''}"><span>–</span><input id="f-hi" placeholder="max" value="${st.payHi ?? ''}"></div>`;
    else if (kind === 'score') inner = `<div class="ct">Min score (0–1)</div><div class="rng"><input id="f-score" placeholder="0.0" value="${st.scoreMin ?? ''}"></div>`;
    else if (kind === 'days') inner = `<div class="ct">Posted within</div><div class="rng"><input id="f-days" placeholder="days" value="${st.daysMax ?? ''}"><span>days</span></div>`;
    cp.innerHTML = inner + `<div class="row2"><button data-act="clear">Clear</button><button class="app" data-act="apply">Apply</button></div>`;
    document.body.appendChild(cp);
    pop = cp;
    const rect = anchor.getBoundingClientRect();
    cp.style.top = rect.bottom + 6 + 'px';
    cp.style.left = Math.max(8, Math.min(rect.left, window.innerWidth - cp.offsetWidth - 10)) + 'px';
    const num = (id: string) => {
      const raw = (cp.querySelector<HTMLInputElement>('#' + id)?.value || '').replace(/[^0-9.]/g, '');
      return raw === '' ? null : parseFloat(raw);
    };
    cp.querySelector('[data-act=apply]')!.addEventListener('click', () => {
      const checked = [...cp.querySelectorAll<HTMLInputElement>('input[data-v]:checked')].map((i) => i.dataset.v!);
      if (kind === 'role') st.roles = new Set(checked);
      else if (kind === 'location') st.locs = new Set(checked);
      else if (kind === 'remote') st.remote = new Set(checked);
      else if (kind === 'pay') { st.payLo = num('f-lo'); st.payHi = num('f-hi'); }
      else if (kind === 'score') st.scoreMin = num('f-score');
      else if (kind === 'days') st.daysMax = num('f-days');
      closePop(); refilter();
    });
    cp.querySelector('[data-act=clear]')!.addEventListener('click', () => {
      if (kind === 'role') st.roles.clear();
      else if (kind === 'location') st.locs.clear();
      else if (kind === 'remote') st.remote.clear();
      else if (kind === 'pay') st.payLo = st.payHi = null;
      else if (kind === 'score') st.scoreMin = null;
      else if (kind === 'days') st.daysMax = null;
      closePop(); refilter();
    });
  }

  function flash(msg: string): void {
    let t = document.getElementById('scry-toast');
    if (!t) { t = document.createElement('div'); t.id = 'scry-toast'; t.className = 'conjure-toast'; document.body.appendChild(t); }
    t.textContent = msg; t.classList.add('show'); setTimeout(() => t!.classList.remove('show'), 2200);
  }

  // Load the verified set and the user's already-consecrated URLs (best-effort).
  root.innerHTML = `<div class="scry-ctx">Loading verified jobs…</div>`;
  Promise.all([
    fetch(api('results')).then((r) => (r.ok ? (r.json() as Promise<ResultsResponse>) : Promise.reject(new Error('results ' + r.status)))),
    deps.authFetch(api('saved')).then((r) => (r && r.ok ? r.json() : null)).catch(() => null),
  ])
    .then(([data, saved]) => {
      st.rows = data.results || [];
      const flags = saved && (saved as { flags?: Record<string, { pinned?: boolean }> }).flags;
      if (flags) for (const [u, f] of Object.entries(flags)) if (f.pinned) st.pinned.add(u);
      st.sortIdx = COLS.length - 1; // Score, descending
      st.sortDir = -1;
      render();
    })
    .catch((e: Error) => {
      root.innerHTML = `<div class="scry-ctx err">Couldn't load jobs: ${esc(e.message)}</div>`;
    });
}
