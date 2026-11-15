import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { ShuffleResultSchema, TrendTriangleResultSchema } from "@gen/analysis/v1/results_pb";
import { histogram, labelled, shuffleBins, triangle } from "../src/lib/evidence";

describe("histogram", () => {
  test("equal bins from the least to the greatest, the greatest in the last", () => {
    const bins = histogram([0, 1, 2, 3, 4, Number.NaN], 4);
    expect(bins.map((b) => b.n)).toEqual([1, 1, 1, 2]);
    expect(bins[0]).toMatchObject({ x0: 0, x1: 1 });
    expect(bins[3]).toMatchObject({ x0: 3, x1: 4 });
  });

  test("all the same number is one bin", () => {
    expect(histogram([2, 2, 2])).toEqual([{ x0: 2, x1: 2, n: 3 }]);
  });

  test("nothing finite, no bins", () => {
    expect(histogram([Number.NaN])).toEqual([]);
  });
});

test("too few shuffles draw no histogram", () => {
  const few = create(ShuffleResultSchema, { observedSlopePerDecade: 1, shuffledSlopesPerDecade: [0, 1] });
  expect(shuffleBins(few)).toBeUndefined();
  const many = create(ShuffleResultSchema, { observedSlopePerDecade: 5, shuffledSlopesPerDecade: Array.from({ length: 100 }, (_, i) => i / 100) });
  expect(shuffleBins(many)?.observed).toBe(5);
  expect(shuffleBins(many)?.bins.reduce((s, b) => s + b.n, 0)).toBe(100);
});

describe("triangle", () => {
  // Starts 2000 and 2001, ends 2009 and 2010; 2001 to 2009 is too short.
  const t = create(TrendTriangleResultSchema, {
    minYears: 10,
    startYears: [2000, 2001],
    endYears: [2009, 2010],
    slopesPerDecade: [0.5, 0.4, Number.NaN, -0.2],
    pValues: [0.01, 0.2, Number.NaN, 0.04],
  });

  test("one cell per window long enough, with whether it passes", () => {
    const tri = triangle(t, 0.05);
    expect(tri?.cells).toEqual([
      { start: 2000, end: 2009, slope: 0.5, p: 0.01, passes: true },
      { start: 2000, end: 2010, slope: 0.4, p: 0.2, passes: false },
      { start: 2001, end: 2010, slope: -0.2, p: 0.04, passes: true },
    ]);
    expect(tri?.max).toBe(0.4);
  });

  test("matrices that don't match the years draw nothing", () => {
    expect(triangle(create(TrendTriangleResultSchema, { startYears: [2000], endYears: [2010], slopesPerDecade: [1, 2] }), 0.05)).toBeUndefined();
  });
});

describe("labelled", () => {
  test("every few, ending on the last", () => {
    expect([...labelled(12, 2)]).toEqual([0, 2, 4, 6, 8, 11]);
    expect([...labelled(11, 2)]).toEqual([0, 2, 4, 6, 8, 10]);
    expect([...labelled(3, 5)]).toEqual([0, 2]);
    expect([...labelled(1, 2)]).toEqual([0]);
  });
});
