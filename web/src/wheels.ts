// Procedurally-drawn zodiac wheels — the brand's signature. Two versions: a clean geometric
// ICON (legible down to favicon size) and an ornate astrolabe BACKGROUND wheel (twelve sign
// glyphs, degree ticks, a star rosette, a radiant sun). Generated rather than hand-authored so
// every tick and glyph sits at an exact polar position. Colours come from the gold accent —
// CSS vars in-page (theme-adaptive), or hex for the favicon (which has no CSS context).

const NS = 'http://www.w3.org/2000/svg';
const GLYPHS = ['♈', '♉', '♊', '♋', '♌', '♍', '♎', '♏', '♐', '♑', '♒', '♓']; // Aries…Pisces
const rad = (d: number): number => ((d - 90) * Math.PI) / 180;
const P = (c: number, r: number, a: number): [number, number] => [c + r * Math.cos(rad(a)), c + r * Math.sin(rad(a))];
const defs = (id: string, g1: string, g2: string): string =>
  `<defs><linearGradient id="${id}" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${g1}"/><stop offset="1" stop-color="${g2}"/></linearGradient></defs>`;

/** Clean geometric wheel — rings, twelve houses + markers, a radiant sun core. */
export function iconWheel(g1 = 'var(--accent2)', g2 = 'var(--accent)'): string {
  const C = 24, id = 'zwi';
  const s: string[] = [`<svg viewBox="0 0 48 48" xmlns="${NS}">${defs(id, g1, g2)}`];
  const ring = (r: number, w: number, op = 1) => `<circle cx="${C}" cy="${C}" r="${r}" fill="none" stroke="url(#${id})" stroke-width="${w}" opacity="${op}"/>`;
  const line = (r1: number, r2: number, a: number, w: number, op = 1) => {
    const [x1, y1] = P(C, r1, a), [x2, y2] = P(C, r2, a);
    return `<line x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}" stroke="url(#${id})" stroke-width="${w}" stroke-linecap="round" opacity="${op}"/>`;
  };
  const dot = (r: number, a: number, rr: number) => { const [x, y] = P(C, r, a); return `<circle cx="${x}" cy="${y}" r="${rr}" fill="url(#${id})"/>`; };
  s.push(ring(22.4, 1.4), ring(20, 0.9, 0.8), ring(14.4, 1.1, 0.9));
  for (let a = 0; a < 360; a += 30) s.push(line(14.4, 20, a, 0.9, 0.85)); // twelve house dividers
  for (let a = 0; a < 360; a += 30) s.push(dot(17.2, a, a % 90 === 0 ? 1.5 : 1)); // house markers, cardinals larger
  for (let a = 0; a < 360; a += 45) s.push(line(6.4, 9.4, a, 1, 0.9)); // sun rays
  s.push(ring(6.4, 1, 0.9), `<circle cx="${C}" cy="${C}" r="2.4" fill="url(#${id})"/>`, '</svg>');
  return s.join('');
}

/** Ornate astrolabe wheel for the page background. */
export function bgWheel(g1 = 'var(--accent2)', g2 = 'var(--accent)'): string {
  const C = 500, id = 'zwb';
  const s: string[] = [`<svg viewBox="0 0 1000 1000" xmlns="${NS}">${defs(id, g1, g2)}`];
  const ring = (r: number, w: number, op = 1) => `<circle cx="${C}" cy="${C}" r="${r}" fill="none" stroke="url(#${id})" stroke-width="${w}" opacity="${op}"/>`;
  const line = (r1: number, r2: number, a: number, w: number, op = 1) => {
    const [x1, y1] = P(C, r1, a), [x2, y2] = P(C, r2, a);
    return `<line x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}" stroke="url(#${id})" stroke-width="${w}" stroke-linecap="round" opacity="${op}"/>`;
  };
  const dot = (r: number, a: number, rr: number, op = 1) => { const [x, y] = P(C, r, a); return `<circle cx="${x}" cy="${y}" r="${rr}" fill="url(#${id})" opacity="${op}"/>`; };
  s.push(ring(492, 3), ring(480, 1.2, 0.8), ring(454, 1.2, 0.75), ring(400, 1.2, 0.75), ring(372, 2, 0.85), ring(300, 1, 0.55), ring(150, 1, 0.45));
  for (let a = 0; a < 360; a += 5) { const maj = a % 30 === 0; s.push(line(maj ? 468 : 483, 480, a, maj ? 1.6 : 0.9, maj ? 1 : 0.6)); } // degree ticks
  for (let a = 0; a < 360; a += 30) s.push(line(372, 480, a, 1.1, 0.7)); // house dividers
  for (let i = 0; i < 12; i++) { const a = 15 + 30 * i; const [x, y] = P(C, 427, a); s.push(`<text x="${x}" y="${y}" font-size="40" text-anchor="middle" dominant-baseline="central" fill="url(#${id})" opacity="0.92">${GLYPHS[i]}︎</text>`); } // sign glyphs
  for (let a = 0; a < 360; a += 30) s.push(dot(372, a, 3.2, 0.8));
  let pts = ''; for (let i = 0; i < 24; i++) { const a = i * 15; const r = i % 2 === 0 ? 300 : 150; const [x, y] = P(C, r, a); pts += `${x},${y} `; }
  s.push(`<polygon points="${pts.trim()}" fill="none" stroke="url(#${id})" stroke-width="1" opacity="0.5" stroke-linejoin="round"/>`); // twelve-point rosette
  s.push(ring(60, 2, 0.9));
  for (let a = 0; a < 360; a += 15) s.push(line(64, 96, a, 1.4, 0.8));
  for (let a = 0; a < 360; a += 30) s.push(line(96, 120, a, 1, 0.55));
  s.push(`<circle cx="${C}" cy="${C}" r="22" fill="url(#${id})" opacity="0.95"/>`);
  ([[210, 20], [250, 70], [330, 140], [280, 200], [190, 255], [240, 310], [320, 340]] as const).forEach(([r, a]) => s.push(dot(r, a, 2, 0.5))); // constellation specks
  s.push('</svg>');
  return s.join('');
}
