import { describe, expect, test } from "bun:test";
import { clampMonth, formatRange, jobRange, lastYears, yearsBetween } from "../src/lib/dates";

describe("jobRange", () => {
  const available = { start: { year: 2015, month: 1, day: 2 }, end: { year: 2022, month: 2, day: 28 } };

  test("whole months, from the first to the last day", () => {
    const r = jobRange({ year: 2016, month: 1, day: 1 }, { year: 2020, month: 2, day: 1 }, undefined);
    expect(r).toEqual({ start: { year: 2016, month: 1, day: 1 }, end: { year: 2020, month: 2, day: 29 } });
  });
  test("kept inside what the variable covers", () => {
    const r = jobRange({ year: 2015, month: 1, day: 1 }, { year: 2022, month: 12, day: 1 }, available);
    expect(r.start).toEqual({ year: 2015, month: 1, day: 2 });
    expect(r.end).toEqual({ year: 2022, month: 2, day: 28 });
  });
  test("labels", () => {
    expect(formatRange(available)).toBe("Jan 2015 to Feb 2022");
    expect(yearsBetween({ start: { year: 2005, month: 1, day: 1 }, end: { year: 2025, month: 12, day: 31 } })).toBe(21);
  });
});

describe("clampMonth", () => {
  const available = { start: { year: 2004, month: 10, day: 1 }, end: { year: 2026, month: 9, day: 30 } };

  test("a month inside the range stays, on the first", () => {
    expect(clampMonth({ year: 2010, month: 5, day: 17 }, available)).toEqual({ year: 2010, month: 5, day: 1 });
  });

  test("a year change that leaves the month outside snaps it back in", () => {
    expect(clampMonth({ year: 2004, month: 3, day: 1 }, available)).toEqual({ year: 2004, month: 10, day: 1 });
    expect(clampMonth({ year: 2026, month: 12, day: 1 }, available)).toEqual({ year: 2026, month: 9, day: 1 });
  });

  test("no range, no limits", () => {
    expect(clampMonth({ year: 1900, month: 1, day: 5 }, undefined)).toEqual({ year: 1900, month: 1, day: 1 });
  });
});

describe("lastYears", () => {
  const available = { start: { year: 1980, month: 1, day: 1 }, end: { year: 2026, month: 9, day: 30 } };

  test("ten years of months ending with the last one covered", () => {
    expect(lastYears(available, 10)).toEqual({ start: { year: 2016, month: 10, day: 1 }, end: { year: 2026, month: 9, day: 1 } });
  });

  test("a December end starts in January", () => {
    const dec = { start: { year: 1981, month: 1, day: 1 }, end: { year: 2025, month: 12, day: 31 } };
    expect(lastYears(dec, 20)).toEqual({ start: { year: 2006, month: 1, day: 1 }, end: { year: 2025, month: 12, day: 1 } });
  });

  test("nothing when the record is shorter", () => {
    const co2 = { start: { year: 2015, month: 1, day: 2 }, end: { year: 2022, month: 2, day: 28 } };
    expect(lastYears(co2, 10)).toBeUndefined();
    expect(lastYears(undefined, 10)).toBeUndefined();
  });
});
