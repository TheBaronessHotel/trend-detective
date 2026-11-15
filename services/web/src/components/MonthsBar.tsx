import { type Dispatch, type SetStateAction, useEffect, useState } from "react";
import { DIVERGING } from "../lib/grid";
import { type MonthFrames, type MonthsKind, SEQUENTIAL } from "../lib/months";
import { formatSigned, formatTime, formatValue } from "../lib/series";
import { useDarkTheme } from "../lib/useDarkTheme";

/** Two significant figures, enough for a colour scale's ends. */
const short = (v: number) => Number(v.toPrecision(2));
/** With a real minus sign. */
const plain = (v: number) => (v < 0 ? `−${formatValue(-v)}` : formatValue(v));

/** Milliseconds per frame at each speed. */
const SPEEDS = [
  { label: "Slow", ms: 400 },
  { label: "Normal", ms: 180 },
  { label: "Fast", ms: 70 },
];

interface Props {
  /** The months, once downloaded. */
  frames: MonthFrames | undefined;
  loading: boolean;
  error: string | undefined;
  /** Whether the map shows the months rather than the trend. */
  on: boolean;
  kind: MonthsKind;
  t: number;
  /** False when there's no trend map to go back to. */
  canClose: boolean;
  onOpen: () => void;
  onClose: () => void;
  onKind: (k: MonthsKind) => void;
  onT: Dispatch<SetStateAction<number>>;
}

/**
 * The time slider over the globe: one button while the heatmap shows, then
 * play, pause and a slider through the months, a choice of measured values or
 * the difference from normal, and the colour key.
 */
export function MonthsBar({ frames, loading, error, on, kind, t, canClose, onOpen, onClose, onKind, onT }: Props) {
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState(1);
  const dark = useDarkTheme();
  const last = (frames?.times.length ?? 1) - 1;

  // One frame at a time, stopping on the last.
  useEffect(() => {
    if (!playing || !on) return;
    if (t >= last) {
      setPlaying(false);
      return;
    }
    const id = setTimeout(() => onT(t + 1), SPEEDS[speed]?.ms ?? 180);
    return () => clearTimeout(id);
  }, [playing, on, speed, last, t, onT]);

  if (!on || !frames) {
    return (
      <div className="months months-closed">
        <button type="button" className="action" disabled={loading} onClick={onOpen}>
          {loading ? "Loading the months…" : "▶ Month by month"}
        </button>
        {error && <span className="months-error">{error}</span>}
      </div>
    );
  }

  const when = formatTime(frames.times[t] ?? 0, frames.step);
  const units = frames.units ? ` ${frames.units}` : "";
  const ramp = kind === "anomalies" ? [DIVERGING.low, DIVERGING.mid, DIVERGING.high] : dark ? SEQUENTIAL.dark : SEQUENTIAL.light;
  const [lo, hi] = frames.valueRange;

  function play() {
    if (!playing && t >= last) onT(0);
    setPlaying(!playing);
  }

  return (
    <section className="months" aria-label="The map month by month">
      <div className="months-row">
        <button type="button" className="action small months-play" onClick={play} aria-label={playing ? "Pause" : "Play"}>
          {playing ? "❚❚ Pause" : "▶ Play"}
        </button>
        <input
          type="range"
          className="months-slider"
          min={0}
          max={last}
          value={t}
          aria-label="Month shown"
          aria-valuetext={when}
          onChange={(e) => {
            setPlaying(false);
            onT(Number(e.target.value));
          }}
        />
        <output className="months-when">{when}</output>
      </div>
      <div className="months-row">
        <div className="months-kind" role="group" aria-label="What the colours show">
          <button type="button" className="action small" aria-pressed={kind === "values"} onClick={() => onKind("values")}>
            Measured
          </button>
          {frames.anomalies && (
            <button type="button" className="action small" aria-pressed={kind === "anomalies"} onClick={() => onKind("anomalies")}>
              Difference from normal
            </button>
          )}
        </div>
        <label className="months-speed">
          <span>Speed</span>
          <select value={speed} onChange={(e) => setSpeed(Number(e.target.value))}>
            {SPEEDS.map((s, i) => (
              <option key={s.label} value={i}>
                {s.label}
              </option>
            ))}
          </select>
        </label>
        {canClose && (
          <button
            type="button"
            className="action small"
            onClick={() => {
              setPlaying(false);
              onClose();
            }}
          >
            Show the trend
          </button>
        )}
      </div>
      <div className="months-key" aria-hidden="true">
        <span className="months-ramp" style={{ background: `linear-gradient(to right, ${ramp.join(", ")})` }} />
        <span className="months-ends">
          {kind === "anomalies" ? (
            <>
              <span>{formatSigned(-short(frames.anomalyMax))}</span>
              <span>normal</span>
              <span>
                {formatSigned(short(frames.anomalyMax))}
                {units}
              </span>
            </>
          ) : (
            <>
              <span>{plain(short(lo))}</span>
              <span>
                {plain(short(hi))}
                {units}
              </span>
            </>
          )}
        </span>
      </div>
    </section>
  );
}
