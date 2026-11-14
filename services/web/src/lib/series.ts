// Reading a preprocessed Series for the chart: one point per time step, the
// stretches the line draws, and the words for what's missing.

import { timestampMs } from "@bufbuild/protobuf/wkt";
import { TimeStep } from "@gen/common/v1/common_pb";
import { AnomalyKind, MissingReason, type Series } from "@gen/preprocess/v1/data_pb";

export interface Point {
  index: number;
  /** Start of the step, in ms since 1970, UTC. */
  time: number;
  /** The measured value, or undefined where it's missing. */
  value: number | undefined;
  /** What the line draws: the value, or a short gap filled in for display. */
  line: number | undefined;
  /** True where the line uses a filled-in value. Never a measurement. */
  filled: boolean;
  anomaly: number | undefined;
  /** Share of the rectangle with data at this step, 0 to 1. */
  coverage: number | undefined;
  /** Why the value is missing, when it is. */
  missing: MissingReason | undefined;
  /** The calendar month's normal, when the series has one for it. */
  normal: { mean: number; sd: number } | undefined;
}

const finite = (v: number | undefined): number | undefined => (v === undefined || Number.isNaN(v) ? undefined : v);

/** One point per time step, with everything the chart and table show for it. */
export function points(s: Series): Point[] {
  const reasons = new Map(s.missing.map((m) => [m.index, m.reason]));
  const normals = new Map(s.normals.filter((n) => n.usable).map((n) => [n.month, { mean: n.mean, sd: n.sd }]));
  return s.times.map((t, i) => {
    const time = timestampMs(t);
    const value = finite(s.values[i]);
    const display = finite(s.displayValues[i]);
    return {
      index: i,
      time,
      value,
      line: value ?? display,
      filled: value === undefined && display !== undefined,
      anomaly: finite(s.anomalies[i]),
      coverage: finite(s.coverage[i]),
      missing: value === undefined ? (reasons.get(i) ?? MissingReason.UNSPECIFIED) : undefined,
      normal: normals.get(new Date(time).getUTCMonth() + 1),
    };
  });
}

/**
 * The stretches drawn dashed: each run of filled-in points, joined to the
 * measured points on either side so the dashes meet the solid line.
 */
export function filledRuns(pts: Point[]): Point[][] {
  const runs: Point[][] = [];
  let i = 0;
  while (i < pts.length) {
    if (!pts[i]?.filled) {
      i++;
      continue;
    }
    let j = i;
    while (pts[j + 1]?.filled) j++;
    const before = pts[i - 1];
    const after = pts[j + 1];
    runs.push([
      ...(before?.line !== undefined ? [before] : []),
      ...pts.slice(i, j + 1),
      ...(after?.line !== undefined ? [after] : []),
    ]);
    i = j + 1;
  }
  return runs;
}

/** Measured points with no measured neighbour, which a line can't show. Drawn as dots. */
export function lonePoints(pts: Point[]): Point[] {
  return pts.filter(
    (p, i) => p.value !== undefined && pts[i - 1]?.line === undefined && pts[i + 1]?.line === undefined,
  );
}

/** The point whose time is nearest, or undefined for an empty series. */
export function nearest(pts: Point[], time: number): Point | undefined {
  let lo = 0;
  let hi = pts.length - 1;
  if (hi < 0) return undefined;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if ((pts[mid]?.time ?? 0) < time) lo = mid + 1;
    else hi = mid;
  }
  const after = pts[lo];
  const before = pts[lo - 1];
  if (before && after && time - before.time < after.time - time) return before;
  return after;
}

/** The lowest and highest value the chart has to show, line and normal range included. */
export function valueExtent(pts: Point[]): [number, number] | undefined {
  let lo = Infinity;
  let hi = -Infinity;
  for (const p of pts) {
    for (const v of [p.line, p.normal && p.normal.mean - p.normal.sd, p.normal && p.normal.mean + p.normal.sd]) {
      if (v === undefined) continue;
      lo = Math.min(lo, v);
      hi = Math.max(hi, v);
    }
  }
  return lo <= hi ? [lo, hi] : undefined;
}

export interface Summary {
  steps: number;
  measured: number;
  missing: number;
  filled: number;
}

export function summarize(pts: Point[]): Summary {
  const measured = pts.filter((p) => p.value !== undefined).length;
  return {
    steps: pts.length,
    measured,
    missing: pts.length - measured,
    filled: pts.filter((p) => p.filled).length,
  };
}

// Fixed abbreviations: Intl's British English writes "Sept", out of step
// with the other three-letter months.
const months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "Mar 2021" for monthly steps, "3 Mar 2021" for daily and weekly ones. */
export function formatTime(time: number, step: TimeStep): string {
  const d = new Date(time);
  const monthYear = `${months[d.getUTCMonth()]} ${d.getUTCFullYear()}`;
  return step === TimeStep.MONTHLY ? monthYear : `${d.getUTCDate()} ${monthYear}`;
}

const significant = new Intl.NumberFormat("en-US", { maximumSignificantDigits: 4 });

/** Four significant digits: 12.35, 400.1, 0.0001234. */
export function formatValue(v: number): string {
  return significant.format(v);
}

/** "+1.2" or "−0.4", with a real minus sign. */
export function formatSigned(v: number): string {
  const s = formatValue(Math.abs(v));
  return v > 0 ? `+${s}` : v < 0 ? `−${s}` : s;
}

export function formatCoverage(c: number): string {
  return `${Math.round(c * 100)}%`;
}

/** The unit an anomaly is measured in. */
export function anomalyUnits(s: Series): string {
  switch (s.anomalyKind) {
    case AnomalyKind.PERCENT:
      return "%";
    case AnomalyKind.STANDARDIZED:
      return "standard deviations";
    default:
      return s.units;
  }
}

const reasons: Record<MissingReason, string> = {
  [MissingReason.UNSPECIFIED]: "no data",
  [MissingReason.FILL_VALUE]: "no data in the file",
  [MissingReason.QC_FAILED]: "failed NASA's quality checks",
  [MissingReason.CLOUD]: "hidden by cloud",
  [MissingReason.POLAR_NIGHT]: "polar night, no sunlight to measure with",
  [MissingReason.INSTRUMENT_OUTAGE]: "the instrument was off",
  [MissingReason.BEFORE_MISSION]: "before the satellite was launched",
  [MissingReason.LOW_COVERAGE]: "too little of the rectangle had data",
  [MissingReason.TOO_FEW_DAYS]: "too few days with data that month",
  [MissingReason.NOT_POSITIVE]: "zero or below, which has no logarithm",
  [MissingReason.NO_NORMAL]: "no normal for this month to compare with",
};

/** Why a step is missing, in words. */
export function missingLabel(r: MissingReason): string {
  return reasons[r] ?? reasons[MissingReason.UNSPECIFIED];
}
