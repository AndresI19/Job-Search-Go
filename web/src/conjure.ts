// The Conjure card board — the reimagined Apply surface. Where Scry is a dense grid
// for scanning hundreds of jobs, Conjure is a reading surface for the handful you've
// committed to: each saved job is a card showing Claude's summary (what it REQUIRES vs
// PREFERS, what the role & company do) and honest pay provenance. Reads the typed
// api/applicator contract (saved-not-applied jobs joined with cached summaries).
//
// Self-contained and typed (not under main.ts's @ts-nocheck). Platform auth is injected
// (authFetch/api) rather than imported, so this module type-checks without the untyped
// @platform/ui surface.
import './conjure.css';

// The api/applicator row (mirror of cmd/gui applyRow). A job is Discerned when it
// carries a summary; Consecrated (saved, not yet summarized) when the summary is blank.
export interface ApplyJob {
  u: string; // canonical URL (the manifested-sync key)
  apply: string; // where to open to apply
  c: string; // company
  t: string; // title
  lp: string; // location
  r: boolean; // remote
  smin: number;
  smax: number;
  emin: number;
  emax: number;
  required: string;
  preferred: string;
  role: string;
  does: string; // what the company does
  payNote: string;
  employment: string;
  contract: boolean;
  a: boolean; // available
}

interface ConjureDeps {
  api: (p: string) => string;
  authFetch: (url: string, init?: RequestInit) => Promise<Response | null>;
}

type PayState = 'posted' | 'estimated' | 'none';
const payState = (j: ApplyJob): PayState =>
  j.smin > 0 || j.smax > 0 ? 'posted' : j.emin > 0 || j.emax > 0 ? 'estimated' : 'none';
const isDiscerned = (j: ApplyJob) => j.required.trim() !== '' || j.role.trim() !== '';

const esc = (s: unknown) =>
  (s == null ? '' : String(s)).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
const kMoney = (n: number) => '$' + Math.round(n / 1000) + 'k';

function toast(msg: string): void {
  let t = document.getElementById('conjure-toast');
  if (!t) {
    t = document.createElement('div');
    t.id = 'conjure-toast';
    t.className = 'conjure-toast';
    document.body.appendChild(t);
  }
  t.textContent = msg;
  t.classList.add('show');
  setTimeout(() => t!.classList.remove('show'), 2200);
}

export function mountConjure(root: HTMLElement, deps: ConjureDeps): void {
  let jobs: ApplyJob[] = []; // saved, not applied (Consecrated + Discerned)
  let manifested: ApplyJob[] = []; // saved AND applied
  let trashed: ApplyJob[] = []; // saved, dismissed / no longer available
  let sub: 'consecrated' | 'discerned' | 'manifested' | 'trashed' = 'discerned';
  const selected = new Set<string>(); // Consecrated jobs picked for Discern (multiselect)

  function payFoot(j: ApplyJob): string {
    const ps = payState(j);
    if (j.contract && j.payNote) return `<span class="amt contract">${esc(j.payNote)}</span><span class="basis">contract rate — not annualized</span>`;
    if (ps === 'posted') return `<span class="amt">${kMoney(j.smin)}–${kMoney(j.smax)}</span><span class="basis">per year · posted</span>`;
    if (ps === 'estimated') return `<span class="amt est">~${kMoney(j.emin)}–${kMoney(j.emax)}</span><span class="basis est">estimated · no pay in posting</span>`;
    return `<span class="amt muted">No pay listed</span><span class="basis">not stated in posting</span>`;
  }
  function payBadge(j: ApplyJob): string {
    const ps = payState(j);
    return ps === 'posted' ? '<span class="pb posted">Posted pay</span>' : ps === 'estimated' ? '<span class="pb est">Estimated</span>' : '<span class="pb none">No pay</span>';
  }

  function discernedCard(j: ApplyJob): string {
    return `<article class="ccard" draggable="true" data-u="${esc(j.u)}">
      <div class="chead"><div><div class="ctitle">${esc(j.t)}</div><div class="cmeta">${esc(j.c)} · ${esc(j.lp)}${j.r ? ' · Remote' : ''}</div></div>
        ${payBadge(j)}</div>
      <div class="clines">
        <div class="ln"><span class="k">Role</span><span class="v">${esc(j.role || '—')}</span></div>
        <div class="ln"><span class="k">Company</span><span class="v">${esc(j.does || '—')}</span></div>
        <div class="ln req"><span class="k">Required</span><span class="v">${esc(j.required || '--')}</span></div>
        <div class="ln pref"><span class="k">Preferred</span><span class="v">${esc(j.preferred || '--')}</span></div>
      </div>
      <div class="cfoot"><div class="pay">${payFoot(j)}</div>
        <div class="cact"><a class="openbtn" href="${esc(j.apply)}" target="_blank" rel="noopener">Open posting ↗</a>
          <button class="openbtn manifest-btn" type="button">✓ Mark manifested</button></div></div>
      ${j.a ? '' : '<div class="gone">⚠ No longer listed</div>'}
    </article>`;
  }

  // A Trash card: read-only — a job you dismissed, or one the Refresh sweep retired.
  function trashedCard(j: ApplyJob): string {
    return `<article class="ccard trashed" data-u="${esc(j.u)}">
      <div class="chead"><div><div class="ctitle">${esc(j.t)}</div><div class="cmeta">${esc(j.c)} · ${esc(j.lp)}${j.r ? ' · Remote' : ''}</div></div>${payBadge(j)}</div>
      <div class="cfoot"><span class="muted">Trashed — dismissed or no longer listed</span>
        <button class="openbtn restore-btn" type="button">↩ Restore</button></div>
    </article>`;
  }
  function consecratedCard(j: ApplyJob): string {
    const on = selected.has(j.u);
    return `<article class="ccard unprepped${on ? ' sel' : ''}" draggable="true" data-u="${esc(j.u)}" role="button" tabindex="0">
      <div class="chead"><div><div class="ctitle">${esc(j.t)}</div><div class="cmeta">${esc(j.c)} · ${esc(j.lp)}${j.r ? ' · Remote' : ''}</div></div>
        ${payBadge(j)}</div>
      <div class="uinfo">${payState(j) === 'none' ? '<span class="muted">no pay</span>' : `<span class="payr">${kMoney(j.smin || j.emin)}–${kMoney(j.smax || j.emax)}</span>`}<span class="selhint">${on ? '✓ selected' : 'click to select'}</span></div>
    </article>`;
  }

  // A Manifested card: the summary if we have one, plus Open posting; no manifest
  // toggle (it's already applied).
  function manifestedCard(j: ApplyJob): string {
    return `<article class="ccard applied" draggable="true" data-u="${esc(j.u)}">
      <div class="chead"><div><div class="ctitle">${esc(j.t)}</div><div class="cmeta">${esc(j.c)} · ${esc(j.lp)}${j.r ? ' · Remote' : ''}</div></div>${payBadge(j)}</div>
      ${isDiscerned(j) ? `<div class="clines"><div class="ln req"><span class="k">Required</span><span class="v">${esc(j.required || '--')}</span></div><div class="ln pref"><span class="k">Preferred</span><span class="v">${esc(j.preferred || '--')}</span></div></div>` : ''}
      <div class="cfoot"><div class="pay">${payFoot(j)}</div>
        <div class="cact"><span class="doneflag">✓ Manifested</span><a class="openbtn" href="${esc(j.apply)}" target="_blank" rel="noopener">Open posting ↗</a><button class="linkbtn unmani">Un-manifest</button></div></div>
    </article>`;
  }

  function render(): void {
    const consecrated = jobs.filter((j) => !isDiscerned(j));
    const discerned = jobs.filter(isDiscerned);
    const shown = sub === 'discerned' ? discerned : sub === 'consecrated' ? consecrated : sub === 'trashed' ? trashed : manifested;
    const cardFn = sub === 'consecrated' ? consecratedCard : sub === 'manifested' ? manifestedCard : sub === 'trashed' ? trashedCard : discernedCard;
    const empties: Record<string, string> = {
      discerned: 'Nothing discerned yet — Consecrate jobs in Scry, then Discern them here.',
      consecrated: 'No un-discerned saved jobs. ✨ Discern reads each into an apply card.',
      manifested: 'Nothing manifested yet — Open a posting to apply, then mark it manifested.',
      trashed: 'Trash is empty. Dismiss a job with 🗑 Trash, or Refresh to sweep expired ones here (manifested jobs are never swept).',
    };
    const cards = shown.length ? shown.map(cardFn).join('') : `<p class="cempty">${empties[sub]}</p>`;

    // Discern lives only on the Consecrated stage (it reads un-discerned jobs): select
    // tiles, or Select all, then Discern the selection.
    const nSel = consecrated.filter((j) => selected.has(j.u)).length;
    const actions = sub === 'consecrated' && consecrated.length
      ? `<button class="linkbtn" id="conjure-selall">${nSel === consecrated.length ? 'Clear' : 'Select all'}</button>
         <button class="discernbtn" id="conjure-discern"${nSel ? '' : ' disabled'}>✨ Discern${nSel ? ` (${nSel})` : ''}</button>`
      : '';

    root.innerHTML = `
      <p class="croomnote">Your shortlist, read for applying. <b>Consecrate</b> jobs in Scry, <b>Discern</b> the ones you pick into apply cards, then <b>Open posting</b> and <b>mark it manifested</b> yourself.</p>
      <div class="chead-row">
        <div class="csubs">
          <button data-sub="consecrated" class="${sub === 'consecrated' ? 'on' : ''}">★ Consecrated <span class="c">${consecrated.length}</span></button>
          <button data-sub="discerned" class="${sub === 'discerned' ? 'on' : ''}">Discerned <span class="c">${discerned.length}</span></button>
          <button data-sub="manifested" class="${sub === 'manifested' ? 'on' : ''}">✓ Manifested <span class="c">${manifested.length}</span></button>
          <button data-sub="trashed" class="${sub === 'trashed' ? 'on' : ''}">🗑 Trash <span class="c">${trashed.length}</span></button>
        </div>
        <div class="cactions">${actions}</div>
      </div>
      <div class="cgrid">${cards}</div>`;

    root.querySelectorAll<HTMLElement>('.csubs button').forEach((b) =>
      b.addEventListener('click', () => { sub = b.dataset.sub as typeof sub; render(); })
    );
    root.querySelectorAll<HTMLButtonElement>('.manifest-btn').forEach((b) =>
      b.addEventListener('click', () => markManifested(b.closest('.ccard') as HTMLElement))
    );
    root.querySelectorAll<HTMLButtonElement>('.unmani').forEach((b) =>
      b.addEventListener('click', () => markUnmanifested(b.closest('.ccard') as HTMLElement))
    );
    // Drag any card onto the Trash tab to dismiss it (works from every Conjure page).
    root.querySelectorAll<HTMLElement>('.ccard[draggable]').forEach((card) => {
      card.addEventListener('dragstart', (e) => { (e as DragEvent).dataTransfer!.setData('text/plain', card.dataset.u!); card.classList.add('dragging'); });
      card.addEventListener('dragend', () => card.classList.remove('dragging'));
    });
    root.querySelectorAll<HTMLButtonElement>('.restore-btn').forEach((b) =>
      b.addEventListener('click', () => restoreItem(b.closest('.ccard') as HTMLElement))
    );
    const trashTab = root.querySelector<HTMLElement>('.csubs button[data-sub="trashed"]');
    if (trashTab) {
      trashTab.addEventListener('dragover', (e) => { e.preventDefault(); trashTab.classList.add('drop'); });
      trashTab.addEventListener('dragleave', () => trashTab.classList.remove('drop'));
      trashTab.addEventListener('drop', (e) => { e.preventDefault(); trashTab.classList.remove('drop'); const u = (e as DragEvent).dataTransfer!.getData('text/plain'); if (u) trashByUrl(u); });
    }
    if (sub === 'consecrated') {
      root.querySelectorAll<HTMLElement>('.ccard.unprepped').forEach((card) =>
        card.addEventListener('click', () => { const u = card.dataset.u!; selected.has(u) ? selected.delete(u) : selected.add(u); render(); })
      );
      const sa = root.querySelector('#conjure-selall');
      if (sa) sa.addEventListener('click', () => { const all = consecrated.every((j) => selected.has(j.u)); consecrated.forEach((j) => (all ? selected.delete(j.u) : selected.add(j.u))); render(); });
    }
    const db = root.querySelector('#conjure-discern');
    if (db) db.addEventListener('click', () => discern(db as HTMLButtonElement, [...selected]));
  }

  // Mark manifested = honor-system self-report (applying happens off-site). Persists via
  // the saved endpoint's applied flag; the card then drops from the saved-not-applied set.
  function markManifested(card: HTMLElement): void {
    const u = card.dataset.u!;
    deps
      .authFetch(deps.api('saved'), {
        method: 'PUT',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ url: u, pinned: true, applied: true }),
      })
      .then((res) => {
        if (!res) {
          toast('Sign in to track manifested jobs');
          return;
        }
        const j = jobs.find((x) => x.u === u);
        jobs = jobs.filter((x) => x.u !== u);
        if (j) manifested.unshift(j);
        render();
        toast('✨ Manifested — moved to Manifested');
      })
      .catch(() => toast('Could not mark manifested'));
  }

  // Trash = dismiss a job: mark its listing unavailable. It leaves Scry and the shortlist
  // and lands in the Trash view. The server never trashes a manifested job.
  // Restore un-trashes a job (available=true again) — it returns to Scry and the shortlist.
  function restoreItem(card: HTMLElement): void {
    const u = card.dataset.u!;
    deps
      .authFetch(deps.api('applicator/restore'), {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ urls: [u] }),
      })
      .then((res) => {
        if (!res) { toast('Sign in to restore jobs'); return; }
        const j = trashed.find((x) => x.u === u);
        trashed = trashed.filter((x) => x.u !== u);
        if (j && !jobs.some((x) => x.u === u)) jobs.unshift(j);
        render();
        toast('↩ Restored to your shortlist');
      })
      .catch(() => toast('Could not restore'));
  }

  function trashByUrl(u: string): void {
    deps
      .authFetch(deps.api('applicator/trash'), {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ urls: [u] }),
      })
      .then((res) => {
        if (!res) { toast('Sign in to trash jobs'); return; }
        const j = jobs.find((x) => x.u === u) || manifested.find((x) => x.u === u);
        jobs = jobs.filter((x) => x.u !== u);
        manifested = manifested.filter((x) => x.u !== u);
        if (j && !trashed.some((x) => x.u === u)) trashed.unshift(j);
        render();
        toast('🗑 Trashed');
      })
      .catch(() => toast('Could not trash'));
  }

  // Un-manifest = undo an accidental manifest: clear the applied flag; the job returns
  // to the shortlist (Discerned/Consecrated) with its summary intact.
  function markUnmanifested(card: HTMLElement): void {
    const u = card.dataset.u!;
    deps
      .authFetch(deps.api('saved'), {
        method: 'PUT',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ url: u, pinned: true, applied: false }),
      })
      .then((res) => {
        if (!res) { toast('Sign in to change manifested jobs'); return; }
        const j = manifested.find((x) => x.u === u);
        manifested = manifested.filter((x) => x.u !== u);
        if (j) jobs.unshift(j);
        render();
        toast('Un-manifested — back in your shortlist');
      })
      .catch(() => toast('Could not un-manifest'));
  }

  // Discern = batch-summarize the Consecrated (saved, not-yet-summarized) jobs. The
  // server summarizes all such jobs; we poll its progress, then reload.
  function discern(btn: HTMLButtonElement, urls: string[]): void {
    if (!urls.length) return;
    btn.disabled = true;
    btn.textContent = '✨ Discerning…';
    deps
      .authFetch(deps.api('applicator/launch'), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ urls }) })
      .then((res) => {
        if (!res) throw new Error('Sign in as an admin to Discern');
        if (!res.ok) return res.text().then((t) => Promise.reject(new Error(t)));
        return res.json() as Promise<{ id: string }>;
      })
      .then(({ id }) => poll(id))
      .catch((e: Error) => {
        toast(e.message || 'Discern failed');
        render();
      });
  }
  function poll(id: string): void {
    fetch(deps.api('applicator/status') + '?id=' + encodeURIComponent(id))
      .then((r) => r.json() as Promise<{ status: string; done: number; total: number }>)
      .then((j) => {
        if (j.status === 'done') {
          toast(`🔮 Discerned ${j.total} job${j.total === 1 ? '' : 's'}`);
          selected.clear();
          sub = 'discerned';
          load();
        } else if (j.status === 'error') {
          toast('Discern failed');
          render();
        } else {
          setTimeout(() => poll(id), 500);
        }
      })
      .catch(() => {
        toast('Discern status lost');
        render();
      });
  }

  // api/applicator is USER-SCOPED (SavedNotApplied keys on the caller's identity), so the
  // read must carry the platform bearer — authFetch first, plain fetch as the guest
  // fallback. A plain-fetch-only read sends no identity, so the server sees no user and
  // returns nothing: that is why a job Consecrated in Scry never appeared here.
  function getJobs(path: string): Promise<{ jobs: ApplyJob[] }> {
    return deps
      .authFetch(deps.api(path))
      .then((r) => r || fetch(deps.api(path)))
      .then((r) => (r.ok ? (r.json() as Promise<{ jobs: ApplyJob[] }>) : Promise.reject(new Error(path + ' ' + r.status))));
  }
  function load(): void {
    root.innerHTML = '<p class="croomnote">Loading your shortlist…</p>';
    Promise.all([
      getJobs('applicator'),
      getJobs('applicator?view=applied').catch(() => ({ jobs: [] as ApplyJob[] })),
      getJobs('applicator?view=trashed').catch(() => ({ jobs: [] as ApplyJob[] })),
    ])
      .then(([active, applied, trash]) => {
        jobs = active.jobs || [];
        manifested = applied.jobs || [];
        trashed = trash.jobs || [];
        render();
      })
      .catch((e: Error) => {
        root.innerHTML = `<p class="croomnote err">Couldn't load your shortlist: ${esc(e.message)}</p>`;
      });
  }

  load();
}
