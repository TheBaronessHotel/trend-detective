import type { Variable } from "@gen/gateway/v1/gateway_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";

/** What a job makes, beyond the region's chart, which it always makes. */
export interface Outputs {
  /** The full trend suite on the region's series: the verdict and its charts. */
  regionTests: boolean;
  /** A trend test in every map cell, drawn as the heatmap. */
  trendMap: boolean;
  /** The map's values month by month, under the time slider. */
  monthsMap: boolean;
}

export const ALL_OUTPUTS: Outputs = { regionTests: true, trendMap: true, monthsMap: true };

/** Whether this variable, from this source, makes a map at all. */
export function makesMap(variable: Variable | undefined, source: Source | undefined): boolean {
  return !!variable?.grid && (source ?? variable.defaultSource) === Source.HARMONY_SUBSET;
}

interface Props {
  variable: Variable | undefined;
  source: Source | undefined;
  outputs: Outputs;
  onOutputs: (o: Outputs) => void;
}

/**
 * Step 5: what to make. The region's chart always comes; the trend tests and
 * the two maps are each optional, so a quick look at the map skips the tests.
 */
export function OutputsPicker({ variable, source, outputs, onOutputs }: Props) {
  const map = makesMap(variable, source);
  const box = (key: keyof Outputs, label: string, note: string, enabled = true) => (
    <label className={enabled ? "output" : "output off"}>
      <input
        type="checkbox"
        checked={enabled && outputs[key]}
        disabled={!enabled}
        onChange={(e) => onOutputs({ ...outputs, [key]: e.target.checked })}
      />
      <span className="output-name">{label}</span>
      <span className="output-note">{note}</span>
    </label>
  );
  return (
    <fieldset className="step">
      <legend className="step-title">
        <span className="step-num">5</span> What to make
      </legend>
      <p className="note">The chart of the region's average always comes.</p>
      {box("regionTests", "Trend tests for the region", "Is it rising or falling, and could it be chance? The verdict and its charts.")}
      {box("trendMap", "Trend map", "A trend test in every map cell, to show where it's changing.", map)}
      {box("monthsMap", "Month-by-month map", "The values on the globe, month by month, with a time slider.", map)}
      {!map && variable && <p className="note">This variable{source === Source.HARMONY_SUBSET || !variable.grid ? "" : ", from this source,"} has no map.</p>}
    </fieldset>
  );
}
