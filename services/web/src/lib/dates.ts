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

/**
 * A month moved inside what the variable covers, on the first of the month.
 * Picking a year can leave the month outside, as with March of a record that
 * starts in October; the month then snaps to the nearest one covered.
 */
export function clampMonth(d: Day, available: LooseRange | undefined): Day {
  const month: Day = { year: d.year, month: d.month, day: 1 };
  const first = available?.start && { year: available.start.year, month: available.start.month, day: 1 };
  const last = available?.end && { year: available.end.year, month: available.end.month, day: 1 };
  if (first && compare(month, first) < 0) return first;
  if (last && compare(month, last) > 0) return last;
  return month;
}

/**
 * The last n years of what the variable covers, as whole months, or
 * undefined when it covers less than that.
 */
export function lastYears(available: LooseRange | undefined, n: number): DayRange | undefined {
  if (!available?.start || !available.end) return undefined;
  const end: Day = { year: available.end.year, month: available.end.month, day: 1 };
  // n years of months ending with the end month start the month after, n years back.
  const start: Day = end.month === 12 ? { year: end.year - n + 1, month: 1, day: 1 } : { year: end.year - n, month: end.month + 1, day: 1 };
  const first: Day = { year: available.start.year, month: available.start.month, day: 1 };
  return compare(start, first) < 0 ? undefined : { start, end };
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
