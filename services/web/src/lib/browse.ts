// The variable browser: search, a topic and a "has a map" filter over the
// catalog, grouped by topic and measurement in the gateway's order, the way
// Giovanni groups its variables by discipline and measurement.

import type { Topic, Variable } from "@gen/gateway/v1/gateway_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";

export interface Filter {
  /** Words to find, in any order. Empty matches everything. */
  query: string;
  /** A topic's name, or "" for every topic. */
  topic: string;
  /** Only variables with a map. */
  mapOnly: boolean;
}

export interface Group {
  topic: string;
  measurement: string;
  variables: Variable[];
}

/** Lower case, without accents, so "colour" finds "Ocean colour" and "e" finds "é". */
function fold(s: string): string {
  return s
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase();
}

/** Every word of the query appears somewhere in the variable's text. */
export function matches(v: Variable, query: string): boolean {
  const words = fold(query).split(/\s+/).filter(Boolean);
  if (!words.length) return true;
  const text = fold([v.name, v.definition, v.topic, v.measurement, v.product, v.units, v.id].join(" "));
  return words.every((w) => text.includes(w));
}

function passes(v: Variable, f: Filter, withTopic: boolean): boolean {
  return (!withTopic || !f.topic || v.topic === f.topic) && (!f.mapOnly || v.grid) && matches(v, f.query);
}

/**
 * The variables that pass the filter, grouped by topic and measurement in the
 * gateway's order. A variable whose topic the gateway didn't list goes last,
 * under its own name, so nothing goes missing.
 */
export function browse(variables: Variable[], topics: Topic[], f: Filter): Group[] {
  const shown = variables.filter((v) => passes(v, f, true));
  const groups: Group[] = [];
  const placed = new Set<string>();
  for (const t of topics) {
    for (const m of t.measurements) {
      const vs = shown.filter((v) => v.topic === t.name && v.measurement === m);
      if (vs.length) groups.push({ topic: t.name, measurement: m, variables: vs });
      for (const v of vs) placed.add(v.id);
    }
  }
  const rest = shown.filter((v) => !placed.has(v.id));
  if (rest.length) groups.push({ topic: "Other", measurement: "", variables: rest });
  return groups;
}

/** How many variables each topic would show with the rest of the filter, keyed by name, "" for all. */
export function topicCounts(variables: Variable[], f: Filter): Map<string, number> {
  const counts = new Map<string, number>([["", 0]]);
  for (const v of variables) {
    if (!passes(v, f, false)) continue;
    counts.set("", (counts.get("") ?? 0) + 1);
    counts.set(v.topic, (counts.get(v.topic) ?? 0) + 1);
  }
  return counts;
}

/** What a variable's default source covers: a map, one point, or the region's average. */
export function coverage(v: Variable): "map" | "one point" | "region average" {
  if (v.grid) return "map";
  if (v.defaultSource === Source.POWER || v.defaultSource === Source.GIOVANNI_POINT) return "one point";
  return "region average";
}
