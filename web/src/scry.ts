// The Scry data-grid — the reimagined Discover surface. It renders the typed
// api/results contract (see model.ts / cmd/gui/results.go) as a dense, sortable,
// per-column-filterable table with New-first grouping and posted-vs-estimated pay
// provenance. Self-contained and typed (not under main.ts's @ts-nocheck); mounted
// into a container by main.ts, so it adds a view without disturbing the legacy page.
import './scry.css';
import type { Result, PayState, ResultsResponse } from './model';
import { confidenceTone } from './model';

const BASE = import.meta.env.BASE_URL;
const api = (p: string) => BASE + 'api/' + p;

// ---- presentation helpers (client-owned; the server sends only domain facts) ----
const esc = (s: unknown) =>
  (s == null ? '' : String(s)).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
const money = (n: number) => '$' + n.toLocaleString();
const kMoney = (n: number) => '$' + Math.round(n / 1000) + 'k';

// A stable pastel per label (FNV-1a), shared by the Role and Location columns so the
// same value is always the same colour and different ones separate well.
function hashColor(label: string): string {
  let h = 2166136261 >>> 0;
  for (let i = 0; i < label.length; i++) {
    h ^= label.charCodeAt(i);
    h = Math.imul(h, 16777619) >>> 0;
  }
  return `hsl(${h % 360},52%,84%)`;
}

// Coarse role classification from the title (the server sends no role field). Specific
// roles win over the generic "Software Engineer".
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

// Posted → days-ago (for recency tint + sort) and a full date (for the hover title).
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
// A posted salary earns the "strong pay" tint; an estimate never does.
const payTint = (r: Result) =>
  r.payState === 'posted' && r.salaryMax >= 200000 ? 'var(--pay-strong)' : r.payState === 'posted' && r.salaryMax >= 150000 ? 'var(--pay-light)' : '';

// ---- column model: sort key + filter kind per header ----
type FilterKind = 'role' | 'location' | 'remote' | 'pay' | 'score' | 'days';
interface Col {
  label: string;
  sort?: (r: Result) => string | number;
  filter?: FilterKind;
  num?: boolean;
}

// ---- grid state ----
interface State {
  rows: Result[];
  sortIdx: number;
  sortDir: 1 | -1;
  roles: Set<string>;
  locs: Set<string>;
  remote: Set<string>; // "yes" | "no"
  payLo: number | null;
  payHi: number | null;
  scoreMin: number | null;
  daysMax: number | null;
}

export function mountScry(root: HTMLElement): void {
  const st: State = {
    rows: [],
    sortIdx: -1,
    sortDir: -1,
    roles: new Set(),
    locs: new Set(),
    remote: new Set(),
    payLo: null,
    payHi: null,
    scoreMin: null,
    daysMax: null,
  };

  const COLS: Col[] = [
    { label: 'Title', sort: (r) => r.title, filter: 'role' },
    { label: 'Role', sort: (r) => classifyRole(r.title), filter: 'role' },
    { label: 'Company', sort: (r) => r.company },
    { label: 'Loc', sort: (r) => r.location, filter: 'location' },
    { label: 'Rem', sort: (r) => (r.remote ? 1 : 0), filter: 'remote' },
    { label: 'Pay min', sort: (r) => r.salaryMin || r.salaryEstMin, filter: 'pay', num: true },
    { label: 'Pay max', sort: (r) => r.salaryMax || r.salaryEstMax, filter: 'pay', num: true },
    { label: 'Posted', sort: (r) => daysAgo(r.posted) ?? 1e9, filter: 'days', num: true },
    { label: 'Score', sort: (r) => r.score, filter: 'score', num: true },
  ];

  const anyFilter = () =>
    st.roles.size || st.locs.size || st.remote.size || st.payLo != null || st.payHi != null || st.scoreMin != null || st.daysMax != null;
  const filterActive = (k: FilterKind) =>
    ({
      role: st.roles.size > 0,
      location: st.locs.size > 0,
      remote: st.remote.size > 0,
      pay: st.payLo != null || st.payHi != null,
      score: st.scoreMin != null,
      days: st.daysMax != null,
    }[k]);

  // filtered + sorted (NOT yet New-partitioned — render() groups)
  function view(): Result[] {
    let rows = st.rows.slice();
    if (st.roles.size) rows = rows.filter((r) => st.roles.has(classifyRole(r.title)));
    if (st.locs.size) rows = rows.filter((r) => st.locs.has(r.location));
    if (st.remote.size) rows = rows.filter((r) => st.remote.has(r.remote ? 'yes' : 'no'));
    if (st.payLo != null) rows = rows.filter((r) => (r.salaryMax || r.salaryEstMax) >= st.payLo!);
    if (st.payHi != null) rows = rows.filter((r) => (r.salaryMin || r.salaryEstMin) <= st.payHi!);
    if (st.scoreMin != null) rows = rows.filter((r) => r.score >= st.scoreMin!);
    if (st.daysMax != null) rows = rows.filter((r) => (daysAgo(r.posted) ?? 1e9) <= st.daysMax!);
    const col = COLS[st.sortIdx];
    if (col && col.sort) {
      const key = col.sort;
      rows.sort((a, b) => {
        const x = key(a);
        const y = key(b);
        if (typeof x === 'string') return (x as string).localeCompare(y as string) * st.sortDir;
        return (((x as number) - (y as number)) || 0) * st.sortDir;
      });
    }
    return rows;
  }

  function payCells(r: Result): string {
    const est = r.payState === 'estimated';
    const lo = r.payState === 'posted' ? r.salaryMin : r.salaryEstMin;
    const hi = r.payState === 'posted' ? r.salaryMax : r.salaryEstMax;
    if (r.payState === 'none') return `<td class="num muted">—</td><td class="num muted">—</td>`;
    const tint = payTint(r);
    if (est) {
      const cell = (v: number) => `<td class="num estcell" title="Estimated — no pay stated in the posting">~${money(v)}</td>`;
      return cell(lo) + cell(hi);
    }
    const cell = (v: number) =>
      `<td class="num"${tint ? ` style="background:${tint}"` : ''}><span class="${tint ? 'tint' : ''}">${money(v)}</span></td>`;
    return cell(lo) + cell(hi);
  }

  function rowHTML(r: Result): string {
    const role = classifyRole(r.title);
    const d = daysAgo(r.posted);
    const [tone] = [confidenceTone(r.confidence)];
    const title = r.applyUrl || r.url
      ? `<a href="${esc(r.applyUrl || r.url)}" target="_blank" rel="noopener">${esc(r.title)}</a>`
      : esc(r.title);
    return `<tr class="${r.new ? 'newrow' : ''}">
      <td class="title">${r.new ? '<span class="nwtag" title="New since last scan">✨</span> ' : ''}${title}</td>
      <td><span class="pill" style="background:${hashColor(role)}">${esc(role)}</span></td>
      <td>${esc(r.company)}</td>
      <td><span class="pill" style="background:${hashColor(r.location || '—')}">${esc(r.location || '—')}</span></td>
      <td class="remote ${r.remote ? 'yes' : 'no'}">${r.remote ? '✓' : '✗'}</td>
      ${payCells(r)}
      <td class="num posted ${d != null ? 'tint' : ''}"${d != null ? ` style="background:${recTint(d)}"` : ''} title="${r.posted ? 'Posted ' + postedDate(r.posted) : 'Posted date unknown'}">${d != null ? d + 'd' : '—'}</td>
      <td class="num"><span class="score">${r.score.toFixed(2)}<span class="cdot ${tone}" title="${esc(r.confidence)}"></span></span></td>
    </tr>`;
  }

  function headHTML(): string {
    return COLS.map((c, i) => {
      const on = i === st.sortIdx;
      const arrows = `<span class="sar" data-sort="${i}"><i class="up${on && st.sortDir === 1 ? ' act' : ''}">▲</i><i class="dn${on && st.sortDir === -1 ? ' act' : ''}">▼</i></span>`;
      const funnel = c.filter ? `<button class="thf${filterActive(c.filter) ? ' act' : ''}" data-filter="${c.filter}" title="Filter">▾</button>` : '';
      return `<th class="th ${c.num ? 'num' : ''}"><span class="th-in"><span class="thl" data-sort="${i}">${esc(c.label)}</span>${arrows}${funnel}</span></th>`;
    }).join('');
  }

  function render(): void {
    const rows = view();
    const fresh = rows.filter((r) => r.new);
    const older = rows.filter((r) => !r.new);
    const grp = (label: string, n: number, cls: string) =>
      `<tr class="grouprow ${cls}"><td colspan="${COLS.length}">${label} <span class="gcnt">${n}</span></td></tr>`;
    let body: string;
    if (!rows.length) {
      body = `<tr class="emptyrow"><td colspan="${COLS.length}">${anyFilter() ? 'No jobs match these filters.' : 'No verified jobs yet — run a scan to fill this.'}</td></tr>`;
    } else {
      body = '';
      if (fresh.length) body += grp('✨ New since last scan', fresh.length, 'new') + fresh.map(rowHTML).join('');
      if (older.length) body += (fresh.length ? grp('Earlier', older.length, 'old') : '') + older.map(rowHTML).join('');
    }
    root.innerHTML = `
      <div class="scry-ctx"><b>${rows.length}</b> verified job${rows.length === 1 ? '' : 's'}${
        fresh.length ? ` · <span class="newpill">✨ ${fresh.length} new</span>` : ''
      }${anyFilter() ? ' · <button class="clearf" id="scry-clear">Clear filters</button>' : ''}</div>
      <div class="scry-wrap"><table class="scry"><thead><tr>${headHTML()}</tr></thead><tbody>${body}</tbody></table></div>`;

    root.querySelectorAll<HTMLElement>('[data-sort]').forEach((el) =>
      el.addEventListener('click', () => {
        const i = Number(el.dataset.sort);
        if (st.sortIdx === i) st.sortDir = (st.sortDir * -1) as 1 | -1;
        else {
          st.sortIdx = i;
          st.sortDir = 1;
        }
        render();
      })
    );
    root.querySelectorAll<HTMLElement>('.thf').forEach((el) =>
      el.addEventListener('click', (e) => {
        e.stopPropagation();
        openFilter(el.dataset.filter as FilterKind, el);
      })
    );
    const clear = root.querySelector('#scry-clear');
    if (clear)
      clear.addEventListener('click', () => {
        st.roles.clear();
        st.locs.clear();
        st.remote.clear();
        st.payLo = st.payHi = st.scoreMin = st.daysMax = null;
        render();
      });
  }

  // ---- per-column filter dropdown ----
  let pop: HTMLElement | null = null;
  function closePop() {
    if (pop) {
      pop.remove();
      pop = null;
    }
  }
  document.addEventListener('click', (e) => {
    if (pop && !(e.target as HTMLElement).closest('.scry-colpop') && !(e.target as HTMLElement).closest('.thf')) closePop();
  });

  function uniq(pick: (r: Result) => string): string[] {
    return [...new Set(st.rows.map(pick))].filter(Boolean).sort();
  }

  function openFilter(kind: FilterKind, anchor: HTMLElement): void {
    closePop();
    const cp = document.createElement('div');
    cp.className = 'scry-colpop';
    let inner = '';
    const checks = (title: string, opts: Array<[string, string]>, has: (v: string) => boolean) =>
      `<div class="ct">${title}</div>` +
      opts.map(([v, lab]) => `<label><input type="checkbox" data-v="${esc(v)}" ${has(v) ? 'checked' : ''}>${esc(lab)}</label>`).join('');
    if (kind === 'role') inner = checks('Filter by role', uniq((r) => classifyRole(r.title)).map((v) => [v, v]), (v) => st.roles.has(v));
    else if (kind === 'location') inner = checks('Filter by location', uniq((r) => r.location).map((v) => [v, v]), (v) => st.locs.has(v));
    else if (kind === 'remote') inner = checks('Remote', [['yes', 'Remote OK'], ['no', 'On-site']], (v) => st.remote.has(v));
    else if (kind === 'pay')
      inner = `<div class="ct">Pay range ($)</div><div class="rng"><input id="f-lo" placeholder="min" value="${st.payLo ?? ''}"><span>–</span><input id="f-hi" placeholder="max" value="${st.payHi ?? ''}"></div>`;
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
      else if (kind === 'pay') {
        st.payLo = num('f-lo');
        st.payHi = num('f-hi');
      } else if (kind === 'score') st.scoreMin = num('f-score');
      else if (kind === 'days') st.daysMax = num('f-days');
      closePop();
      render();
    });
    cp.querySelector('[data-act=clear]')!.addEventListener('click', () => {
      if (kind === 'role') st.roles.clear();
      else if (kind === 'location') st.locs.clear();
      else if (kind === 'remote') st.remote.clear();
      else if (kind === 'pay') st.payLo = st.payHi = null;
      else if (kind === 'score') st.scoreMin = null;
      else if (kind === 'days') st.daysMax = null;
      closePop();
      render();
    });
  }

  // ---- load ----
  root.innerHTML = `<div class="scry-ctx">Loading verified jobs…</div>`;
  fetch(api('results'))
    .then((r) => {
      if (!r.ok) throw new Error('results ' + r.status);
      return r.json() as Promise<ResultsResponse>;
    })
    .then((data) => {
      st.rows = data.results || [];
      st.sortIdx = COLS.length - 1; // Score, descending — best-first by default
      st.sortDir = -1;
      render();
    })
    .catch((e) => {
      root.innerHTML = `<div class="scry-ctx err">Couldn't load jobs: ${esc(e.message)}</div>`;
    });
}
