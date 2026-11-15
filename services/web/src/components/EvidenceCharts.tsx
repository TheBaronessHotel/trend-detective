import { useMemo, useRef, useState } from "react";
import { scaleLinear } from "d3-scale";
import type { MonthlyTrend, ShuffleResult, TrendTriangleResult } from "@gen/analysis/v1/results_pb";
import { type Bin, labelled, type MonthBar, monthBars, shuffleBins, type TriangleCell, triangle } from "../lib/evidence";
import { DIVERGING } from "../lib/grid";
import { formatSigned } from "../lib/series";
import { useWidth } from "../lib/useWidth";

/** Three significant figures, with a sign. */
const signed = (v: number) => formatSigned(Number(v.toPrecision(3)));

/**
 * The shuffle test as a histogram: the trends of the shuffled records as grey
 * bars, and this record's trend as a black line. Far from the bars means
 * unlikely to be chance.
 */
export function ShuffleChart({ shuffle, units }: { shuffle: ShuffleResult; units: string }) {
  const wrap = useRef<HTMLElement>(null);
  const width = useWidth(wrap) || 480;
  const data = useMemo(() => shuffleBins(shuffle), [shuffle]);
  const [hover, setHover] = useState<Bin>();
  if (!data) return null;

  const u = units ? ` ${units}` : "";
  const height = 120;
  const m = { top: 20, right: 14, bottom: 24, left: 14 };
  const first = data.bins[0]?.x0 ?? 0;
  const last = data.bins.at(-1)?.x1 ?? 0;
  const lo = Math.min(first, data.observed);
  const hi = Math.max(last, data.observed);
  const pad = (hi - lo) * 0.04 || 1;
  const x = scaleLinear()
    .domain([lo - pad, hi + pad])
    .range([m.left, width - m.right]);
  const top = Math.max(...data.bins.map((b) => b.n), 1);
  const y = scaleLinear()
    .domain([0, top])
    .range([height - m.bottom, m.top]);
  const total = data.bins.reduce((s, b) => s + b.n, 0);
  const ox = x(data.observed);
  const labelLeft = ox > width - 110;

  return (
    <figure className="evidence" ref={wrap}>
      <figcaption className="evidence-title">The shuffle test</figcaption>
      <svg width={width} height={height} role="img" aria-label={`Histogram of ${total} shuffled trends, with this record's trend of ${signed(data.observed)}${u} per decade`}>
        {x.ticks(5).map((t) => (
          <text key={t} className="evidence-tick" x={x(t)} y={height - 6} textAnchor="middle">
            {t === 0 ? "0" : signed(t)}
          </text>
        ))}
        {data.bins.map((b) => {
          const bx = x(b.x0);
          const bw = Math.max(1, x(b.x1) - bx - 1);
          return (
            <g key={b.x0} onMouseEnter={() => setHover(b)} onMouseLeave={() => setHover(undefined)}>
              {/* A taller, invisible target, so thin bars are easy to point at. */}
              <rect x={bx} y={m.top} width={bw + 1} height={y(0) - m.top} fill="transparent" />
              {b.n > 0 && <rect className={hover === b ? "shuffle-bar on" : "shuffle-bar"} x={bx} y={y(b.n)} width={bw} height={y(0) - y(b.n)} />}
            </g>
          );
        })}
        <line className="evidence-axis" x1={m.left} x2={width - m.right} y1={y(0)} y2={y(0)} />
        <line className="shuffle-observed" x1={ox} x2={ox} y1={m.top - 8} y2={y(0)} />
        <text className="evidence-label" x={labelLeft ? ox - 6 : ox + 6} y={m.top - 2} textAnchor={labelLeft ? "end" : "start"}>
          This record
        </text>
      </svg>
      <p className="evidence-readout">
        {hover
          ? `${hover.n} of ${total} shuffles gave ${signed(hover.x0)} to ${signed(hover.x1)}${u} per decade.`
          : `This record: ${signed(data.observed)}${u} per decade. Grey: ${total} shuffled versions of it.`}
      </p>
      <p className="note">
        Shuffling the record in blocks of {shuffle.blockLength} steps breaks any trend but keeps the ups and downs. The bars are
        the trends the shuffled records got. The further the line sits from them, the less likely the trend is chance.
      </p>
    </figure>
  );
}

/**
 * The trend over every stretch of years at least minYears long: start year
 * down the side, end year along the bottom, so the whole record is the top
 * right square. Dots where the test passes for that stretch alone.
 */
export function TriangleChart({ t, alpha, units }: { t: TrendTriangleResult; alpha: number; units: string }) {
  const wrap = useRef<HTMLElement>(null);
  const width = useWidth(wrap) || 480;
  const tri = useMemo(() => triangle(t, alpha), [t, alpha]);
  const [hover, setHover] = useState<TriangleCell>();
  if (!tri) return null;

  const u = units ? ` ${units}` : "";
  const m = { top: 6, right: 8, bottom: 22, left: 40 };
  const nS = tri.starts.length;
  const nE = tri.ends.length;
  const s = Math.max(4, Math.min(18, Math.floor((width - m.left - m.right) / nE)));
  const height = m.top + nS * s + m.bottom;
  const svgWidth = m.left + nE * s + m.right;
  const colour = scaleLinear<string>()
    .domain([-tri.max, 0, tri.max])
    .range([DIVERGING.low, DIVERGING.mid, DIVERGING.high])
    .clamp(true);
  const col = new Map(tri.ends.map((e, j) => [e, j]));
  const row = new Map(tri.starts.map((st, i) => [st, i]));
  // Year labels every few years, so they don't collide.
  const every = Math.max(1, Math.ceil(28 / s));
  const startLabels = labelled(nS, every);
  const endLabels = labelled(nE, every);
  const passing = tri.cells.filter((c) => c.passes).length;

  return (
    <figure className="evidence" ref={wrap}>
      <figcaption className="evidence-title">Does it depend on the years picked?</figcaption>
      <svg width={svgWidth} height={height} role="img" aria-label={`Trends over ${tri.cells.length} stretches of years, ${passing} of them passing the test`}>
        {tri.starts.map((st, i) =>
          startLabels.has(i) ? (
            <text key={st} className="evidence-tick" x={m.left - 5} y={m.top + i * s + s / 2 + 3} textAnchor="end">
              {st}
            </text>
          ) : null,
        )}
        {tri.ends.map((e, j) =>
          endLabels.has(j) ? (
            <text key={e} className="evidence-tick" x={m.left + j * s + s / 2} y={height - 6} textAnchor="middle">
              {e}
            </text>
          ) : null,
        )}
        {tri.cells.map((c) => {
          const cx = m.left + (col.get(c.end) ?? 0) * s;
          const cy = m.top + (row.get(c.start) ?? 0) * s;
          return (
            <g key={`${c.start}-${c.end}`} onMouseEnter={() => setHover(c)} onMouseLeave={() => setHover(undefined)}>
              <rect className={hover === c ? "tri-cell on" : "tri-cell"} x={cx} y={cy} width={s - 1} height={s - 1} fill={colour(c.slope)} />
              {c.passes && <circle className="tri-dot" cx={cx + (s - 1) / 2} cy={cy + (s - 1) / 2} r={Math.max(1.2, s * 0.16)} />}
            </g>
          );
        })}
      </svg>
      <p className="evidence-readout">
        {hover
          ? `${hover.start} to ${hover.end}: ${signed(hover.slope)}${u} per decade, ${hover.passes ? "passes the test" : "could be chance"}.`
          : `Start year down the side, end year along the bottom. The top right square is the whole record.`}
      </p>
      <p className="note">
        Each square is the trend over one stretch of at least {tri.minYears} years: blue falling, red rising, grey little
        change. Dots mark the stretches that pass the test on their own, {passing} of {tri.cells.length}. If the colour holds
        across the triangle, the trend doesn't hang on the years picked.
      </p>
    </figure>
  );
}

/**
 * The trend for each month of the year, tested on its own: is it the winters
 * or the summers that changed? Bars that pass are solid and marked with a dot;
 * the rest are pale. Twelve tests at once can turn up one or two by chance.
 */
export function MonthlyChart({ trends, units }: { trends: readonly MonthlyTrend[]; units: string }) {
  const wrap = useRef<HTMLElement>(null);
  const width = useWidth(wrap) || 480;
  const bars = useMemo(() => monthBars(trends), [trends]);
  const [hover, setHover] = useState<MonthBar>();
  if (!bars) return null;

  const u = units ? ` ${units}` : "";
  const height = 140;
  const m = { top: 14, right: 8, bottom: 22, left: 44 };
  const top = Math.max(...bars.map((b) => Math.abs(b.slope)), 1e-9);
  const y = scaleLinear()
    .domain([-top, top])
    .nice()
    .range([height - m.bottom, m.top]);
  const slot = (width - m.left - m.right) / 12;
  const barW = Math.max(4, Math.min(28, slot - 6));
  const cx = (month: number) => m.left + (month - 0.5) * slot;
  const passing = bars.filter((b) => b.passes).length;

  return (
    <figure className="evidence" ref={wrap}>
      <figcaption className="evidence-title">Which months changed?</figcaption>
      <svg width={width} height={height} role="img" aria-label={`The trend for each month of the year; ${passing} of ${bars.length} months pass the test`}>
        {y.ticks(4).map((t) => (
          <g key={t} transform={`translate(0,${y(t)})`}>
            <line className={t === 0 ? "evidence-zero" : "evidence-axis"} x1={m.left} x2={width - m.right} />
            <text className="evidence-tick" x={m.left - 6} dy="0.32em" textAnchor="end">
              {t === 0 ? "0" : signed(t)}
            </text>
          </g>
        ))}
        {bars.map((b) => {
          const y0 = y(0);
          const y1 = y(b.slope);
          const x0 = cx(b.month) - barW / 2;
          return (
            <g key={b.month} onMouseEnter={() => setHover(b)} onMouseLeave={() => setHover(undefined)}>
              <rect x={cx(b.month) - slot / 2} y={m.top} width={slot} height={height - m.top - m.bottom} fill="transparent" />
              <rect
                className={`month-bar ${b.slope >= 0 ? "up" : "down"}${b.passes ? " passes" : ""}${hover === b ? " on" : ""}`}
                x={x0}
                y={Math.min(y0, y1)}
                width={barW}
                height={Math.max(1, Math.abs(y1 - y0))}
              />
              {b.passes && <circle className="tri-dot" cx={cx(b.month)} cy={b.slope >= 0 ? y1 - 6 : y1 + 6} r={2.5} />}
              <text className="evidence-tick" x={cx(b.month)} y={height - 6} textAnchor="middle">
                {b.name.slice(0, 3)}
              </text>
            </g>
          );
        })}
      </svg>
      <p className="evidence-readout">
        {hover
          ? `${hover.name}: ${signed(hover.slope)}${u} per decade, ${hover.passes ? "passes the test" : "could be chance"} (p ${Number(hover.p.toPrecision(2))}).`
          : `Each bar is one month of the year's trend, per decade${u ? `, in${u}` : ""}. ${passing} of ${bars.length} pass the test.`}
      </p>
      <p className="note">
        Each month is tested on its own, against the same month in other years: blue bars falling, red rising, solid ones
        with a dot passing the test. With twelve tests, one or two can pass by chance, so look for a run of neighbouring
        months, like a whole season.
      </p>
    </figure>
  );
}
