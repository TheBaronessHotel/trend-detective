import { describe, expect, test } from "bun:test";
import { center, describe as describeBox, fromCorners, problem } from "../src/lib/bbox";

describe("fromCorners", () => {
  test("either drag direction gives the same rectangle", () => {
    const a = fromCorners({ lat: 32.004, lng: 74.001 }, { lat: 28, lng: 78 });
    const b = fromCorners({ lat: 28, lng: 78 }, { lat: 32.004, lng: 74.001 });
    expect(a).toEqual({ west: 74, south: 28, east: 78, north: 32 });
    expect(b).toEqual(a);
  });
  test("longitudes past the date line are wrapped", () => {
    expect(fromCorners({ lat: 20, lng: 200 }, { lat: 22, lng: 206 })).toEqual({ west: -160, south: 20, east: -154, north: 22 });
  });
  test("latitudes stay on the map", () => {
    expect(fromCorners({ lat: -95, lng: 0 }, { lat: 10, lng: 5 }).south).toBe(-90);
  });
});

describe("problem", () => {
  test("a good rectangle", () => {
    expect(problem({ west: 74, south: 28, east: 78, north: 32 })).toBeUndefined();
  });
  test("what's wrong, in words", () => {
    expect(problem(undefined)).toContain("Draw");
    expect(problem({ west: 78, south: 28, east: 74, north: 32 })).toContain("West");
    expect(problem({ west: 74, south: 32, east: 78, north: 28 })).toContain("South");
    expect(problem({ west: -190, south: 0, east: 0, north: 1 })).toContain("edge");
    expect(problem({ west: Number.NaN, south: 0, east: 1, north: 1 })).toContain("number");
  });
});

test("center and description", () => {
  const b = { west: -93, south: 26, east: -87, north: 29 };
  expect(center(b)).toEqual({ lat: 27.5, lng: -90 });
  expect(describeBox(b)).toBe("93°W to 87°W, 26°N to 29°N");
});
