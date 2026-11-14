import { DIVERGING, type Heatmap } from "../lib/grid";
import { formatSigned, formatValue } from "../lib/series";

/** Two significant figures, enough for a colour scale's ends. */
const short = (v: number) => Number(v.toPrecision(2));

/**
 * The key to the heatmap drawn on the globe: the colour scale for the trend
 * per decade, how many cells are significant after the false discovery rate
 * correction, and the cell size.
 */
export function HeatmapLegend({ heatmap: h }: { heatmap: Heatmap }) {
  const units = h.units ? ` ${h.units}` : "";
  return (
    <section className="heat-legend" aria-label="Key to the map's colours">
      <p className="heat-title">Where it's changing, on the globe</p>
      <svg className="heat-scale" width="220" height="12" aria-hidden="true">
        <defs>
          <linearGradient id="heat-gradient">
            <stop offset="0%" stopColor={DIVERGING.low} />
            <stop offset="50%" stopColor={DIVERGING.mid} />
            <stop offset="100%" stopColor={DIVERGING.high} />
          </linearGradient>
        </defs>
        <rect width="220" height="12" fill="url(#heat-gradient)" />
      </svg>
      <p className="heat-ends">
        <span>
          {formatSigned(-short(h.max))}
          {units} per decade
        </span>
        <span>0</span>
        <span>
          {formatSigned(short(h.max))}
          {units}
        </span>
      </p>
      <p className="note">
        <span className="heat-outline" aria-hidden="true" /> Outlined cells: {h.significant} of {h.tested} pass the test
        after correcting for testing many cells at once
        {h.significant > 0 ? ` (p up to ${formatValue(short(h.pThreshold))})` : ""}. Each cell averages {h.k} × {h.k} pixels. Point at
        a cell for its numbers.
      </p>
    </section>
  );
}
