import { describe, expect, test } from "bun:test";
import { formatRange, fromMonthInput, jobRange, toMonthInput, yearsBetween } from "../src/lib/dates";

describe("month inputs", () => {
  test("round trip", () => {
    expect(toMonthInput({ year: 2005, month: 3, day: 17 })).toBe("2005-03");
    expect(fromMonthInput("2005-03")).toEqual({ year: 2005, month: 3, day: 1 });
  });
  test("bad input", () => {
    expect(fromMonthInput("")).toBeUndefined();
    expect(fromMonthInput("2005-13")).toBeUndefined();
    expect(fromMonthInput("March")).toBeUndefined();
  });
});

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
