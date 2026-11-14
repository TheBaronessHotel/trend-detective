// Dates in the form the protos use: year, month and day, with no time zone.
// The app works in whole months, like most of the catalog.

export interface Day {
  year: number;
  month: number; // 1 to 12
  day: number;
}

export interface DayRange {
  start: Day;
  end: Day;
}

/** A range as the protos send it, where either end can be missing. */
export interface LooseRange {
  start?: Day | undefined;
  end?: Day | undefined;
}

/** "YYYY-MM", the value of an <input type="month">. */
export function toMonthInput(d: Day | undefined): string {
  if (!d) return "";
  return `${String(d.year).padStart(4, "0")}-${String(d.month).padStart(2, "0")}`;
}

/** The first day of the month in an "YYYY-MM" input, or undefined. */
export function fromMonthInput(value: string): Day | undefined {
  const m = /^(\d{4})-(\d{2})$/.exec(value);
  if (!m) return undefined;
  const year = Number(m[1]);
  const month = Number(m[2]);
  if (month < 1 || month > 12) return undefined;
  return { year, month, day: 1 };
}

export function daysInMonth(year: number, month: number): number {
  return new Date(Date.UTC(year, month, 0)).getUTCDate();
}

export function lastDayOfMonth(d: Day): Day {
  return { year: d.year, month: d.month, day: daysInMonth(d.year, d.month) };
}

export function compare(a: Day, b: Day): number {
  return a.year - b.year || a.month - b.month || a.day - b.day;
}

function later(a: Day, b: Day): Day {
  return compare(a, b) >= 0 ? a : b;
}

function earlier(a: Day, b: Day): Day {
  return compare(a, b) <= 0 ? a : b;
}

/**
 * The range a job should ask for: from the first day of the start month to
 * the last day of the end month, kept inside what the variable covers. The
 * gateway clamps too, but showing it here means no surprises.
 */
export function jobRange(startMonth: Day, endMonth: Day, available: LooseRange | undefined): DayRange {
  let start: Day = { year: startMonth.year, month: startMonth.month, day: 1 };
  let end: Day = lastDayOfMonth(endMonth);
  if (available?.start) start = later(start, available.start);
  if (available?.end) end = earlier(end, available.end);
  return { start, end };
}

/** Whole years between two days, rounded down, for "a 20-year record". */
export function yearsBetween(r: DayRange): number {
  const months = (r.end.year - r.start.year) * 12 + (r.end.month - r.start.month) + 1;
  return Math.floor(months / 12);
}

const monthNames = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "Mar 2010". */
export function formatMonth(d: Day | undefined): string {
  if (!d) return "";
  return `${monthNames[d.month - 1] ?? "?"} ${d.year}`;
}

/** "Jan 2005 to Dec 2025". */
export function formatRange(r: LooseRange | undefined): string {
  if (!r?.start || !r.end) return "";
  return `${formatMonth(r.start)} to ${formatMonth(r.end)}`;
}
