// Curated cases: one click fills in the form and runs the job. Each has a
// clear story, and the NASA downloads behind them are cached once run.
//
// The cases live in examples.json, which `make warm-examples` also reads to
// run them all ahead of a demo.

import type { Box } from "./bbox";
import type { Day } from "./dates";
import data from "./examples.json";

export interface Example {
  id: string;
  /** Short, for the button. */
  label: string;
  /** One line on what it shows. */
  blurb: string;
  variableId: string;
  box: Box;
  start: Day;
  end: Day;
}

/** "2003-02" as the first of that month. */
function month(s: string): Day {
  const [year, m] = s.split("-").map(Number);
  return { year: year ?? 0, month: m ?? 1, day: 1 };
}

export const EXAMPLES: Example[] = data.map((e) => ({ ...e, start: month(e.start), end: month(e.end) }));
