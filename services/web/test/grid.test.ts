import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { GridAnalysisSchema } from "@gen/analysis/v1/results_pb";
import { BBoxSchema } from "@gen/common/v1/common_pb";
import { VariableSchema } from "@gen/gateway/v1/gateway_pb";
import { GridSchema } from "@gen/preprocess/v1/data_pb";
import { blockSize, cellRing, heatmap, previewGrid } from "../src/lib/grid";

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
    expect(first?.properties).toEqual({ slope: 0.4, significant: true, p: 0.001, cell: 0, months: -1 });
    expect(h).toMatchObject({ method: "regression", methods: ["regression", "mannKendall"] });
    expect(first?.geometry.coordinates[0]).toEqual([[70, 25], [75, 25], [75, 30], [70, 30], [70, 25]]);
  });

  test("edge cells are cut to the job's rectangle", () => {
    // Pixel centers on the rectangle's edges reach half a pixel past it.
    const wide = create(GridSchema, {
      rows: 2,
      cols: 2,
      blockPixels: 1,
      bbox: { west: 70, south: 20, east: 80, north: 30 },
      latEdges: [30.25, 25, 19.75],
      lonEdges: [69.6875, 75, 80.3125],
    });
    const h = heatmap(wide, analysis);
    expect(h?.cut).toBe(true);
    expect(h?.cells.features[0]?.geometry.coordinates[0]).toEqual([[70, 25], [75, 25], [75, 30], [70, 30], [70, 25]]);
    expect(h?.cells.features.at(-1)?.geometry.coordinates[0]).toEqual([[75, 20], [80, 20], [80, 25], [75, 25], [75, 20]]);
    expect(heatmap(grid, analysis)?.cut).toBe(false);
  });

  test("the Mann-Kendall layer, with each cell's months", () => {
    const withMonths = create(GridAnalysisSchema, { ...analysis, validSteps: [40, 38, 0, 12] });
    const h = heatmap(grid, withMonths, "mannKendall");
    expect(h?.method).toBe("mannKendall");
    expect(h?.cells.features.map((f) => f.properties.slope)).toEqual([9, 9, 9, 9]);
    expect(h?.cells.features[1]?.properties.months).toBe(38);
  });

  test("a grid that doesn't match the analysis draws nothing", () => {
    expect(heatmap(create(GridSchema, { rows: 3, cols: 2 }), analysis)).toBeUndefined();
  });
});

describe("cellRing", () => {
  const box = create(BBoxSchema, { west: 0, south: 42, east: 20, north: 55 });

  test("a cell inside the rectangle is left whole", () => {
    expect(cellRing(50, 49.5, 5, 5.625, box)).toEqual([[5, 49.5], [5.625, 49.5], [5.625, 50], [5, 50], [5, 49.5]]);
  });

  test("a cell wholly outside is dropped", () => {
    expect(cellRing(60, 56, 5, 6, box)).toBeUndefined();
  });

  test("a rectangle across the date line cuts nothing", () => {
    const across = create(BBoxSchema, { west: 170, south: 0, east: -170, north: 10 });
    expect(cellRing(10.25, 9.75, 169.7, 170.3, across)?.[0]).toEqual([169.7, 9.75]);
  });
});
