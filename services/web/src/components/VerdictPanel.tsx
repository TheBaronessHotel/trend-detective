import type { SeriesAnalysis } from "@gen/analysis/v1/results_pb";
import { verdict } from "../lib/verdict";
import { ShuffleChart, TriangleChart } from "./EvidenceCharts";

// Never colour alone: the direction has its own glyph as well.
const glyphs = { rising: "↗", falling: "↘", flat: "→" } as const;

/** What the trend tests found, in plain words, above the chart. */
export function VerdictPanel({ analysis }: { analysis: SeriesAnalysis }) {
  const v = verdict(analysis);
  if (!v) return null;
  return (
    <section className={v.significant ? "verdict verdict-sure" : "verdict"} aria-label="What the trend tests found">
      <p className="verdict-head">
        <span className="verdict-glyph" aria-hidden="true">
          {v.significant ? glyphs[v.direction] : "~"}
        </span>
        <span className="verdict-headline">{v.headline}</span>
        <span className="verdict-rate">{v.rate}</span>
      </p>
      <ul className="verdict-lines">
        {v.lines.map((l) => (
          <li key={l}>{l}</li>
        ))}
      </ul>
      {(v.more.length > 0 || v.warnings.length > 0 || analysis.shuffle || analysis.trendTriangle) && (
        <details className="verdict-more">
          <summary>Why we think so{v.warnings.length > 0 ? `, and ${v.warnings.length === 1 ? "one caveat" : `${v.warnings.length} caveats`}` : ""}</summary>
          <ul className="verdict-lines">
            {v.more.map((l) => (
              <li key={l}>{l}</li>
            ))}
          </ul>
          {analysis.shuffle && <ShuffleChart shuffle={analysis.shuffle} units={analysis.units} />}
          {analysis.trendTriangle && <TriangleChart t={analysis.trendTriangle} alpha={analysis.alpha || 0.05} units={analysis.units} />}
          {v.warnings.length > 0 && (
            <ul className="verdict-warnings">
              {v.warnings.map((w) => (
                <li key={w}>
                  <span aria-hidden="true">!</span> {w}
                </li>
              ))}
            </ul>
          )}
        </details>
      )}
    </section>
  );
}
