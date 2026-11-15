import { DIVERGING, type Heatmap, type Method } from "../lib/grid";
import { formatSigned, formatValue } from "../lib/series";

/** Two significant figures, enough for a colour scale's ends. */
const short = (v: number) => Number(v.toPrecision(2));

const METHOD_NAMES: Record<Method, string> = { regression: "Least squares", mannKendall: "Mann-Kendall" };

/** What each method is, in a sentence, under the switch. */
const METHOD_NOTES: Record<Method, string> = {
  regression: "Colours are each cell's least-squares trend.",
  mannKendall:
    "Colours are each cell's Sen's slope, and the test is Mann-Kendall's, which doesn't need the ups and downs to be bell-shaped and isn't pulled by one extreme month.",
};

/**
 * The key to the heatmap drawn on the globe: the colour scale for the trend
 * per decade, which test it comes from, how many cells are significant after
 * the false discovery rate correction, and the cell size.
 */
export function HeatmapLegend({ heatmap: h, onMethod }: { heatmap: Heatmap; onMethod?: (m: Method) => void }) {
  const units = h.units ? ` ${h.units}` : "";
  return (
    <section className="heat-legend" aria-label="Key to the map's colours">
      <p className="heat-title">Where it's changing, on the globe</p>
      {h.methods.length > 1 && onMethod && (
        <div className="heat-methods" role="group" aria-label="Which trend test the map shows">
          {h.methods.map((m) => (
            <button key={m} type="button" className="action small" aria-pressed={h.method === m} onClick={() => onMethod(m)}>
              {METHOD_NAMES[m]}
            </button>
          ))}
        </div>
      )}
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
        {h.significant > 0 ? ` (p up to ${formatValue(short(h.pThreshold))})` : ""}. Each cell averages {h.k} × {h.k} pixels.
        {h.cut ? " Cells on the edge are drawn cut to your rectangle, but their numbers come from whole NASA pixels, which reach a little past it." : ""}{" "}
        {h.methods.length > 1 ? `${METHOD_NOTES[h.method]} ` : ""}Point at a cell for its numbers, or click it for its own chart.
      </p>
    </section>
  );
}
