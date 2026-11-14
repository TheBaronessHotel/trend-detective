import { expect, test } from "bun:test";
import { Role } from "@gen/narrator/v1/narrator_pb";
import { authorOf, historyFor } from "../src/lib/narrate";

test("history keeps the turns in order and drops empty ones", () => {
  const h = historyFor([
    { role: "assistant", text: "It is warming." },
    { role: "user", text: "Why?" },
    { role: "assistant", text: "  " },
  ]);
  expect(h.map((m) => [m.role, m.text])).toEqual([
    [Role.ASSISTANT, "It is warming."],
    [Role.USER, "Why?"],
  ]);
});

test("the author line says when no AI wrote it", () => {
  expect(authorOf("template")).toContain("without an AI model");
  expect(authorOf("claude-sonnet-5-5")).toContain("Written by claude-sonnet-5-5");
  expect(authorOf(undefined)).toBe("");
});
