// The fitted trend line from analysis, and the band where the trend itself
// likely lies, at each time step of the chart.

import { timestampMs } from "@bufbuild/protobuf/wkt";
import type { FittedModel } from "@gen/analysis/v1/results_pb";

export interface TrendPoint {
  /** ms since 1970, UTC. */
  time: number;
  fit: number;
  low: number;
  high: number;
}

const YEAR_MS = 365.2425 * 86_400_000;

/**
 * The z for a two-sided interval at 1 - alpha, from Acklam's rational
 * approximation of the inverse normal (good to about 1e-9).
 */
export function zFor(alpha: number): number {
  const p = 1 - Math.min(Math.max(alpha, 1e-12), 0.999) / 2;
  const a = [-39.69683028665376, 220.9460984245205, -275.9285104469687, 138.357751867269, -30.66479806614716, 2.506628277459239];
  const b = [-54.47609879822406, 161.5858368580409, -155.6989798598866, 66.80131188771972, -13.28068155288572];
  const c = [-0.007784894002430293, -0.3223964580411365, -2.400758277161838, -2.549732539343734, 4.374664141464968, 2.938163982698783];
  const d = [0.007784695709041462, 0.3224671290700398, 2.445134137142996, 3.754408661907416];
  const [a0, a1, a2, a3, a4, a5] = a as [number, number, number, number, number, number];
  const [b0, b1, b2, b3, b4] = b as [number, number, number, number, number];
  const [c0, c1, c2, c3, c4, c5] = c as [number, number, number, number, number, number];
  const [d0, d1, d2, d3] = d as [number, number, number, number];
  if (p > 1 - 0.02425) {
    const q = Math.sqrt(-2 * Math.log(1 - p));
    return -(((((c0 * q + c1) * q + c2) * q + c3) * q + c4) * q + c5) / ((((d0 * q + d1) * q + d2) * q + d3) * q + 1);
  }
  const q = p - 0.5;
  const r = q * q;
  return ((((((a0 * r + a1) * r + a2) * r + a3) * r + a4) * r + a5) * q) / (((((b0 * r + b1) * r + b2) * r + b3) * r + b4) * r + 1);
}

/**
 * The line y = intercept + slope * t, with t in years since the model's time
 * origin, and the 1 - alpha band for the trend itself. Analysis puts the
 * origin at the mean time of the months it tested, so the intercept and the
 * slope are uncorrelated and the band is z * sqrt(se_a² + t² se_b²).
 */
export function trendLine(model: FittedModel, times: number[], alpha = 0.05): TrendPoint[] {
  if (!model.timeOrigin) return [];
  const origin = timestampMs(model.timeOrigin);
  const z = zFor(alpha);
  return times.map((time) => {
    const t = (time - origin) / YEAR_MS;
    const fit = model.intercept + model.slope * t;
    const se = Math.sqrt(model.interceptStderr ** 2 + (t * model.slopeStderr) ** 2);
    return { time, fit, low: fit - z * se, high: fit + z * se };
  });
}
