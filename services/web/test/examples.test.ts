import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { problem } from "../src/lib/bbox";
import { compare } from "../src/lib/dates";
import { EXAMPLES } from "../src/lib/examples";

// The gateway's catalog, read as text: each example must name a variable it
// serves, inside the dates it covers.
const catalog = readFileSync(new URL("../../gateway/catalog.yaml", import.meta.url), "utf8");

function available(id: string): { start: string; end: string } | undefined {
  const block = catalog.split("\n  - id: ").find((b) => b.startsWith(`${id}\n`));
  const m = block && /available: \{ start: (\d{4}-\d{2}-\d{2}), end: (\d{4}-\d{2}-\d{2}) \}/.exec(block);
  return m ? { start: m[1] ?? "", end: m[2] ?? "" } : undefined;
}

const iso = (d: { year: number; month: number }) => `${d.year}-${String(d.month).padStart(2, "0")}`;

test("examples have unique ids", () => {
  expect(new Set(EXAMPLES.map((e) => e.id)).size).toBe(EXAMPLES.length);
});

test.each(EXAMPLES.map((e) => [e.id, e] as const))("%s is a valid job", (_, e) => {
  expect(problem(e.box)).toBeUndefined();
  expect(compare(e.start, e.end)).toBeLessThan(0);
  const range = available(e.variableId);
  expect(range).toBeDefined();
  expect(iso(e.start) >= (range?.start ?? "").slice(0, 7)).toBe(true);
  expect(iso(e.end) <= (range?.end ?? "").slice(0, 7)).toBe(true);
});
