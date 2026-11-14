import { useId } from "react";
import type { Variable } from "@gen/gateway/v1/gateway_pb";
import { clampMonth, compare, type Day, type DayRange, formatRange, jobRange, type LooseRange, lastYears, yearsBetween } from "../lib/dates";

interface Props {
  variable: Variable | undefined;
  start: Day | undefined;
  end: Day | undefined;
  onChange: (start: Day | undefined, end: Day | undefined) => void;
}

const monthNames = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** Years a picker offers: the variable's, or a broad guess before one is picked. */
function yearsOf(available: LooseRange | undefined): number[] {
  const first = available?.start?.year ?? 1980;
  const last = available?.end?.year ?? new Date().getUTCFullYear();
  return Array.from({ length: Math.max(0, last - first + 1) }, (_, i) => first + i);
}

/**
 * One month, as a month menu and a year menu. Menus work the same in every
 * browser; Firefox has no month input and shows a bare text box instead.
 */
function MonthField({
  label,
  value,
  available,
  disabled,
  onChange,
}: {
  label: string;
  value: Day | undefined;
  available: LooseRange | undefined;
  disabled: boolean;
  onChange: (d: Day) => void;
}) {
  const id = useId();
  const years = yearsOf(available);
  const year = value?.year ?? years.at(-1) ?? 2000;
  // Months the variable doesn't cover in the chosen year can't be picked.
  const outside = (month: number) => compare(clampMonth({ year, month, day: 1 }, available), { year, month, day: 1 }) !== 0;

  return (
    <div className="field" role="group" aria-labelledby={id}>
      <span id={id}>{label}</span>
      <span className="month-field">
        <select
          aria-label={`${label}, month`}
          value={value?.month ?? ""}
          disabled={disabled}
          onChange={(e) => onChange(clampMonth({ year, month: Number(e.target.value), day: 1 }, available))}
        >
          {!value && <option value="">–</option>}
          {monthNames.map((name, i) => (
            <option key={name} value={i + 1} disabled={outside(i + 1)}>
              {name}
            </option>
          ))}
        </select>
        <select
          aria-label={`${label}, year`}
          value={value?.year ?? ""}
          disabled={disabled}
          onChange={(e) => onChange(clampMonth({ year: Number(e.target.value), month: value?.month ?? 1, day: 1 }, available))}
        >
          {!value && <option value="">–</option>}
          {years.map((y) => (
            <option key={y} value={y}>
              {y}
            </option>
          ))}
        </select>
      </span>
    </div>
  );
}

/** Step 3: when. Whole months, kept inside what the variable covers. */
export function DatesPicker({ variable, start, end, onChange }: Props) {
  const available = variable?.available;
  const range = start && end ? jobRange(start, end, available) : undefined;
  const reversed = !!start && !!end && compare(start, end) > 0;
  const years = range && !reversed ? yearsBetween(range) : 0;
  const presets: [string, DayRange | undefined][] = [
    ["Last 10 years", lastYears(available, 10)],
    ["Last 20 years", lastYears(available, 20)],
  ];

  return (
    <fieldset className="step">
      <legend className="step-title">
        <span className="step-num">3</span> Dates
      </legend>
      <div className="fields">
        <MonthField label="From" value={start} available={available} disabled={!variable} onChange={(d) => onChange(d, end)} />
        <MonthField label="To" value={end} available={available} disabled={!variable} onChange={(d) => onChange(start, d)} />
      </div>
      {available?.start && available.end && (
        <div className="date-presets">
          <span className="note">Data from {formatRange(available)}.</span>
          {presets.map(
            ([name, r]) =>
              r && (
                <button key={name} type="button" className="action small" onClick={() => onChange(r.start, r.end)}>
                  {name}
                </button>
              ),
          )}
          <button type="button" className="action small" onClick={() => onChange(available.start, available.end)}>
            All of it
          </button>
        </div>
      )}
      {reversed && <p className="note">The start has to come before the end.</p>}
      {range && !reversed && years < 2 && <p className="note">A trend needs at least two years, and ten or more is better.</p>}
    </fieldset>
  );
}
