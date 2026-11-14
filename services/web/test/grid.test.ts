import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { GridAnalysisSchema } from "@gen/analysis/v1/results_pb";
import { VariableSchema } from "@gen/gateway/v1/gateway_pb";
import { GridSchema } from "@gen/preprocess/v1/data_pb";
import { blockSize, heatmap, previewGrid } from "../src/lib/grid";

describe("blockSize mirrors preprocess", () => {
  test.each([
    [32, 32, 4, 4], // the chosen block
    [32, 32, undefined, 4], // else from 8 cells per side
    [33, 20, undefined, 5],
    [12, 12, 8, 4], // at least 3 cells on the shorter side
    [8, 32, undefined, 2],
    [400, 400, 1, 4], // never over 128 per side
    [2, 2, 4, 1],
  ])("%i x %i with block %p gives %i", (rows, cols, block, k) => {
    expect(blockSize(rows, cols, block)).toBe(k);
  });
});

describe("previewGrid", () => {
  const merra = create(VariableSchema, { grid: true, pixelLatDeg: 0.5, pixelLonDeg: 0.625, defaultBlockPixels: 1 });
  const box = { west: 70, east: 80, south: 20, north: 30 };

  test("the catalog default", () => {
    const p = previewGrid(merra, box, undefined);
    expect(p).toMatchObject({ pixels: { rows: 20, cols: 16 }, k: 1, rows: 20, cols: 16 });
    expect(p?.km.ns).toBeCloseTo(55.66, 1);
  });

  test("a chosen block", () => {
    expect(previewGrid(merra, box, 2)).toMatchObject({ k: 2, rows: 10, cols: 8 });
  });

  test("series-only variables have no grid", () => {
    expect(previewGrid(create(VariableSchema, { grid: false }), box, 2)).toBeUndefined();
  });
});

describe("heatmap", () => {
  const grid = create(GridSchema, {
    rows: 2,
    cols: 2,
    blockPixels: 2,
    latEdges: [30, 25, 20],
    lonEdges: [70, 75, 80],
  });
  const analysis = create(GridAnalysisSchema, {
    rows: 2,
    cols: 2,
    units: "°C",
    layers: [
      { test: { test: { case: "mannKendall", value: {} } }, slopesPerDecade: [9, 9, 9, 9], pValues: [1, 1, 1, 1], significant: [false, false, false, false] },
      {
        test: { test: { case: "regression", value: {} } },
        slopesPerDecade: [0.4, -0.2, Number.NaN, 0.1],
        pValues: [0.001, 0.3, 1, 0.04],
        significant: [true, false, false, true],
        pThreshold: 0.04,
      },
    ],
  });

  test("one polygon per cell with a slope, from the regression layer", () => {
    const h = heatmap(grid, analysis);
    expect(h?.tested).toBe(3);
    expect(h?.significant).toBe(2);
    expect(h).toMatchObject({ pThreshold: 0.04, units: "°C", k: 2 });
    const first = h?.cells.features[0];
    expect(first?.properties).toEqual({ slope: 0.4, significant: true, p: 0.001 });
    expect(first?.geometry.coordinates[0]).toEqual([[70, 25], [75, 25], [75, 30], [70, 30], [70, 25]]);
  });

  test("a grid that doesn't match the analysis draws nothing", () => {
    expect(heatmap(create(GridSchema, { rows: 3, cols: 2 }), analysis)).toBeUndefined();
  });
});
