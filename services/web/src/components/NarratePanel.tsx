import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { Provider } from "@gen/narrator/v1/narrator_pb";
import { errorMessage, gateway } from "../api";
import { authorOf, DEFAULT_MODELS, historyFor, type LlmSettings, type Turn } from "../lib/narrate";

const KEY_HEADER = "X-Llm-Api-Key";

// The Markdown renderer loads on its own, the first time an answer shows.
const Markdown = lazy(() => import("./Markdown"));

/**
 * Explanations of the results in plain words. The overview works for
 * everyone: without a key, the narrator writes it from the results alone.
 * Follow-up questions need the user's own AI key, which stays in this page's
 * memory and goes only to the gateway with each call.
 */
export function NarratePanel({ jobId }: { jobId: string }) {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [question, setQuestion] = useState("");
  const [llm, setLlm] = useState<LlmSettings>({
    provider: Provider.ANTHROPIC_MESSAGES,
    model: DEFAULT_MODELS[Provider.ANTHROPIC_MESSAGES],
    baseUrl: "",
    key: "",
  });
  const abort = useRef<AbortController | undefined>(undefined);

  // A new job starts a new conversation.
  useEffect(() => {
    setTurns([]);
    setError(undefined);
    return () => abort.current?.abort();
  }, [jobId]);

  const hasKey = llm.key.trim() !== "";

  async function ask(q: string) {
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;
    const history = historyFor(turns);
    const asked: Turn[] = q ? [...turns, { role: "user", text: q }] : turns;
    setTurns([...asked, { role: "assistant", text: "" }]);
    setBusy(true);
    setError(undefined);
    try {
      const stream = gateway.narrate(
        {
          jobId,
          question: q,
          history,
          ...(hasKey ? { llm: { provider: llm.provider, model: llm.model, baseUrl: llm.baseUrl } } : {}),
        },
        { signal: ctrl.signal, headers: hasKey ? { [KEY_HEADER]: llm.key } : undefined },
      );
      let text = "";
      for await (const res of stream) {
        const ev = res.event?.event;
        if (ev?.case === "textDelta") {
          text += ev.value;
          setTurns([...asked, { role: "assistant", text }]);
        } else if (ev?.case === "done") {
          setTurns([...asked, { role: "assistant", text, model: ev.value.model, unverified: ev.value.unverifiedNumbers }]);
        }
      }
      setQuestion("");
    } catch (err) {
      if (ctrl.signal.aborted) return;
      setTurns(asked);
      setError(errorMessage(err));
    } finally {
      if (!ctrl.signal.aborted) setBusy(false);
    }
  }

  return (
    <section className="narrate" aria-label="Explanations">
      <div className="narrate-head">
        <h3 className="narrate-title">In plain words</h3>
        <button type="button" className="action" disabled={busy} onClick={() => void ask("")}>
          {turns.length ? "Explain again" : "Explain the results"}
        </button>
      </div>

      {turns.length > 0 && (
        <ol className="narrate-turns" aria-live="polite">
          {turns.map((t, i) => (
            <li key={i} className={`turn turn-${t.role}`}>
              {t.role === "assistant" && t.text ? (
                // Plain text until the Markdown renderer has loaded.
                <Suspense fallback={<p className="turn-text">{t.text}</p>}>
                  <Markdown text={t.text} />
                </Suspense>
              ) : (
                <p className="turn-text">{t.text || (busy && i === turns.length - 1 ? "…" : "")}</p>
              )}
              {t.role === "assistant" && t.model && <p className="note">{authorOf(t.model)}</p>}
              {t.unverified && t.unverified.length > 0 && (
                <p className="note turn-warn">
                  <span aria-hidden="true">!</span> These numbers don't appear in the results, so check them:{" "}
                  {t.unverified.join(", ")}.
                </p>
              )}
            </li>
          ))}
        </ol>
      )}

      {error && (
        <p className="alert" role="alert">
          <span className="alert-glyph" aria-hidden="true">
            ✕
          </span>
          <span>{error}</span>
        </p>
      )}

      <form
        className="narrate-ask"
        onSubmit={(e) => {
          e.preventDefault();
          if (question.trim() && hasKey) void ask(question.trim());
        }}
      >
        <label className="field">
          <span>Ask about these results</span>
          <input
            type="text"
            value={question}
            placeholder={hasKey ? "Why might this be changing?" : "Add your AI key below to ask questions"}
            onChange={(e) => setQuestion(e.target.value)}
            disabled={!hasKey || busy}
          />
        </label>
        <button type="submit" className="action" disabled={!hasKey || busy || !question.trim()}>
          Ask
        </button>
      </form>

      <details className="narrate-key">
        <summary>{hasKey ? "Your AI key is set for this page" : "Use your own AI key"}</summary>
        <p className="note">
          The key stays in this page's memory, goes only to our gateway with each question, and is never stored or
          logged.
        </p>
        <div className="fields">
          <label className="field">
            <span>Provider</span>
            <select
              value={llm.provider}
              onChange={(e) => {
                const provider = Number(e.target.value) as Provider;
                setLlm({ ...llm, provider, model: DEFAULT_MODELS[provider] });
              }}
            >
              <option value={Provider.ANTHROPIC_MESSAGES}>Anthropic</option>
              <option value={Provider.OPENAI_CHAT}>OpenAI-compatible</option>
            </select>
          </label>
          <label className="field">
            <span>Model</span>
            <input type="text" value={llm.model} onChange={(e) => setLlm({ ...llm, model: e.target.value })} />
          </label>
          <label className="field">
            <span>Base URL (optional)</span>
            <input
              type="url"
              value={llm.baseUrl}
              placeholder="the provider's own"
              onChange={(e) => setLlm({ ...llm, baseUrl: e.target.value })}
            />
          </label>
          <label className="field">
            <span>Key</span>
            <input
              type="password"
              autoComplete="off"
              value={llm.key}
              onChange={(e) => setLlm({ ...llm, key: e.target.value })}
            />
          </label>
        </div>
      </details>
    </section>
  );
}
