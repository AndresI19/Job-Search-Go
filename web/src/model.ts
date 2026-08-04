// The typed Jobomancer data contract — the frontend mirror of the Go `resultDTO`
// (cmd/gui/results.go), served by `GET api/results`. This replaces the legacy
// {columns, rows:[{cells:[{value,fill}]}]} spreadsheet payload: the server sends
// domain facts only, and the client owns all presentation (tier tinting, pay-
// provenance styling, role classification, New-first grouping).
//
// Phase 2 (the Scry grid) consumes these types. Kept in its own module so the
// contract is documented and type-checked independently of the legacy main.ts.

export type PayState = 'posted' | 'estimated' | 'none';
export type Confidence = 'likely-real' | 'uncertain' | 'likely-ghost';

export interface Result {
  url: string;
  applyUrl?: string;
  title: string;
  company: string;
  companyTier?: 'f500' | 'software' | 'startup';
  location: string;
  remote: boolean;
  posted?: string; // RFC3339 UTC; absent when the source gave no date
  applicants: number; // -1 when unknown or bucketed ("200+")
  yearsExperience: number;
  salaryMin: number;
  salaryMax: number;
  salaryEstMin: number;
  salaryEstMax: number;
  payState: PayState;
  score: number; // 0..1 legitimacy
  confidence: Confidence;
  coverage: string[];
  verifiedVia?: string;
  reasoning?: string;
  new: boolean; // added by the latest scan — pinned to the top of the grid
}

export interface ResultsResponse {
  results: Result[];
  total: number;
}

// confidenceTone maps a verdict to a semantic UI severity — the footnote dot beside
// the Score column. Kept separate from the accent hue (good/warn/bad, not brand).
export function confidenceTone(c: Confidence): 'good' | 'warn' | 'bad' {
  return c === 'likely-real' ? 'good' : c === 'uncertain' ? 'warn' : 'bad';
}

// hasPay is true when a listing carries any usable pay figure (posted or estimated),
// so the grid/cards can distinguish "no pay listed" from a real range.
export function hasPay(r: Result): boolean {
  return r.payState !== 'none';
}
