// The map grid: previewing its cells before a job, and turning a finished
// job's Grid and GridAnalysis into polygons for the heatmap.

import type { GridAnalysis, GridLayer } from "@gen/analysis/v1/results_pb";
import type { BBox } from "@gen/common/v1/common_pb";
import type { Variable } from "@gen/gateway/v1/gateway_pb";
import type { Grid } from "@gen/preprocess/v1/data_pb";
import type { Feature, FeatureCollection, Polygon } from "geojson";
import type { Box } from "./bbox";

/** Preprocess's defaults; the gateway sends the same. */
export const SUBREGION_N = 8;
export const MAX_GRID = 128;
const MIN_CELLS = 3;
const KM_PER_DEGREE = 111.32;

/**
 * The block side k preprocess will use, mirroring its block_size: the chosen
 * block, else ceil(longer side / subregionN), else 1; then at least 3 cells on
 * the shorter side, and within maxGrid.
 */
export function blockSize(rows: number, cols: number, block: number | undefined, subregionN = SUBREGION_N, maxGrid = MAX_GRID): number {
  const longer = Math.max(rows, cols);
  const shorter = Math.min(rows, cols);
  let k = block && block > 0 ? block : subregionN > 0 ? Math.ceil(longer / subregionN) : 1;
  k = Math.min(k, Math.max(1, Math.floor(shorter / MIN_CELLS)));
  if (maxGrid > 0) k = Math.max(k, Math.ceil(longer / maxGrid));
  return Math.max(k, 1);
}

export interface GridPreview {
  /** Native pixels the rectangle covers. */
  pixels: { rows: number; cols: number };
  /** The block side preprocess will use. */
  k: number;
  rows: number;
  cols: number;
  /** A cell's size on the ground, at the rectangle's middle latitude. */
  km: { ns: number; ew: number };
}

/**
 * What a job's map grid will look like, before it runs. Pixel counts are
 * estimates from the variable's native pixel size; the Grid has the exact
 * ones afterwards. undefined for series-only variables.
 */
export function previewGrid(v: Variable, box: Box, block: number | undefined): GridPreview | undefined {
  if (!v.grid || v.pixelLatDeg <= 0 || v.pixelLonDeg <= 0) return undefined;
  const rows = Math.max(1, Math.round((box.north - box.south) / v.pixelLatDeg));
  const cols = Math.max(1, Math.round((box.east - box.west) / v.pixelLonDeg));
  const k = blockSize(rows, cols, block ?? (v.defaultBlockPixels || undefined));
  const mid = ((box.north + box.south) / 2) * (Math.PI / 180);
  return {
    pixels: { rows, cols },
    k,
    rows: Math.ceil(rows / k),
    cols: Math.ceil(cols / k),
    km: {
      ns: k * v.pixelLatDeg * KM_PER_DEGREE,
      ew: k * v.pixelLonDeg * KM_PER_DEGREE * Math.cos(mid),
    },
  };
}

/** The layer the heatmap shows: regression's when it ran, else the first. */
export function heatmapLayer(a: GridAnalysis): GridLayer | undefined {
  return a.layers.find((l) => l.test?.test.case === "regression") ?? a.layers[0];
}

export interface CellProps {
  slope: number;
  significant: boolean;
  p: number;
}

/**
 * A cell's ring from its edges, cut to the rectangle the job asked for.
 * Pixels whose centers sit on the rectangle's edges reach half a pixel past
 * it, so edge cells are drawn cut, inside the rectangle the user drew. Their
 * numbers still come from the whole pixels. A rectangle crossing the date
 * line (west above east) isn't cut. undefined when nothing is left.
 */
export function cellRing(north: number, south: number, west: number, east: number, bbox?: BBox): number[][] | undefined {
  let [n, s, w, e] = [north, south, west, east];
  if (bbox && bbox.east > bbox.west && bbox.north > bbox.south) {
    n = Math.min(n, bbox.north);
    s = Math.max(s, bbox.south);
    w = Math.max(w, bbox.west);
    e = Math.min(e, bbox.east);
  }
  if (!(n > s && e > w)) return undefined;
  return [
    [w, s],
    [e, s],
    [e, n],
    [w, n],
    [w, s],
  ];
}

/** Whether a cell reaches past the rectangle, so it's drawn cut. */
function reachesPast(north: number, south: number, west: number, east: number, bbox?: BBox): boolean {
  if (!bbox || !(bbox.east > bbox.west && bbox.north > bbox.south)) return false;
  return north > bbox.north || south < bbox.south || west < bbox.west || east > bbox.east;
}

export interface Heatmap {
  cells: FeatureCollection<Polygon, CellProps>;
  /** Some edge cells reach past the rectangle and are drawn cut to it. */
  cut: boolean;
  /** The colour scale runs from -max to +max. */
  max: number;
  tested: number;
  significant: number;
  pThreshold: number;
  units: string;
  k: number;
}

/**
 * One polygon per cell that has a slope, from the Grid's exact edges. The
 * colour scale is symmetric around zero, out to the 95th percentile of the
 * slopes' sizes, so one extreme cell doesn't wash out the rest.
 */
export function heatmap(grid: Grid, analysis: GridAnalysis): Heatmap | undefined {
  const layer = heatmapLayer(analysis);
  if (!layer || grid.rows !== analysis.rows || grid.cols !== analysis.cols) return undefined;
  if (grid.latEdges.length !== grid.rows + 1 || grid.lonEdges.length !== grid.cols + 1) return undefined;
  const features: Feature<Polygon, CellProps>[] = [];
  let cut = false;
  for (let r = 0; r < grid.rows; r++) {
    for (let c = 0; c < grid.cols; c++) {
      const i = r * grid.cols + c;
      const slope = layer.slopesPerDecade[i];
      if (slope === undefined || !Number.isFinite(slope)) continue;
      const n = grid.latEdges[r] ?? 0;
      const s = grid.latEdges[r + 1] ?? 0;
      const w = grid.lonEdges[c] ?? 0;
      const e = grid.lonEdges[c + 1] ?? 0;
      const ring = cellRing(n, s, w, e, grid.bbox);
      if (!ring) continue;
      cut ||= reachesPast(n, s, w, e, grid.bbox);
      features.push({
        type: "Feature",
        properties: { slope, significant: !!layer.significant[i], p: layer.pValues[i] ?? 1 },
        geometry: { type: "Polygon", coordinates: [ring] },
      });
    }
  }
  const sizes = features.map((f) => Math.abs(f.properties.slope)).sort((a, b) => a - b);
  const max = sizes.length ? sizes[Math.min(sizes.length - 1, Math.floor(0.95 * (sizes.length - 1)))] || sizes.at(-1) || 1 : 1;
  return {
    cells: { type: "FeatureCollection", features },
    cut,
    max,
    tested: features.length,
    significant: features.filter((f) => f.properties.significant).length,
    pThreshold: layer.pThreshold,
    units: analysis.units,
    k: grid.blockPixels || 1,
  };
}

/** The diverging scale: blue for falling, grey for none, red for rising. */
export const DIVERGING = { low: "#2166ac", mid: "#d9d9d9", high: "#b2182b" };
