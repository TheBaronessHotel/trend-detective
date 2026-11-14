import { describe, expect, test } from "bun:test";
import { create, fromJson } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { TimeStep } from "@gen/common/v1/common_pb";
import { AnomalyKind, MissingReason, SeriesSchema } from "@gen/preprocess/v1/data_pb";
import {
  anomalyUnits,
  filledRuns,
  formatSigned,
  formatTime,
  formatValue,
  lonePoints,
  missingLabel,
  nearest,
  points,
  summarize,
  valueExtent,
} from "../src/lib/series";

const month = (y: number, m: number) => timestampFromMs(Date.UTC(y, m - 1, 1));

// Six months of 2020: March is missing and filled in for the line, May is
// missing for good.
const series = create(SeriesSchema, {
  variableId: "t2m-merra2",
  units: "°C",
  step: TimeStep.MONTHLY,
  times: [1, 2, 3, 4, 5, 6].map((m) => month(2020, m)),
  values: [1, 2, NaN, 4, NaN, 6],
  displayValues: [1, 2, 3, 4, NaN, 6],
  anomalies: [0.5, -0.5, NaN, 1, NaN, 0],
  coverage: [1, 1, 0, 0.9, 0, 1],
  missing: [
    { index: 2, reason: MissingReason.CLOUD },
    { index: 4, reason: MissingReason.LOW_COVERAGE },
  ],
  normals: [
    { month: 1, mean: 0.5, sd: 1, years: 30, usable: true },
    { month: 2, mean: 2.5, sd: 1, years: 30, usable: false },
  ],
});

describe("points", () => {
  const pts = points(series);

  test("one per step, values where measured", () => {
    expect(pts.map((p) => p.value)).toEqual([1, 2, undefined, 4, undefined, 6]);
    expect(pts[0]?.time).toBe(Date.UTC(2020, 0, 1));
  });

  test("a filled-in value draws the line but isn't a measurement", () => {
    expect(pts[2]).toMatchObject({ value: undefined, line: 3, filled: true, missing: MissingReason.CLOUD });
    expect(pts[4]).toMatchObject({ value: undefined, line: undefined, filled: false, missing: MissingReason.LOW_COVERAGE });
  });

  test("normals by calendar month, only usable ones", () => {
    expect(pts[0]?.normal).toEqual({ mean: 0.5, sd: 1 });
    expect(pts[1]?.normal).toBeUndefined();
  });

  test("a series without the optional arrays still reads", () => {
    const bare = points(create(SeriesSchema, { times: [month(2020, 1)], values: [NaN] }));
    expect(bare[0]).toMatchObject({ value: undefined, line: undefined, filled: false, missing: MissingReason.UNSPECIFIED });
    expect(bare[0]?.anomaly).toBeUndefined();
  });
});

test("filled runs reach the measured points on either side", () => {
  const runs = filledRuns(points(series));
  expect(runs.map((r) => r.map((p) => p.index))).toEqual([[1, 2, 3]]);
});

test("a measured point between gaps is drawn as a dot", () => {
  const pts = points(create(SeriesSchema, { times: [1, 2, 3].map((m) => month(2020, m)), values: [NaN, 5, NaN] }));
  expect(lonePoints(pts).map((p) => p.index)).toEqual([1]);
  // February and April sit next to the filled-in March, so they join the
  // dashed line. June follows the missing May and ends the series: a dot.
  expect(lonePoints(points(series)).map((p) => p.index)).toEqual([5]);
});

test("nearest snaps to the closest step", () => {
  const pts = points(series);
  expect(nearest(pts, Date.UTC(2020, 1, 20))?.index).toBe(2);
  expect(nearest(pts, Date.UTC(2020, 1, 3))?.index).toBe(1);
  expect(nearest(pts, Date.UTC(2019, 0, 1))?.index).toBe(0);
  expect(nearest(pts, Date.UTC(2030, 0, 1))?.index).toBe(5);
  expect(nearest([], 0)).toBeUndefined();
});

test("the value range covers the line and the normal range", () => {
  expect(valueExtent(points(series))).toEqual([-0.5, 6]);
  expect(valueExtent([])).toBeUndefined();
});

test("summary counts", () => {
  expect(summarize(points(series))).toEqual({ steps: 6, measured: 4, missing: 2, filled: 1 });
});

test("formats", () => {
  expect(formatTime(Date.UTC(2021, 2, 1), TimeStep.MONTHLY)).toBe("Mar 2021");
  expect(formatTime(Date.UTC(2021, 2, 3), TimeStep.DAILY)).toBe("3 Mar 2021");
  expect(formatTime(Date.UTC(2023, 8, 1), TimeStep.MONTHLY)).toBe("Sep 2023");
  expect(formatValue(12.3456)).toBe("12.35");
  expect(formatValue(0.000123456)).toBe("0.0001235");
  expect(formatSigned(-0.4)).toBe("−0.4");
  expect(formatSigned(1.2)).toBe("+1.2");
  expect(missingLabel(MissingReason.CLOUD)).toBe("hidden by cloud");
  expect(anomalyUnits(create(SeriesSchema, { units: "mm/day", anomalyKind: AnomalyKind.PERCENT }))).toBe("%");
});

// What preprocess writes: protobuf JSON, with NaN as a string.
test("reads the JSON preprocess writes", () => {
  const s = fromJson(
    SeriesSchema,
    {
      variableId: "precip-imerg",
      units: "mm/day",
      step: "TIME_STEP_MONTHLY",
      times: ["2020-01-01T00:00:00Z", "2020-02-01T00:00:00Z"],
      values: [3.5, "NaN"],
      missing: [{ index: 1, reason: "MISSING_REASON_FILL_VALUE" }],
      someFieldFromLater: true,
    },
    { ignoreUnknownFields: true },
  );
  const pts = points(s);
  expect(pts.map((p) => p.value)).toEqual([3.5, undefined]);
  expect(pts[1]?.missing).toBe(MissingReason.FILL_VALUE);
});
