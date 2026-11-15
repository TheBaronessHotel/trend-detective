import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { TopicSchema, VariableSchema } from "@gen/gateway/v1/gateway_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";
import { browse, coverage, matches, topicCounts } from "../src/lib/browse";

const v = (id: string, name: string, topic: string, measurement: string, product: string, grid = true, definition = "") =>
  create(VariableSchema, { id, name, topic, measurement, product, grid, definition });

const variables = [
  v("t2m", "Air temperature (map)", "Air", "Temperature", "MERRA-2", true, "How warm the air is."),
  v("rain", "Rain and snow (one point)", "Air", "Rain and snow", "POWER", false),
  v("chl", "Ocean chlorophyll", "Ocean", "Ocean colour", "NOBM", false, "Tiny plants in the sea."),
  v("tair", "Air temperature (one point)", "Air", "Temperature", "POWER", false),
  v("odd", "Something new", "Space", "Stars", "X"),
];
const topics = [
  create(TopicSchema, { name: "Air", measurements: ["Temperature", "Rain and snow"] }),
  create(TopicSchema, { name: "Ocean", measurements: ["Ocean colour"] }),
];

describe("matches", () => {
  test("every word, anywhere, any case", () => {
    expect(matches(variables[0]!, "AIR merra")).toBe(true);
    expect(matches(variables[0]!, "air modis")).toBe(false);
    expect(matches(variables[2]!, "plants sea")).toBe(true);
    expect(matches(variables[0]!, "  ")).toBe(true);
  });

  test("accents don't matter", () => {
    expect(matches(v("x", "Café", "Air", "Temperature", "P"), "cafe")).toBe(true);
  });
});

describe("browse", () => {
  const all = { query: "", topic: "", mapOnly: false };

  test("groups in the gateway's order, with unknown topics last", () => {
    const groups = browse(variables, topics, all);
    expect(groups.map((g) => `${g.topic}/${g.measurement}`)).toEqual(["Air/Temperature", "Air/Rain and snow", "Ocean/Ocean colour", "Other/"]);
    expect(groups[0]!.variables.map((x) => x.id)).toEqual(["t2m", "tair"]);
  });

  test("a topic, a search and only maps narrow it", () => {
    expect(browse(variables, topics, { ...all, topic: "Ocean" }).flatMap((g) => g.variables.map((x) => x.id))).toEqual(["chl"]);
    expect(browse(variables, topics, { ...all, query: "point" }).flatMap((g) => g.variables.map((x) => x.id))).toEqual(["tair", "rain"]);
    expect(browse(variables, topics, { ...all, mapOnly: true }).flatMap((g) => g.variables.map((x) => x.id))).toEqual(["t2m", "odd"]);
  });

  test("topic counts follow the search but not the topic", () => {
    const counts = topicCounts(variables, { ...all, query: "temperature", topic: "Ocean" });
    expect(counts.get("")).toBe(2);
    expect(counts.get("Air")).toBe(2);
    expect(counts.get("Ocean")).toBeUndefined();
  });
});

test("coverage says map, one point or region average", () => {
  expect(coverage(variables[0]!)).toBe("map");
  expect(coverage(create(VariableSchema, { defaultSource: Source.POWER }))).toBe("one point");
  expect(coverage(create(VariableSchema, { defaultSource: Source.GIOVANNI_AREA }))).toBe("region average");
});
