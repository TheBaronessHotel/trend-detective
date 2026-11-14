import { expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { GetQuotaResponseSchema } from "@gen/gateway/v1/gateway_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";
import { quotaMatters, quotaNotes } from "../src/lib/quota";

const q = (o: Partial<{ giovanniUsedFraction: number; harmonyInFlight: number }>) =>
  create(GetQuotaResponseSchema, { giovanniUsedFraction: 0.12, giovanniCutoff: 0.9, harmonyInFlight: 1, harmonyLimit: 4, ...o });

test("Giovanni area uses both allowances", () => {
  expect(quotaNotes(q({}), Source.GIOVANNI_AREA)).toEqual([
    "Giovanni quota: 12% used this month.",
    "NASA Harmony: 1 of 4 job slots busy.",
  ]);
});

test("each source only hears about its own", () => {
  expect(quotaNotes(q({}), Source.GIOVANNI_POINT)).toEqual(["Giovanni quota: 12% used this month."]);
  expect(quotaNotes(q({}), Source.HARMONY_SUBSET)).toEqual(["NASA Harmony: 1 of 4 job slots busy."]);
  expect(quotaNotes(q({}), Source.POWER)).toEqual([]);
  expect(quotaMatters(Source.POWER)).toBe(false);
});

test("past the limits it says what happens", () => {
  expect(quotaNotes(q({ giovanniUsedFraction: 0.95 }), Source.GIOVANNI_POINT)[0]).toContain("paused");
  expect(quotaNotes(q({ harmonyInFlight: 4 }), Source.HARMONY_SUBSET)[0]).toContain("waits");
});

test("an unknown Giovanni share says nothing", () => {
  expect(quotaNotes(q({ giovanniUsedFraction: -1 }), Source.GIOVANNI_POINT)).toEqual([]);
});
