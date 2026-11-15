// The numbers behind "Why we think so": the shuffled trends as a histogram,
// and the trend triangle's cells.

import type { MonthlyTrend, ShuffleResult, TrendTriangleResult } from "@gen/analysis/v1/results_pb";

export interface Bin {
  x0: number;
  x1: number;
  n: number;
}

/** Counts of the finite numbers in xs, in equal bins from their least to their greatest. */
export function histogram(xs: readonly number[], bins = 30): Bin[] {
  const finite = xs.filter(Number.isFinite);
  if (!finite.length) return [];
  const lo = Math.min(...finite);
  const hi = Math.max(...finite);
  if (hi === lo) return [{ x0: lo, x1: hi, n: finite.length }];
  const w = (hi - lo) / bins;
  const out: Bin[] = Array.from({ length: bins }, (_, i) => ({ x0: lo + i * w, x1: lo + (i + 1) * w, n: 0 }));
  for (const x of finite) {
    const bin = out[Math.min(bins - 1, Math.floor((x - lo) / w))];
    if (bin) bin.n++;
  }
  return out;
}

/** The shuffle histogram, or undefined when too few shuffles ran to draw one. */
export function shuffleBins(s: ShuffleResult | undefined): { bins: Bin[]; observed: number } | undefined {
  if (!s || s.shuffledSlopesPerDecade.length < 20 || !Number.isFinite(s.observedSlopePerDecade)) return undefined;
  return { bins: histogram(s.shuffledSlopesPerDecade), observed: s.observedSlopePerDecade };
}

export interface TriangleCell {
  start: number;
  end: number;
  slope: number;
  p: number;
  /** p below alpha. Not corrected for testing many windows, so it's a guide only. */
  passes: boolean;
}

export interface Triangle {
  starts: number[];
  ends: number[];
  cells: TriangleCell[];
  /** The colour scale runs from -max to +max: the 95th percentile of the slopes' sizes. */
  max: number;
  minYears: number;
}

/**
 * The windows long enough to test, from the row-major [start][end] matrices.
 * undefined when the matrices don't match the years or no window has a slope.
 */
export function triangle(t: TrendTriangleResult | undefined, alpha: number): Triangle | undefined {
  if (!t) return undefined;
  const nS = t.startYears.length;
  const nE = t.endYears.length;
  if (!nS || !nE || t.slopesPerDecade.length !== nS * nE) return undefined;
  const cells: TriangleCell[] = [];
  for (let i = 0; i < nS; i++) {
    for (let j = 0; j < nE; j++) {
      const slope = t.slopesPerDecade[i * nE + j] ?? Number.NaN;
      if (!Number.isFinite(slope)) continue;
      const p = t.pValues[i * nE + j] ?? Number.NaN;
      cells.push({ start: t.startYears[i] ?? 0, end: t.endYears[j] ?? 0, slope, p, passes: Number.isFinite(p) && p < alpha });
    }
  }
  if (!cells.length) return undefined;
  const sizes = cells.map((c) => Math.abs(c.slope)).sort((a, b) => a - b);
  const max = sizes[Math.min(sizes.length - 1, Math.floor(0.95 * (sizes.length - 1)))] || sizes.at(-1) || 1;
  return { starts: [...t.startYears], ends: [...t.endYears], cells, max, minYears: t.minYears };
}

/**
 * Which of n labels to show, every few so they don't collide: the first, then
 * each every-th, with the last one in place of a near neighbour.
 */
export function labelled(n: number, every: number): Set<number> {
  const out: number[] = [];
  for (let i = 0; i < n; i += every) out.push(i);
  const last = n - 1;
  if (out.at(-1) !== last) {
    if (out.length > 1 && last - (out.at(-1) ?? 0) < every) out.pop();
    out.push(last);
  }
  return new Set(out);
}

export const MONTH_NAMES = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

export interface MonthBar {
  /** 1 to 12. */
  month: number;
  name: string;
  slope: number;
  p: number;
  passes: boolean;
}

/**
 * One bar per calendar month with a trend, January first. undefined when
 * fewer than 6 months could be tested, which says too little to draw.
 */
export function monthBars(trends: readonly MonthlyTrend[]): MonthBar[] | undefined {
  const bars = trends
    .filter((t) => t.month >= 1 && t.month <= 12 && Number.isFinite(t.slopePerDecade))
    .map((t) => ({ month: t.month, name: MONTH_NAMES[t.month - 1] ?? "", slope: t.slopePerDecade, p: t.pValue, passes: t.significant }))
    .sort((a, b) => a.month - b.month);
  return bars.length >= 6 ? bars : undefined;
}
