import type { Variable } from "@gen/gateway/v1/gateway_pb";
import { type Day, formatRange, fromMonthInput, jobRange, toMonthInput, yearsBetween } from "../lib/dates";

interface Props {
  variable: Variable | undefined;
  start: Day | undefined;
  end: Day | undefined;
  onChange: (start: Day | undefined, end: Day | undefined) => void;
}

/** Step 3: when. Whole months, kept inside what the variable covers. */
export function DatesPicker({ variable, start, end, onChange }: Props) {
  const available = variable?.available;
  const min = toMonthInput(available?.start);
  const max = toMonthInput(available?.end);
  const range = start && end ? jobRange(start, end, available) : undefined;
  const years = range ? yearsBetween(range) : 0;

  return (
    <fieldset className="step">
      <legend className="step-title">
        <span className="step-num">3</span> Dates
      </legend>
      <div className="fields">
        <label className="field">
          <span>From</span>
          <input
            type="month"
            min={min}
            max={max}
            value={toMonthInput(start)}
            onChange={(e) => onChange(fromMonthInput(e.target.value), end)}
          />
        </label>
        <label className="field">
          <span>To</span>
          <input
            type="month"
            min={min}
            max={max}
            value={toMonthInput(end)}
            onChange={(e) => onChange(start, fromMonthInput(e.target.value))}
          />
        </label>
      </div>
      {available?.start && available.end && (
        <p className="note">
          Data from {formatRange(available)}.{" "}
          <button type="button" className="action small" onClick={() => onChange(available.start, available.end)}>
            Use all of it
          </button>
        </p>
      )}
      {range && years < 2 && <p className="note">A trend needs at least two years, and ten or more is better.</p>}
    </fieldset>
  );
}
