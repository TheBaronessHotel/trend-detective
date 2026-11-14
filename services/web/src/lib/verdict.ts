// What the analysis found, in plain language for the verdict panel. Every
// number comes from analysis; this only words it.

import { timestampMs } from "@bufbuild/protobuf/wkt";
import { SeriesField } from "@gen/analysis/v1/analysis_pb";
import type { SeriesAnalysis } from "@gen/analysis/v1/results_pb";
import { TimeStep } from "@gen/common/v1/common_pb";
import { formatSigned, formatTime, formatValue } from "./series";

export type Direction = "rising" | "falling" | "flat";

export interface Verdict {
  direction: Direction;
  /** Whether the trend is unlikely to be chance. */
  significant: boolean;
  headline: string;
  /** The rate, for example "+0.25 °C per decade". */
  rate: string;
  /** The answer's support: the rate, chance, and how long it would take. */
  lines: string[];
  /** More detail for the curious: the second test, shuffles, the biggest shift. */
  more: string[];
  /** Analysis's own caveats, as written. */
  warnings: string[];
}

function percent(p: number): string {
  if (p < 0.001) return "less than 0.1%";
  if (p < 0.01) return `${(p * 100).toFixed(1)}%`;
  return `${Math.round(p * 100)}%`;
}

/**
 * A rate rounded to what its uncertainty supports: a likely range of ±0.1
 * shows two decimals. Zero shows without a sign.
 */
export function rounded(v: number, halfWidth: number): string {
  const digits = Math.min(6, Math.max(0, 1 - Math.floor(Math.log10(Math.max(halfWidth, 1e-9)))));
  const r = Number(v.toFixed(digits));
  const text = Math.abs(r).toFixed(digits);
  if (r === 0) return text;
  return r > 0 ? `+${text}` : `−${text}`;
}

/** A value with a real minus sign, as in the rest of the app's text. */
function num(v: number): string {
  return formatValue(v).replace("-", "−");
}

function years(n: number): string {
  const r = n < 10 ? Math.round(n * 10) / 10 : Math.round(n);
  return `${r} ${r === 1 ? "year" : "years"}`;
}

/** The verdict, or undefined when analysis ran no trend test. */
export function verdict(a: SeriesAnalysis): Verdict | undefined {
  const reg = a.regression;
  const mk = a.mannKendall;
  if (!reg && !mk) return undefined;

  const units = a.units ? ` ${a.units}` : "";
  const slope = reg ? reg.slopePerDecade : (mk?.sensSlopePerDecade ?? 0);
  const significant = reg ? reg.significant : !!mk?.significant;
  const direction: Direction = slope > 0 ? "rising" : slope < 0 ? "falling" : "flat";
  const what = a.field === SeriesField.VALUES ? "the values" : "the values, with the seasons taken out,";

  const headline = significant
    ? `${direction === "rising" ? "Rising" : "Falling"}, and unlikely to be chance`
    : "No clear trend yet";

  const lines: string[] = [];
  const more: string[] = [];
  const half = reg ? Math.abs(reg.slopeCiHigh - reg.slopeCiLow) / 2 : mk ? Math.abs(mk.sensCiHigh - mk.sensCiLow) / 2 : 0;
  const fmt = (v: number) => (half > 0 ? rounded(v, half) : formatSigned(v));
  const rate = `${fmt(slope)}${units} per decade`;
  if (reg) {
    lines.push(
      `Over the record, ${what} change by about ${rate}. The real rate is likely between ${fmt(reg.slopeCiLow)} and ${fmt(reg.slopeCiHigh)}${units} per decade.`,
    );
    lines.push(
      significant
        ? `If nothing were changing, a trend this strong would turn up by chance in only ${percent(reg.pValue)} of records like this one.`
        : `If nothing were changing, a trend this strong would still turn up by chance in ${percent(reg.pValue)} of records like this one, so the ups and downs could explain it.`,
    );
  } else if (mk) {
    lines.push(`Over the record, ${what} change by about ${rate} (Sen's slope).`);
  }

  if (reg && mk && a.agreement) {
    const second = "A second test, Mann-Kendall, which isn't thrown off by a few extreme months,";
    // With neither test sure, which way each one leans means nothing.
    if (a.agreement.sameSignificance && !significant) {
      more.push(`${second} doesn't find a clear trend either.`);
    } else if (a.agreement.sameSignificance && a.agreement.sameDirection) {
      more.push(`${second} agrees.`);
    } else {
      more.push(`${second} disagrees, so a few unusual months may be driving this.`);
    }
  }

  if (a.shuffle && a.shuffle.permutations > 0) {
    more.push(
      `Shuffling the record ${a.shuffle.permutations.toLocaleString("en-US")} times produced a trend at least this strong ${percent(a.shuffle.pValue)} of the time.`,
    );
  }

  const yts = a.yearsToSignificance;
  if (yts && !yts.alreadySignificant && Number.isFinite(yts.yearsNeeded) && yts.yearsNeeded > 0) {
    lines.push(
      yts.yearsNeeded > 100
        ? `A trend this small would take more than a century of data to tell apart from the natural ups and downs. This record has ${years(yts.yearsSoFar)}.`
        : `Telling a trend this size apart from the natural ups and downs takes about ${years(yts.yearsNeeded)} of data, and this record has ${years(yts.yearsSoFar)}.`,
    );
  }

  const pt = a.pettitt;
  if (pt?.significant && pt.changeTime) {
    more.push(
      `The biggest shift came around ${formatTime(timestampMs(pt.changeTime), TimeStep.MONTHLY)}: the average moved from ${num(pt.meanBefore)} to ${num(pt.meanAfter)}${units}.`,
    );
  }

  return { direction, significant, headline, rate, lines, more, warnings: [...a.warnings] };
}
