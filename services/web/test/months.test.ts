import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { BBoxSchema } from "@gen/common/v1/common_pb";
import { GridSchema } from "@gen/preprocess/v1/data_pb";
import { SeriesSchema } from "@gen/preprocess/v1/data_pb";
import { cellAt, cellBox, cellSeries, monthFrames, quantile, readCube } from "../src/lib/months";

// 2 time steps of a 2 x 2 grid whose last cell never has data.
const grid = create(GridSchema, {
  rows: 2,
  cols: 2,
  units: "°C",
  times: [timestampFromMs(Date.UTC(2020, 0, 1)), timestampFromMs(Date.UTC(2020, 1, 1))],
  latEdges: [30, 25, 20],
  lonEdges: [70, 75, 80],
  cellPixels: [4, 4, 2, 0],
});

function blob(xs: number[]): ArrayBuffer {
  const buf = new ArrayBuffer(xs.length * 4);
  const view = new DataView(buf);
  xs.forEach((x, i) => view.setFloat32(i * 4, x, true));
  return buf;
}

describe("readCube", () => {
  test("reads little-endian float32 in [time][row][col] order", () => {
    const xs = readCube(blob([1, 2, 3, Number.NaN, 5, 6, 7, Number.NaN]), grid);
    expect(Array.from(xs ?? [])).toEqual([1, 2, 3, Number.NaN, 5, 6, 7, Number.NaN]);
  });

  test("a blob of the wrong size is refused", () => {
    expect(readCube(blob([1, 2, 3]), grid)).toBeUndefined();
  });
});

describe("monthFrames", () => {
  const values = readCube(blob([1, 2, 3, Number.NaN, 5, 6, Number.NaN, Number.NaN]), grid)!;
  const anomalies = readCube(blob([-1, 0, 1, Number.NaN, 2, -2, Number.NaN, Number.NaN]), grid)!;
  const f = monthFrames(grid, values, anomalies)!;

  test("one cell per grid cell with pixels, keyed by its index", () => {
    expect(f.cells.features.map((c) => c.id)).toEqual([0, 1, 2]);
    expect(f.cells.features[0]?.geometry.coordinates[0]).toEqual([[70, 25], [75, 25], [75, 30], [70, 30], [70, 25]]);
    expect(f.times).toEqual([Date.UTC(2020, 0, 1), Date.UTC(2020, 1, 1)]);
  });

  test("a cell's number in a month, and nothing where it's missing", () => {
    expect(cellAt(f, "values", 1, 1)).toBe(6);
    expect(cellAt(f, "anomalies", 1, 0)).toBe(2);
    expect(cellAt(f, "values", 1, 2)).toBeUndefined();
  });

  test("colour scales from every frame", () => {
    expect(f.valueRange[0]).toBe(1);
    expect(f.valueRange[1]).toBe(6);
    expect(f.anomalyMax).toBe(2);
  });

  test("cells are cut to the job's rectangle", () => {
    const cut = create(GridSchema, grid);
    cut.bbox = create(BBoxSchema, { west: 71, south: 21, east: 80, north: 30 });
    const frames = monthFrames(cut, values)!;
    expect(frames.cells.features[0]?.geometry.coordinates[0]).toEqual([[71, 25], [75, 25], [75, 30], [71, 30], [71, 25]]);
    expect(frames.cells.features[2]?.geometry.coordinates[0]).toEqual([[71, 21], [75, 21], [75, 25], [71, 25], [71, 21]]);
  });

  test("anomalies of the wrong size are left out", () => {
    expect(monthFrames(grid, values, new Float32Array(3))?.anomalies).toBeUndefined();
  });

  test("values that don't match the grid draw nothing", () => {
    expect(monthFrames(grid, new Float32Array(3))).toBeUndefined();
  });
});

test("quantile skips missing numbers", () => {
  expect(quantile(Float32Array.from([Number.NaN, 3, 1, 2]), 0.5)).toBe(2);
  expect(quantile(Float32Array.from([-5, 1]), 1, true)).toBe(5);
  expect(quantile(Float32Array.from([Number.NaN]), 0.5)).toBeUndefined();
});

describe("one cell", () => {
  const values = readCube(blob([1, 2, 3, Number.NaN, 5, 6, Number.NaN, Number.NaN]), grid)!;
  const anomalies = readCube(blob([-1, 0, 1, Number.NaN, 2, -2, Number.NaN, Number.NaN]), grid)!;
  const f = monthFrames(grid, values, anomalies)!;
  const region = create(SeriesSchema, { variableId: "t2m", units: "°C" });

  test("its box comes from the Grid's edges", () => {
    expect(cellBox(grid, 1)).toEqual({ west: 75, south: 25, east: 80, north: 30 });
  });

  test("its series is that cell's values and anomalies, month by month", () => {
    const s = cellSeries(region, grid, f, 1)!;
    expect(s.values).toEqual([2, 6]);
    expect(s.anomalies).toEqual([0, -2]);
    expect(s.times).toHaveLength(2);
    expect(s).toMatchObject({ variableId: "t2m", units: "°C" });
    expect(cellSeries(region, grid, f, 2)!.values.map(Number.isNaN)).toEqual([false, true]);
  });

  test("a cell outside the grid has none", () => {
    expect(cellSeries(region, grid, f, 9)).toBeUndefined();
  });
});
