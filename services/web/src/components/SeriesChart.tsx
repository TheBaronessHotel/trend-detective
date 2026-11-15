import { useMemo, useRef, useState } from "react";
import { scaleLinear, scaleUtc } from "d3-scale";
import { area, line } from "d3-shape";
import { TimeStep } from "@gen/common/v1/common_pb";
import type { Series } from "@gen/preprocess/v1/data_pb";
import { SeriesField } from "@gen/analysis/v1/analysis_pb";
import type { SeriesAnalysis } from "@gen/analysis/v1/results_pb";
import { describe } from "../lib/bbox";
import {
  anomalyUnits,
  filledRuns,
  formatCoverage,
  formatSigned,
  formatTime,
  formatValue,
  lonePoints,
  missingLabel,
  nearest,
  type Point,
  points,
  summarize,
  valueExtent,
} from "../lib/series";
import { stepLabel } from "../lib/sources";
import { type TrendPoint, trendLine } from "../lib/trend";
import { useWidth } from "../lib/useWidth";

interface Props {
  series: Series;
  /** The trend tests, for the fitted line and its band. */
  analysis?: SeriesAnalysis;
  /** The variable's name. */
  title: string;
  /** Buttons for the chart's header row, such as saving the data. */
  actions?: React.ReactNode;
}

const margin = { top: 12, right: 16, bottom: 26, left: 52 };
const valuesHeight = 170;
const anomaliesHeight = 100;
const panelGap = 30;
const tipWidth = 240;

/** A bar from the zero line to v, rounded only at its data end, as the chart specs ask. */
function barPath(x: number, w: number, zero: number, end: number): string {
  const r = Math.min(4, w / 2, Math.abs(end - zero));
  if (r < 1) return `M${x},${zero}V${end}H${x + w}V${zero}Z`;
  const up = end < zero;
  const s = up ? 1 : -1;
  return [
    `M${x},${zero}`,
    `V${end + s * r}`,
    `Q${x},${end} ${x + r},${end}`,
    `H${x + w - r}`,
    `Q${x + w},${end} ${x + w},${end + s * r}`,
    `V${zero}Z`,
  ].join("");
}

function stepNoun(step: TimeStep, n: number): string {
  const one = step === TimeStep.MONTHLY ? "month" : step === TimeStep.WEEKLY ? "week" : step === TimeStep.DAILY ? "day" : "step";
  return n === 1 ? one : `${one}s`;
}

function Tip({ p, series, left }: { p: Point; series: Series; left: number }) {
  return (
    <div className="chart-tip" style={{ left, width: tipWidth }}>
      <div className="tip-muted">{formatTime(p.time, series.step)}</div>
      {p.value !== undefined ? (
        <div>
          <strong>{formatValue(p.value)}</strong> {series.units}
        </div>
      ) : (
        <div>
          <strong>No data</strong> <span className="tip-muted">· {missingLabel(p.missing ?? 0)}</span>
        </div>
      )}
      {p.filled && p.line !== undefined && (
        <div className="tip-muted">The line is filled in here at {formatValue(p.line)}, only for drawing.</div>
      )}
      {p.anomaly !== undefined && (
        <div>
          <strong>{formatSigned(p.anomaly)}</strong> <span className="tip-muted">{anomalyUnits(series)} against normal</span>
        </div>
      )}
      {p.normal && (
        <div className="tip-muted">
          Normal {formatValue(p.normal.mean)} ± {formatValue(p.normal.sd)}
        </div>
      )}
      {p.coverage !== undefined && p.coverage < 1 && (
        <div className="tip-muted">{formatCoverage(p.coverage)} of the rectangle had data</div>
      )}
    </div>
  );
}

function Table({ pts, series }: { pts: Point[]; series: Series }) {
  const anomalies = pts.some((p) => p.anomaly !== undefined);
  // Full coverage everywhere, as from a single point, says nothing.
  const coverage = pts.some((p) => p.coverage !== undefined && p.coverage < 1);
  return (
    <div className="chart-table-wrap">
      <table className="chart-table">
        <thead>
          <tr>
            <th scope="col">When</th>
            <th scope="col">Value, {series.units}</th>
            {anomalies && <th scope="col">Against normal, {anomalyUnits(series)}</th>}
            {coverage && <th scope="col">Area with data</th>}
            <th scope="col">Note</th>
          </tr>
        </thead>
        <tbody>
          {pts.map((p) => (
            <tr key={p.index}>
              <th scope="row">{formatTime(p.time, series.step)}</th>
              <td>{p.value === undefined ? "–" : formatValue(p.value)}</td>
              {anomalies && <td>{p.anomaly === undefined ? "–" : formatSigned(p.anomaly)}</td>}
              {coverage && <td>{p.coverage === undefined ? "–" : formatCoverage(p.coverage)}</td>}
              <td>
                {p.value === undefined ? missingLabel(p.missing ?? 0) : ""}
                {p.filled ? "; filled in on the chart" : ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * The region's series: the values as a line, short gaps filled in dashed,
 * missing steps ticked along the bottom, the normal range as a band, and
 * anomalies as bars beneath, whichever the series has. A crosshair reads any
 * step, by pointer or arrow keys, and the table lists every value.
 */
export function SeriesChart({ series, analysis, title, actions }: Props) {
  const wrap = useRef<HTMLDivElement>(null);
  const width = useWidth(wrap) || 640;
  const [hover, setHover] = useState<number>();
  const [tableOpen, setTableOpen] = useState(false);

  const pts = useMemo(() => points(series), [series]);
  const summary = summarize(pts);
  const extent = valueExtent(pts);
  const hasAnomalies = pts.some((p) => p.anomaly !== undefined);
  const hasNormals = pts.some((p) => p.normal);
  // The trend is drawn on whichever series analysis tested: anomalies by
  // default, so it goes on the lower panel, which then shows even without bars.
  const model = analysis?.regression?.model;
  const trendOn = !model ? undefined : analysis?.field === SeriesField.VALUES ? "values" : "anomalies";
  const confidence = Math.round((1 - (analysis?.alpha || 0.05)) * 100);
  const trend = useMemo(
    () => (model ? trendLine(model, pts.map((p) => p.time), analysis?.alpha || 0.05) : []),
    [model, pts, analysis?.alpha],
  );
  const showAnomalies = hasAnomalies || trendOn === "anomalies";
  const first = pts[0];
  const last = pts.at(-1);

  const head = (
    <figcaption className="chart-head">
      <span className="chart-title">{title}</span>
      <span className="chart-meta">
        {[
          series.units,
          stepLabel(series.step),
          series.bbox ? describe(series.bbox) : "",
          first && last ? `${formatTime(first.time, series.step)} to ${formatTime(last.time, series.step)}` : "",
        ]
          .filter(Boolean)
          .join(" · ")}
      </span>
      {actions && <span className="chart-actions">{actions}</span>}
    </figcaption>
  );

  if (!first || !last || !extent) {
    return (
      <figure className="chart">
        {head}
        <p className="note">{pts.length === 0 ? "The series came back empty." : "No step in this series has data."}</p>
      </figure>
    );
  }

  const innerWidth = Math.max(120, width - margin.left - margin.right);
  const anomaliesTop = valuesHeight + panelGap;
  const innerHeight = showAnomalies ? anomaliesTop + anomaliesHeight : valuesHeight;
  const height = margin.top + innerHeight + margin.bottom;

  // A single step gets a month either side, so it sits in the middle.
  const span = last.time - first.time || 30 * 86_400_000;
  const x = scaleUtc()
    .domain(last.time > first.time ? [first.time, last.time] : [first.time - span, last.time + span])
    .range([0, innerWidth]);
  const onValues = trendOn === "values" ? trend : [];
  const lo = Math.min(extent[0], ...onValues.map((t) => t.low));
  const hi = Math.max(extent[1], ...onValues.map((t) => t.high));
  const pad = lo === hi ? Math.abs(lo) * 0.1 || 1 : 0;
  const y = scaleLinear()
    .domain([lo - pad, hi + pad])
    .nice()
    .range([valuesHeight, 0]);

  const onAnomalies = trendOn === "anomalies" ? trend : [];
  const maxAnomaly =
    Math.max(...pts.map((p) => Math.abs(p.anomaly ?? 0)), ...onAnomalies.map((t) => Math.max(Math.abs(t.low), Math.abs(t.high)))) || 1;
  const ya = scaleLinear()
    .domain([-maxAnomaly, maxAnomaly])
    .nice()
    .range([anomaliesTop + anomaliesHeight, anomaliesTop]);

  const px = (p: Point) => x(p.time);
  const solid = line<Point>()
    .defined((p) => p.value !== undefined)
    .x(px)
    .y((p) => y(p.value ?? 0))(pts);
  const dashed = filledRuns(pts).map((run) =>
    line<Point>()
      .x(px)
      .y((p) => y(p.line ?? 0))(run),
  );
  const band = hasNormals
    ? area<Point>()
        .defined((p) => !!p.normal)
        .x(px)
        .y0((p) => y((p.normal?.mean ?? 0) - (p.normal?.sd ?? 0)))
        .y1((p) => y((p.normal?.mean ?? 0) + (p.normal?.sd ?? 0)))(pts)
    : null;
  const ty = trendOn === "values" ? y : ya;
  const tx = (t: TrendPoint) => x(t.time);
  const trendPath = trend.length ? line<TrendPoint>().x(tx).y((t) => ty(t.fit))(trend) : null;
  const trendBand = trend.length
    ? area<TrendPoint>()
        .x(tx)
        .y0((t) => ty(t.low))
        .y1((t) => ty(t.high))(trend)
    : null;
  const dots = lonePoints(pts);
  const missing = pts.filter((p) => p.value === undefined);

  // Bars fill their step but never touch, and never grow past 24px.
  const stepWidth = innerWidth / Math.max(1, pts.length - 1);
  const barWidth = Math.max(1, Math.min(24, stepWidth - 2));

  const xTicks = x.ticks(Math.max(2, Math.floor(innerWidth / 90)));
  const xFormat = x.tickFormat();
  const yTicks = y.ticks(5);
  const yFormat = y.tickFormat(5);

  const hovered = hover === undefined ? undefined : pts[hover];
  const hx = hovered ? px(hovered) : 0;
  const tipLeft = margin.left + hx + 14 + tipWidth > width ? margin.left + hx - 14 - tipWidth : margin.left + hx + 14;

  function onPointerMove(e: React.PointerEvent<SVGRectElement>) {
    const box = e.currentTarget.getBoundingClientRect();
    setHover(nearest(pts, x.invert(e.clientX - box.left).getTime())?.index);
  }

  function onKeyDown(e: React.KeyboardEvent<SVGSVGElement>) {
    const lastIndex = pts.length - 1;
    const current = hover ?? lastIndex;
    const next: Record<string, number> = {
      ArrowLeft: Math.max(0, current - 1),
      ArrowRight: Math.min(lastIndex, current + 1),
      Home: 0,
      End: lastIndex,
    };
    if (e.key in next) {
      e.preventDefault();
      setHover(next[e.key]);
    } else if (e.key === "Escape") {
      setHover(undefined);
    }
  }

  const label =
    `${title}, ${stepLabel(series.step)} values from ${formatTime(first.time, series.step)} to ` +
    `${formatTime(last.time, series.step)}, between ${formatValue(lo)} and ${formatValue(hi)} ${series.units}. ` +
    "Use the left and right arrow keys to read each step, or open the table.";

  return (
    <figure className="chart">
      {head}
      <div className="chart-plot" ref={wrap}>
        <svg
          width={width}
          height={height}
          role="img"
          aria-label={label}
          tabIndex={0}
          onKeyDown={onKeyDown}
          onFocus={() => setHover((h) => h ?? pts.length - 1)}
          onBlur={() => setHover(undefined)}
        >
          <g transform={`translate(${margin.left},${margin.top})`}>
            {yTicks.map((t) => (
              <g key={t} transform={`translate(0,${y(t)})`}>
                <line className="grid" x2={innerWidth} />
                <text className="axis-text" x={-8} dy="0.32em" textAnchor="end">
                  {yFormat(t)}
                </text>
              </g>
            ))}
            {band && <path className="band" d={band} />}
            {solid && <path className="line" d={solid} />}
            {dashed.map((d, i) => d && <path key={i} className="line filled" d={d} />)}
            {dots.map((p) => (
              <circle key={p.index} className="dot" cx={px(p)} cy={y(p.value ?? 0)} r={4} />
            ))}
            {missing.map((p) => (
              <line key={p.index} className="missing" x1={px(p)} x2={px(p)} y1={valuesHeight - 7} y2={valuesHeight} />
            ))}

            {trendOn === "values" && trendBand && <path className="trend-band" d={trendBand} />}
            {trendOn === "values" && trendPath && <path className="trend-line" d={trendPath} />}

            {showAnomalies && (
              <g>
                <text className="axis-text" x={0} y={anomaliesTop - 10}>
                  {hasAnomalies ? `Against normal, ${anomalyUnits(series)}` : `Trend with the seasons taken out, ${analysis?.units ?? series.units}`}
                </text>
                {ya.ticks(2).map((t) => (
                  <g key={t} transform={`translate(0,${ya(t)})`}>
                    <line className={t === 0 ? "zero" : "grid"} x2={innerWidth} />
                    <text className="axis-text" x={-8} dy="0.32em" textAnchor="end">
                      {t === 0 ? "0" : formatSigned(t)}
                    </text>
                  </g>
                ))}
                {pts.map(
                  (p) =>
                    p.anomaly !== undefined &&
                    p.anomaly !== 0 && (
                      <path key={p.index} className="bar" d={barPath(px(p) - barWidth / 2, barWidth, ya(0), ya(p.anomaly))} />
                    ),
                )}
                {trendOn === "anomalies" && trendBand && <path className="trend-band" d={trendBand} />}
                {trendOn === "anomalies" && trendPath && <path className="trend-line" d={trendPath} />}
              </g>
            )}

            <g transform={`translate(0,${innerHeight})`}>
              <line className="grid" x2={innerWidth} />
              {xTicks.map((t) => (
                <text key={t.getTime()} className="axis-text" x={x(t)} y={18} textAnchor="middle">
                  {xFormat(t)}
                </text>
              ))}
            </g>

            {hovered && (
              <g pointerEvents="none">
                <line className="crosshair" x1={hx} x2={hx} y1={0} y2={innerHeight} />
                {hovered.line !== undefined && <circle className="dot" cx={hx} cy={y(hovered.line)} r={4} />}
              </g>
            )}
            <rect
              className="hit"
              width={innerWidth}
              height={innerHeight}
              onPointerMove={onPointerMove}
              onPointerLeave={() => setHover(undefined)}
            />
          </g>
        </svg>
        {hovered && <Tip p={hovered} series={series} left={tipLeft} />}
      </div>

      <ul className="chart-key">
        <li>
          {summary.measured} of {summary.steps} {stepNoun(series.step, summary.steps)} have data
        </li>
        {summary.filled > 0 && (
          <li>
            <svg className="key-mark" width="22" height="8" aria-hidden="true">
              <line className="line filled" x1="1" x2="21" y1="4" y2="4" />
            </svg>
            filled in for drawing, not measured
          </li>
        )}
        {hasNormals && (
          <li>
            <svg className="key-mark" width="14" height="10" aria-hidden="true">
              <rect className="band" width="14" height="10" />
            </svg>
            normal range, ± one standard deviation
          </li>
        )}
        {trend.length > 0 && (
          <li>
            <svg className="key-mark" width="22" height="10" aria-hidden="true">
              <rect className="trend-band" width="22" height="10" />
              <line className="trend-line" x1="1" x2="21" y1="5" y2="5" />
            </svg>
            trend, and the {confidence}% band where it likely lies
          </li>
        )}
        {summary.missing > 0 && (
          <li>
            <svg className="key-mark" width="6" height="10" aria-hidden="true">
              <line className="missing" x1="3" x2="3" y1="1" y2="9" />
            </svg>
            no data
          </li>
        )}
      </ul>

      <details className="chart-details" onToggle={(e) => setTableOpen(e.currentTarget.open)}>
        <summary>Show the numbers as a table</summary>
        {tableOpen && <Table pts={pts} series={series} />}
      </details>
    </figure>
  );
}
