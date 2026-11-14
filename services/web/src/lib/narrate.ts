// The explanation panel's conversation: turns, what the narrator gets back as
// history, and how an answer's author is described.

import { create } from "@bufbuild/protobuf";
import { type ChatMessage, ChatMessageSchema, Provider, Role } from "@gen/narrator/v1/narrator_pb";

export interface Turn {
  role: "user" | "assistant";
  text: string;
  /** The model that answered, "template" without a key. */
  model?: string;
  /** Numbers in the answer that aren't in the results. */
  unverified?: string[];
}

/** Earlier turns as the narrator wants them, oldest first, skipping empty ones. */
export function historyFor(turns: Turn[]): ChatMessage[] {
  return turns
    .filter((t) => t.text.trim() !== "")
    .map((t) => create(ChatMessageSchema, { role: t.role === "user" ? Role.USER : Role.ASSISTANT, text: t.text }));
}

/** Who wrote an answer, for the line under it. */
export function authorOf(model: string | undefined): string {
  if (!model) return "";
  if (model === "template") return "Written from the results, without an AI model.";
  return `Written by ${model}, from the results. Check anything important against the numbers above.`;
}

export interface LlmSettings {
  provider: Provider;
  model: string;
  /** Empty for the provider's public endpoint. */
  baseUrl: string;
  /** Kept in memory only, never stored. */
  key: string;
}

export const DEFAULT_MODELS: Record<Provider, string> = {
  [Provider.UNSPECIFIED]: "",
  [Provider.OPENAI_CHAT]: "gpt-5.5",
  [Provider.ANTHROPIC_MESSAGES]: "claude-sonnet-5-5",
};
