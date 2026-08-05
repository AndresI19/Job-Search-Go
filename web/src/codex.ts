// The Codex — the third Jobomancer room: a personal library of reusable copy-paste
// application text (cover letters, snippets, "why this company" answers). Each template's
// body carries {{TOKENS}} like {{COMPANY}} / {{POSITION}}; a param bar collects one value
// per distinct token and substitutes them the moment you Copy.
//
// Storage is transparent: signed-in users persist to the server (api/codex); guests keep
// theirs in localStorage. A few generic, non-personal STARTERS ship read-only so the room
// is never blank. Self-contained and typed — platform auth is injected, never imported.
import './codex.css';

export interface Template {
  id: string;
  title: string;
  category: string;
  body: string;
  tags: string; // comma-joined
  updatedAt?: string;
  starter?: boolean; // built-in, read-only
}

export interface CodexDeps {
  api: (p: string) => string;
  authFetch: (url: string, init?: RequestInit) => Promise<Response | null>;
  isSignedIn: () => boolean;
}

const LS_TPL = 'jobomancer:codex:templates';
const LS_PARAMS = 'jobomancer:codex:params';
// Reserved category: your PERSONAL info (name, phone, email, LinkedIn, GitHub, …), kept
// separate from company/application templates. Rendered as a compact field strip, and its
// values feed the {{TOKENS}} in templates (a "LinkedIn" field fills {{LINKEDIN}}). 'Quick Info'
// is the legacy name for the same category, still recognised so older entries aren't orphaned.
const PERSONAL = 'Personal Info';
const QUICK = 'Quick Info';
const isPersonal = (t: Template): boolean => t.category === PERSONAL || t.category === QUICK;
// The {{TOKEN}} a personal field fills: its label upper-cased ("Full Name" → FULL_NAME).
export const fieldToken = (label: string): string => label.trim().toUpperCase().replace(/[^A-Z0-9]+/g, '_').replace(/^_|_$/g, '');
// Build a token→value map from a user's personal fields, for filling templates elsewhere (Conjure).
export function personalTokens(templates: Template[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const t of templates) if (isPersonal(t)) { const k = fieldToken(t.title); if (k) out[k] = t.body; }
  return out;
}
// Preset fields — always shown and non-deletable; the user just fills their values. Extra
// custom fields can be added below and removed. FIELD_SUGGESTIONS backs the custom-add datalist.
const PRESET_FIELDS = ['Name', 'Email', 'Phone', 'LinkedIn', 'GitHub', 'Website'];
const FIELD_SUGGESTIONS = ['Location', 'Portfolio', 'Twitter', 'Pronouns'];

const esc = (s: unknown) =>
  (s == null ? '' : String(s)).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

// Generic, non-personal skeletons — safe to ship. They demonstrate tokens and the Copy
// flow without revealing anyone's real letters. Read-only; "Duplicate" makes an editable copy.
export const STARTERS: Template[] = [
  {
    id: 'starter:cover',
    starter: true,
    category: 'Cover letter',
    tags: '',
    title: 'Cover letter — skeleton',
    body:
      'Dear {{COMPANY}} Hiring Team,\n\n' +
      "I'm applying for the {{POSITION}} role. In my work I've {{ONE_LINE_ACHIEVEMENT}}, and I'm drawn to {{COMPANY}} because {{WHY_COMPANY}}.\n\n" +
      "I'd welcome the chance to talk about how I can contribute.\n\nBest regards,\n{{NAME}}",
  },
  {
    id: 'starter:why',
    starter: true,
    category: 'Q&A',
    tags: '',
    title: 'Why {{COMPANY}}? — skeleton',
    body: 'What draws me to {{COMPANY}} is {{WHY_COMPANY}}. The {{POSITION}} role fits how I like to work because {{FIT}}.',
  },
  {
    id: 'starter:followup',
    starter: true,
    category: 'Follow-up',
    tags: '',
    title: 'Follow-up email',
    body:
      'Hi {{HIRING_MANAGER}},\n\n' +
      "Thanks for taking the time to discuss the {{POSITION}} role. Our conversation about {{TOPIC}} reinforced my interest in {{COMPANY}}. Happy to share anything else that's useful.\n\nBest,\n{{NAME}}",
  },
];

const TOKEN_RE = /\{\{\s*([A-Z0-9_]+)\s*\}\}/g;
function tokensIn(body: string): string[] {
  const out: string[] = [];
  for (const m of body.matchAll(TOKEN_RE)) if (!out.includes(m[1])) out.push(m[1]);
  return out;
}
// Fill known tokens; leave unknown ones as-is so an unfilled field is visible in the paste.
export function fill(body: string, params: Record<string, string>): string {
  return body.replace(TOKEN_RE, (whole, tok) => (params[tok] ? params[tok] : whole));
}
const prettyToken = (t: string) => t.replace(/_/g, ' ').toLowerCase().replace(/^\w/, (c) => c.toUpperCase());

const loadLocalTemplates = (): Template[] => { try { return JSON.parse(localStorage.getItem(LS_TPL) || '[]'); } catch { return []; } };

// When a guest signs in, upload any templates they made in localStorage to their account, so
// guest work follows them. Clears localStorage only if EVERY template uploaded (a partial
// failure retries next sign-in). Returns how many synced. No-op for guests / an empty store.
export async function migrateGuestTemplates(deps: CodexDeps): Promise<number> {
  if (!deps.isSignedIn()) return 0;
  const local = loadLocalTemplates();
  if (!local.length) return 0;
  let ok = 0;
  for (const t of local) {
    try {
      const res = await deps.authFetch(deps.api('codex'), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(t) });
      if (res && res.ok) ok++;
    } catch { /* leave it in localStorage to retry */ }
  }
  if (ok === local.length) { try { localStorage.removeItem(LS_TPL); } catch { /* private mode */ } }
  return ok;
}
// Shared loader: a user's templates (server when signed in, localStorage for guests). Used by
// the Codex room AND by Conjure's "cover letter" auto-fill, so both read the same store.
export async function fetchTemplates(deps: CodexDeps): Promise<Template[]> {
  if (deps.isSignedIn()) {
    try { const res = await deps.authFetch(deps.api('codex')); if (res && res.ok) return ((await res.json()).templates || []) as Template[]; } catch { /* fall through to empty */ }
    return [];
  }
  return loadLocalTemplates();
}

export function mountCodex(root: HTMLElement, deps: CodexDeps): void {
  let templates: Template[] = [];
  let params: Record<string, string> = loadParams();
  let editing: Template | 'new' | null = null;
  let filterCat: string | null = null;
  let qForm: Template | 'new' | null = null; // inline Quick Info add/edit form

  function loadParams(): Record<string, string> {
    try {
      return JSON.parse(localStorage.getItem(LS_PARAMS) || '{}');
    } catch {
      return {};
    }
  }
  function saveParams(): void {
    try {
      localStorage.setItem(LS_PARAMS, JSON.stringify(params));
    } catch {
      /* private mode — params just won't persist */
    }
  }
  function loadLocal(): Template[] {
    try {
      return JSON.parse(localStorage.getItem(LS_TPL) || '[]');
    } catch {
      return [];
    }
  }
  function saveLocal(list: Template[]): void {
    try {
      localStorage.setItem(LS_TPL, JSON.stringify(list));
    } catch {
      /* private mode */
    }
  }

  async function load(): Promise<void> {
    root.innerHTML = `<p class="croomnote">Loading your Codex…</p>`;
    templates = await fetchTemplates(deps);
    // Seed the token param values from Personal Info, so {{NAME}}/{{LINKEDIN}}/… are pre-filled.
    for (const [k, v] of Object.entries(personalTokens(templates))) if (!params[k]) params[k] = v;
    render();
  }

  function persist(t: Template): void {
    if (deps.isSignedIn()) {
      deps
        .authFetch(deps.api('codex'), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(t) })
        .then((res) => {
          if (!res || !res.ok) throw new Error('save failed');
        })
        .catch(() => toast('Could not save — check your connection'));
    } else {
      const list = loadLocal().filter((x) => x.id !== t.id);
      list.unshift(t);
      saveLocal(list);
    }
  }
  function removeTemplate(id: string): void {
    if (deps.isSignedIn()) {
      deps.authFetch(deps.api('codex') + '?id=' + encodeURIComponent(id), { method: 'DELETE' }).catch(() => {});
    } else {
      saveLocal(loadLocal().filter((x) => x.id !== id));
    }
  }

  function copy(body: string): void {
    const text = fill(body, params);
    navigator.clipboard?.writeText(text).then(
      () => toast('📋 Copied — tokens filled from the bar above'),
      () => toast('Copy blocked by the browser'),
    );
  }

  // ---- rendering ----------------------------------------------------------------
  // Quick Info items render in their own compact strip, so they're kept out of the
  // token param bar, the category chips, and the main card grid.
  const quickItems = (): Template[] => templates.filter(isPersonal);
  function shownTemplates(): Template[] {
    const all = [...templates, ...STARTERS].filter((t) => !isPersonal(t));
    return filterCat ? all.filter((t) => (t.category || 'Uncategorized') === filterCat) : all;
  }

  function paramBar(): string {
    const toks: string[] = [];
    for (const t of shownTemplates()) for (const tk of tokensIn(t.body)) if (!toks.includes(tk)) toks.push(tk);
    if (!toks.length) return '';
    const inputs = toks
      .map(
        (tk) =>
          `<label class="cx-param"><span>${esc(prettyToken(tk))}</span><input data-tok="${esc(tk)}" value="${esc(params[tk] || '')}" placeholder="{{${esc(tk)}}}" autocomplete="off"></label>`,
      )
      .join('');
    return `<div class="cx-parambar"><span class="cx-parhead">Fill once, copy anywhere</span>${inputs}</div>`;
  }

  function categoryChips(): string {
    const cats = Array.from(new Set([...templates, ...STARTERS].filter((t) => !isPersonal(t)).map((t) => t.category || 'Uncategorized')));
    const chip = (label: string, key: string | null) =>
      `<button class="cx-chip${filterCat === key ? ' on' : ''}" data-cat="${key === null ? '' : esc(key)}">${esc(label)}</button>`;
    return `<div class="cx-chips">${chip('All', null)}${cats.map((c) => chip(c, c)).join('')}</div>`;
  }

  function templateCard(t: Template): string {
    const toks = tokensIn(t.body);
    const preview = esc(t.body.length > 320 ? t.body.slice(0, 320) + '…' : t.body);
    const tags = t.tags
      ? t.tags
          .split(',')
          .map((x) => x.trim())
          .filter(Boolean)
          .map((x) => `<span class="cx-tag">${esc(x)}</span>`)
          .join('')
      : '';
    const actions = t.starter
      ? `<button class="cx-btn" data-dup="${esc(t.id)}">Duplicate to edit</button>
         <button class="cx-btn primary" data-copy="${esc(t.id)}">Copy</button>`
      : `<button class="cx-btn" data-edit="${esc(t.id)}">Edit</button>
         <button class="cx-btn danger" data-del="${esc(t.id)}">Delete</button>
         <button class="cx-btn primary" data-copy="${esc(t.id)}">Copy</button>`;
    return `<article class="cx-card${t.starter ? ' starter' : ''}">
      <div class="cx-card-h">
        <div><div class="cx-title">${esc(t.title || 'Untitled')}</div>
          <div class="cx-meta"><span class="cx-cat">${esc(t.category || 'Uncategorized')}</span>${t.starter ? '<span class="cx-badge">starter</span>' : ''}${tags}</div></div>
      </div>
      <pre class="cx-body">${preview}</pre>
      ${toks.length ? `<div class="cx-tokens">${toks.map((x) => `<code>{{${esc(x)}}}</code>`).join('')}</div>` : ''}
      <div class="cx-actions">${actions}</div>
    </article>`;
  }

  function editor(): string {
    const t = editing === 'new' || editing === null ? { id: '', title: '', category: '', body: '', tags: '' } : editing;
    const cats = Array.from(new Set([...templates, ...STARTERS].map((x) => x.category).filter(Boolean)));
    return `<div class="cx-editor">
      <div class="cx-ed-h">${editing === 'new' ? 'New template' : 'Edit template'}</div>
      <label class="cx-field"><span>Title</span><input id="cx-title" value="${esc(t.title)}" placeholder="e.g. Cover letter — backend roles" autocomplete="off"></label>
      <div class="cx-field-row">
        <label class="cx-field"><span>Category</span><input id="cx-cat" value="${esc(t.category)}" placeholder="Cover letter" list="cx-cats" autocomplete="off"></label>
        <label class="cx-field"><span>Tags (comma-separated)</span><input id="cx-tags" value="${esc(t.tags)}" placeholder="backend, remote" autocomplete="off"></label>
      </div>
      <datalist id="cx-cats">${cats.map((c) => `<option value="${esc(c)}">`).join('')}</datalist>
      <label class="cx-field"><span>Body — use {{TOKENS}} like {{COMPANY}}; they become fill-in fields above.</span>
        <textarea id="cx-body" rows="9" placeholder="Dear {{COMPANY}} Hiring Team, …">${esc(t.body)}</textarea></label>
      <div class="cx-ed-actions">
        <button class="cx-btn" id="cx-cancel">Cancel</button>
        <button class="cx-btn primary" id="cx-save">Save template</button>
      </div>
    </div>`;
  }

  // Personal Info — your name / contact fields (label + value), kept separate from templates.
  // Their values fill the matching {{TOKEN}} in cover letters (here and in Conjure). A light
  // inline add/edit form with suggested field names; the label datalist offers the common ones.
  function quickForm(): string {
    const t = qForm === 'new' || qForm === null ? { title: '', body: '' } : qForm;
    return `<div class="cx-qform">
      <input id="cx-qlabel" value="${esc(t.title)}" placeholder="Field — e.g. LinkedIn" list="cx-fields" autocomplete="off">
      <datalist id="cx-fields">${FIELD_SUGGESTIONS.map((f) => `<option value="${esc(f)}">`).join('')}</datalist>
      <input id="cx-qvalue" value="${esc(t.body)}" placeholder="Value — e.g. linkedin.com/in/you" autocomplete="off">
      <button class="cx-btn primary" id="cx-qsave">Save</button>
      <button class="cx-btn" id="cx-qcancel">Cancel</button>
    </div>`;
  }
  function quickSection(): string {
    const items = quickItems();
    const presetToks = new Set(PRESET_FIELDS.map(fieldToken));
    const valueOf = (field: string) => { const t = items.find((x) => fieldToken(x.title) === fieldToken(field)); return t ? t.body : ''; };
    // Preset fields — always present, non-deletable; just fill the value.
    const presetRows = PRESET_FIELDS.map(
      (f) => `<label class="cx-pf"><span class="cx-pf-k">${esc(f)}</span><input class="cx-pf-v" data-field="${esc(f)}" value="${esc(valueOf(f))}" placeholder="add your ${esc(f.toLowerCase())} — fills {{${esc(fieldToken(f))}}}" autocomplete="off"></label>`,
    ).join('');
    // Custom fields — anything not a preset; these keep edit + delete.
    const custom = items.filter((t) => !presetToks.has(fieldToken(t.title)));
    const customRows = custom
      .map((t) => {
        const v = t.body.trim();
        return `<div class="cx-qi">
          <button class="cx-qi-copy" data-copyqi="${esc(t.id)}" title="Copy ${esc(t.title)}"><span class="cx-qi-k">${esc(t.title || 'Untitled')}</span><span class="cx-qi-v">${esc(v.length > 60 ? v.slice(0, 60) + '…' : v)}</span><span class="cx-qi-tok">{{${esc(fieldToken(t.title))}}}</span></button>
          <button class="cx-qi-mini" data-qedit="${esc(t.id)}" title="Edit" aria-label="Edit">✎</button>
          <button class="cx-qi-mini" data-qdel="${esc(t.id)}" title="Delete" aria-label="Delete">✕</button>
        </div>`;
      })
      .join('');
    return `<div class="cx-quick">
      <div class="cx-quick-h"><span>👤 Personal Info</span></div>
      <p class="cx-pf-note">Fill these once — each value fills its matching <code>{{TOKEN}}</code> in your cover letters, here and when applying from Conjure.</p>
      <div class="cx-pf-grid">${presetRows}</div>
      ${custom.length ? `<div class="cx-quick-list cx-custom">${customRows}</div>` : ''}
      <div class="cx-pf-add">${qForm ? quickForm() : '<button class="cx-btn cx-qadd" id="cx-qadd">+ Add custom field</button>'}</div>
    </div>`;
  }

  function render(): void {
    const shown = shownTemplates();
    const mine = shown.filter((t) => !t.starter);
    const starters = shown.filter((t) => t.starter);
    const signedOut = !deps.isSignedIn();
    const grid = (list: Template[]) => list.map(templateCard).join('');
    root.innerHTML = `
      <p class="croomnote">Your <b>Codex</b> — reusable application text. Fill the tokens once, then <b>Copy</b> any template with them substituted.${
        signedOut ? ' <span class="cx-note">Guest mode: templates are saved in this browser. <b>Sign in</b> to sync them.</span>' : ''
      }</p>
      ${quickSection()}
      ${paramBar()}
      <div class="cx-toolbar">
        ${categoryChips()}
        <button class="cx-btn primary cx-new" id="cx-new">+ New template</button>
      </div>
      ${editing ? editor() : ''}
      ${mine.length ? `<div class="cx-grid">${grid(mine)}</div>` : `<p class="cx-empty">No templates yet. <b>+ New template</b> to add your first, or duplicate a starter below.</p>`}
      ${starters.length ? `<div class="cx-sec">Starters <span class="cx-secsub">generic skeletons — duplicate to make your own</span></div><div class="cx-grid">${grid(starters)}</div>` : ''}`;

    // param inputs
    root.querySelectorAll<HTMLInputElement>('.cx-param input').forEach((inp) =>
      inp.addEventListener('input', () => {
        params[inp.dataset.tok!] = inp.value;
        saveParams();
      }),
    );
    // category filter
    root.querySelectorAll<HTMLElement>('.cx-chip').forEach((b) =>
      b.addEventListener('click', () => {
        filterCat = b.dataset.cat ? b.dataset.cat : null;
        render();
      }),
    );
    // copy / edit / delete / duplicate
    const byId = (id: string) => [...templates, ...STARTERS].find((t) => t.id === id);
    root.querySelectorAll<HTMLElement>('[data-copy]').forEach((b) =>
      b.addEventListener('click', () => {
        const t = byId(b.dataset.copy!);
        if (t) copy(t.body);
      }),
    );
    root.querySelectorAll<HTMLElement>('[data-edit]').forEach((b) =>
      b.addEventListener('click', () => {
        const t = byId(b.dataset.edit!);
        if (t) {
          editing = { ...t };
          render();
        }
      }),
    );
    root.querySelectorAll<HTMLElement>('[data-dup]').forEach((b) =>
      b.addEventListener('click', () => {
        const t = byId(b.dataset.dup!);
        if (t) {
          editing = { id: '', title: t.title.replace(' — skeleton', ''), category: t.category, body: t.body, tags: t.tags };
          render();
        }
      }),
    );
    root.querySelectorAll<HTMLElement>('[data-del]').forEach((b) =>
      b.addEventListener('click', () => {
        const t = byId(b.dataset.del!);
        if (t && confirm(`Delete "${t.title || 'this template'}"? This can't be undone.`)) {
          removeTemplate(t.id);
          templates = templates.filter((x) => x.id !== t.id);
          toast('Deleted');
          render();
        }
      }),
    );
    // toolbar / editor
    root.querySelector('#cx-new')?.addEventListener('click', () => {
      editing = 'new';
      render();
    });
    root.querySelector('#cx-cancel')?.addEventListener('click', () => {
      editing = null;
      render();
    });
    root.querySelector('#cx-save')?.addEventListener('click', save);

    // Preset personal fields: save on change (blur/enter). No re-render — the input keeps its value.
    root.querySelectorAll<HTMLInputElement>('.cx-pf-v').forEach((inp) =>
      inp.addEventListener('change', () => savePresetField(inp.dataset.field!, inp.value)),
    );
    // Quick Info: copy verbatim (no token fill), inline add/edit, delete.
    root.querySelectorAll<HTMLElement>('[data-copyqi]').forEach((b) =>
      b.addEventListener('click', () => {
        const t = templates.find((x) => x.id === b.dataset.copyqi);
        if (t) navigator.clipboard?.writeText(t.body.trim()).then(() => toast('📋 Copied'), () => toast('Copy blocked by the browser'));
      }),
    );
    root.querySelectorAll<HTMLElement>('[data-qedit]').forEach((b) =>
      b.addEventListener('click', () => {
        const t = templates.find((x) => x.id === b.dataset.qedit);
        if (t) {
          qForm = { ...t };
          render();
        }
      }),
    );
    root.querySelectorAll<HTMLElement>('[data-qdel]').forEach((b) =>
      b.addEventListener('click', () => {
        const t = templates.find((x) => x.id === b.dataset.qdel);
        if (t && confirm(`Delete "${t.title || 'this item'}"?`)) {
          removeTemplate(t.id);
          templates = templates.filter((x) => x.id !== t.id);
          toast('Deleted');
          render();
        }
      }),
    );
    root.querySelector('#cx-qadd')?.addEventListener('click', () => {
      qForm = 'new';
      render();
    });
    root.querySelector('#cx-qcancel')?.addEventListener('click', () => {
      qForm = null;
      render();
    });
    root.querySelector('#cx-qsave')?.addEventListener('click', saveQuick);
  }

  // Upsert (or clear) a preset field's value, keyed by its token so a field is never duplicated.
  function savePresetField(field: string, value: string): void {
    const tok = fieldToken(field);
    const existing = quickItems().find((t) => fieldToken(t.title) === tok);
    const v = value.trim();
    if (!v) {
      if (existing) { removeTemplate(existing.id); templates = templates.filter((x) => x.id !== existing.id); }
      delete params[tok];
      saveParams();
      return;
    }
    const t: Template = { id: existing ? existing.id : 'tpl-' + (crypto.randomUUID?.() || String(Date.now())), title: field, category: PERSONAL, body: v, tags: '' };
    persist(t);
    templates = [t, ...templates.filter((x) => x.id !== t.id)];
    params[tok] = v;
    saveParams();
  }

  function saveQuick(): void {
    const label = (root.querySelector<HTMLInputElement>('#cx-qlabel')?.value || '').trim();
    const value = (root.querySelector<HTMLInputElement>('#cx-qvalue')?.value || '').trim();
    if (!label && !value) {
      toast('Add a label or value first');
      return;
    }
    const existingId = qForm && qForm !== 'new' ? qForm.id : '';
    const t: Template = {
      id: existingId || 'tpl-' + (crypto.randomUUID?.() || String(Date.now())),
      title: label,
      category: PERSONAL,
      body: value,
      tags: '',
    };
    persist(t);
    templates = [t, ...templates.filter((x) => x.id !== t.id)];
    qForm = null;
    toast('✨ Saved to Quick Info');
    render();
  }

  function save(): void {
    const val = (id: string) => (root.querySelector<HTMLInputElement | HTMLTextAreaElement>('#' + id)?.value || '').trim();
    const title = val('cx-title');
    const body = root.querySelector<HTMLTextAreaElement>('#cx-body')?.value || '';
    if (!title && !body.trim()) {
      toast('Add a title or body first');
      return;
    }
    const existingId = editing && editing !== 'new' ? editing.id : '';
    const t: Template = {
      id: existingId || 'tpl-' + (crypto.randomUUID?.() || String(Date.now())),
      title,
      category: val('cx-cat') || 'Uncategorized',
      body,
      tags: val('cx-tags'),
    };
    persist(t);
    templates = [t, ...templates.filter((x) => x.id !== t.id)];
    editing = null;
    toast('✨ Saved to your Codex');
    render();
  }

  // Codex has its own toast (amber), matching Scry/Conjure's self-contained pattern.
  function toast(msg: string): void {
    let el = document.getElementById('codex-toast');
    if (!el) {
      el = document.createElement('div');
      el.id = 'codex-toast';
      el.className = 'codex-toast';
      document.body.appendChild(el);
    }
    el.textContent = msg;
    el.classList.add('show');
    window.setTimeout(() => el?.classList.remove('show'), 2200);
  }

  load();
}
