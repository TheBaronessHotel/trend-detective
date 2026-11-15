// The month-by-month map: a finished job's Grid values (or anomalies), one
// frame per time step, drawn on the globe's cells as the slider moves.

import { timestampMs } from "@bufbuild/protobuf/wkt";
import type { TimeStep } from "@gen/common/v1/common_pb";
import { create } from "@bufbuild/protobuf";
import { type Grid, type Series, SeriesSchema } from "@gen/preprocess/v1/data_pb";
import type { Feature, FeatureCollection, Polygon } from "geojson";
import { cellRing } from "./grid";

export type MonthsKind = "values" | "anomalies";

export interface MonthFrames {
  /** One polygon per cell that ever has data; each feature's id is its cell index. */
  cells: FeatureCollection<Polygon>;
  /** Start of each time step, ms since 1970, UTC. */
  times: number[];
  step: TimeStep;
  /** Cells per frame: rows × cols. */
  size: number;
  values: Float32Array;
  /** Unset for a grid without an anomaly step. */
  anomalies?: Float32Array;
  units: string;
  /** The values' colour scale: the 2nd to the 98th percentile of every frame. */
  valueRange: [number, number];
  /** The anomalies' scale runs from -max to +max: the 98th percentile of their sizes. */
  anomalyMax: number;
}

/**
 * Reads a blob of little-endian float32 in [time][row][col] order. undefined
 * when its length doesn't match the grid.
 */
export function readCube(buf: ArrayBuffer, grid: Grid): Float32Array | undefined {
  const want = grid.times.length * grid.rows * grid.cols;
  if (buf.byteLength !== want * 4) return undefined;
  const view = new DataView(buf);
  const out = new Float32Array(want);
  for (let i = 0; i < want; i++) out[i] = view.getFloat32(i * 4, true);
  return out;
}

/** The q-th quantile of the finite numbers in xs, from at most 100,000 of them. */
export function quantile(xs: Float32Array, q: number, abs = false): number | undefined {
  const stride = Math.max(1, Math.floor(xs.length / 100_000));
  const sample: number[] = [];
  for (let i = 0; i < xs.length; i += stride) {
    const x = xs[i] ?? Number.NaN;
    if (Number.isFinite(x)) sample.push(abs ? Math.abs(x) : x);
  }
  if (!sample.length) return undefined;
  sample.sort((a, b) => a - b);
  return sample[Math.min(sample.length - 1, Math.round(q * (sample.length - 1)))];
}

/**
 * The frames for a grid. Cells come from the Grid's exact edges, cut to the
 * job's rectangle like the heatmap's; a cell with no pixels that ever have
 * data is left out.
 */
export function monthFrames(grid: Grid, values: Float32Array, anomalies?: Float32Array): MonthFrames | undefined {
  if (grid.latEdges.length !== grid.rows + 1 || grid.lonEdges.length !== grid.cols + 1) return undefined;
  const size = grid.rows * grid.cols;
  if (!size || values.length !== grid.times.length * size) return undefined;
  const features: Feature<Polygon>[] = [];
  for (let r = 0; r < grid.rows; r++) {
    for (let c = 0; c < grid.cols; c++) {
      const i = r * grid.cols + c;
      if (grid.cellPixels.length === size && !grid.cellPixels[i]) continue;
      // Cut to the rectangle, like the heatmap's cells.
      const ring = cellRing(grid.latEdges[r] ?? 0, grid.latEdges[r + 1] ?? 0, grid.lonEdges[c] ?? 0, grid.lonEdges[c + 1] ?? 0, grid.bbox);
      if (!ring) continue;
      features.push({ type: "Feature", id: i, properties: {}, geometry: { type: "Polygon", coordinates: [ring] } });
    }
  }
  const low = quantile(values, 0.02) ?? 0;
  const high = quantile(values, 0.98) ?? 1;
  const good = anomalies && anomalies.length === values.length ? anomalies : undefined;
  return {
    cells: { type: "FeatureCollection", features },
    times: grid.times.map((t) => timestampMs(t)),
    step: grid.step,
    size,
    values,
    anomalies: good,
    units: grid.units,
    valueRange: high > low ? [low, high] : [low - 1, high + 1],
    anomalyMax: (good && quantile(good, 0.98, true)) || 1,
  };
}

/** One cell's number at time step t, or undefined where it's missing. */
export function cellAt(f: MonthFrames, kind: MonthsKind, t: number, cell: number): number | undefined {
  const data = kind === "anomalies" ? f.anomalies : f.values;
  const v = data?.[t * f.size + cell];
  return v !== undefined && Number.isFinite(v) ? v : undefined;
}

/**
 * The sequential scale for values: one hue, light for low and dark for high
 * on the light map, the other way round on the dark one, so high values stand
 * out on both. Green, so it isn't read as the trend's blue or red.
 */
export const SEQUENTIAL = {
  light: ["#e5f5ee", "#b4e1cc", "#78c7a5", "#3aa57c", "#17805a", "#085a3d"],
  dark: ["#0d2a1f", "#124a36", "#1a7454", "#2ba277", "#6fd0a6", "#c8f0dd"],
};

/** A cell's rectangle, from the Grid's edges, cut to the job's rectangle like its polygon. */
export function cellBox(grid: Grid, cell: number): { west: number; south: number; east: number; north: number } | undefined {
  const r = Math.floor(cell / grid.cols);
  const c = cell % grid.cols;
  const ring = cellRing(grid.latEdges[r] ?? 0, grid.latEdges[r + 1] ?? 0, grid.lonEdges[c] ?? 0, grid.lonEdges[c + 1] ?? 0, grid.bbox);
  if (!ring) return undefined;
  const [sw, , ne] = ring as [number[], number[], number[]];
  return { west: sw[0] ?? 0, south: sw[1] ?? 0, east: ne[0] ?? 0, north: ne[1] ?? 0 };
}

/**
 * One cell's own series, for the chart: its values and anomalies month by
 * month, taken from the frames, with the region's units, step and kind of
 * anomaly. It has no coverage or normals of its own, so the chart leaves
 * those out.
 */
export function cellSeries(region: Series, grid: Grid, f: MonthFrames, cell: number): Series | undefined {
  const box = cellBox(grid, cell);
  if (!box || cell < 0 || cell >= f.size) return undefined;
  const pick = (data: Float32Array | undefined) => f.times.map((_, t) => data?.[t * f.size + cell] ?? Number.NaN);
  return create(SeriesSchema, {
    variableId: region.variableId,
    units: region.units,
    bbox: box,
    source: region.source,
    step: region.step,
    times: grid.times,
    values: pick(f.values),
    anomalies: f.anomalies ? pick(f.anomalies) : [],
    anomalyKind: region.anomalyKind,
  });
}
