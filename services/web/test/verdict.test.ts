import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { SeriesField } from "@gen/analysis/v1/analysis_pb";
import { FittedModelSchema, SeriesAnalysisSchema } from "@gen/analysis/v1/results_pb";
import { trendLine, zFor } from "../src/lib/trend";
import { rounded, verdict } from "../src/lib/verdict";

const rising = create(SeriesAnalysisSchema, {
  units: "°C",
  field: SeriesField.ANOMALIES,
  nTotal: 360,
  regression: { slopePerDecade: 0.25, slopeCiLow: 0.12, slopeCiHigh: 0.38, pValue: 0.0004, significant: true },
  mannKendall: { sensSlopePerDecade: 0.24, pValue: 0.001, significant: true },
  agreement: { sameDirection: true, sameSignificance: true },
  shuffle: { permutations: 1000, pValue: 0.002 },
  yearsToSignificance: { yearsNeeded: 14.2, yearsSoFar: 30, alreadySignificant: true },
  pettitt: { significant: true, changeTime: timestampFromMs(Date.UTC(2015, 2, 1)), meanBefore: -0.2, meanAfter: 0.3 },
  warnings: ["Only 8 years of data."],
});

describe("verdict", () => {
  test("a clear rising trend", () => {
    const v = verdict(rising);
    expect(v).toMatchObject({ direction: "rising", significant: true, headline: "Rising, and unlikely to be chance", rate: "+0.25 °C per decade" });
    expect(v?.lines[0]).toContain("between +0.12 and +0.38 °C per decade");
    expect(v?.lines[1]).toContain("less than 0.1%");
    expect(v?.more.some((l) => l.includes("agrees"))).toBe(true);
    expect(v?.more.some((l) => l.includes("1,000 times") && l.includes("0.2%"))).toBe(true);
    expect(v?.more.some((l) => l.includes("around Mar 2015") && l.includes("−0.2 to 0.3 °C"))).toBe(true);
    // Already significant, so no "years needed" line.
    expect(v?.lines.some((l) => l.includes("takes about"))).toBe(false);
    expect(v?.warnings).toEqual(["Only 8 years of data."]);
  });

  test("not significant says it could be chance and how long it would take", () => {
    const v = verdict(
      create(SeriesAnalysisSchema, {
        units: "mm/day",
        regression: { slopePerDecade: -0.05, slopeCiLow: -0.2, slopeCiHigh: 0.1, pValue: 0.41, significant: false },
        mannKendall: { sensSlopePerDecade: 0.01, pValue: 0.6, significant: false },
        agreement: { sameDirection: false, sameSignificance: true },
        yearsToSignificance: { yearsNeeded: 62.4, yearsSoFar: 22, alreadySignificant: false },
      }),
    );
    expect(v).toMatchObject({ direction: "falling", significant: false, headline: "No clear trend yet" });
    expect(v?.lines[1]).toContain("41%");
    // Neither test is sure, so leaning different ways isn't a disagreement.
    expect(v?.more.some((l) => l.includes("doesn't find a clear trend either"))).toBe(true);
    expect(v?.lines.some((l) => l.includes("about 62 years") && l.includes("22 years"))).toBe(true);
  });

  test("a tiny trend doesn't promise a thousand years", () => {
    const v = verdict(
      create(SeriesAnalysisSchema, {
        regression: { slopePerDecade: 0.001, slopeCiLow: -0.1, slopeCiHigh: 0.1, pValue: 0.98, significant: false },
        yearsToSignificance: { yearsNeeded: 1118, yearsSoFar: 45, alreadySignificant: false },
      }),
    );
    expect(v?.lines.some((l) => l.includes("more than a century") && l.includes("45 years"))).toBe(true);
  });

  test("the tests disagree when only one is sure", () => {
    const v = verdict(
      create(SeriesAnalysisSchema, {
        regression: { slopePerDecade: 0.3, slopeCiLow: 0.1, slopeCiHigh: 0.5, pValue: 0.01, significant: true },
        mannKendall: { sensSlopePerDecade: 0.1, pValue: 0.2, significant: false },
        agreement: { sameDirection: true, sameSignificance: false },
      }),
    );
    expect(v?.more.some((l) => l.includes("disagrees"))).toBe(true);
  });

  test("rates are rounded to what their range supports", () => {
    expect(rounded(-0.001368, 0.1)).toBe("0.00");
    expect(rounded(0.2534, 0.13)).toBe("+0.25");
    expect(rounded(-12.64, 4)).toBe("−12.6");
    expect(rounded(-12.6, 40)).toBe("−13");
    expect(rounded(0.004567, 0.002)).toBe("+0.0046");
  });

  test("a passing test on a short record isn't called a trend", () => {
    const v = verdict(create(SeriesAnalysisSchema, { ...rising, nTotal: 24 }));
    expect(v).toMatchObject({ significant: true, short: true, headline: "Rising over 2 years, too short to call a trend" });
    expect(v?.lines[0]).toContain("2 years is a short record");
    expect(v?.lines[0]).toContain("at least 10 years");
    // Ten years or more is a normal verdict.
    expect(verdict(create(SeriesAnalysisSchema, { ...rising, nTotal: 120 }))?.short).toBe(false);
  });

  test("nothing to say without a trend test", () => {
    expect(verdict(create(SeriesAnalysisSchema, {}))).toBeUndefined();
  });
});

describe("trend line", () => {
  test("z for common alphas", () => {
    expect(zFor(0.05)).toBeCloseTo(1.959964, 5);
    expect(zFor(0.01)).toBeCloseTo(2.575829, 5);
    expect(zFor(0.1)).toBeCloseTo(1.644854, 5);
  });

  test("the band is narrowest at the origin and widens away from it", () => {
    const origin = Date.UTC(2010, 0, 1);
    const model = create(FittedModelSchema, {
      timeOrigin: timestampFromMs(origin),
      intercept: 1,
      interceptStderr: 0.1,
      slope: 0.02,
      slopeStderr: 0.005,
    });
    const year = 365.2425 * 86_400_000;
    const [before, at, after] = trendLine(model, [origin - 10 * year, origin, origin + 10 * year]);
    expect(at?.fit).toBeCloseTo(1, 9);
    expect(after?.fit).toBeCloseTo(1.2, 9);
    expect(at!.high - at!.low).toBeCloseTo(2 * 1.959964 * 0.1, 4);
    expect(after!.high - after!.low).toBeGreaterThan(at!.high - at!.low);
    expect(before!.high - before!.low).toBeCloseTo(after!.high - after!.low, 9);
  });
});
