import type { Variable } from "@gen/gateway/v1/gateway_pb";
import { EXAMPLES, type Example } from "../lib/examples";

interface Props {
  variables: Variable[] | undefined;
  busy: boolean;
  onPick: (e: Example) => void;
}

/** Ready-made cases that fill in the steps below and run straight away. */
export function ExamplesBar({ variables, busy, onPick }: Props) {
  // Only cases whose variable this gateway serves.
  const available = EXAMPLES.filter((e) => variables?.some((v) => v.id === e.variableId));
  if (available.length === 0) return null;
  return (
    <section className="examples" aria-label="Examples">
      <p className="examples-title">Try an example</p>
      <div className="examples-list">
        {available.map((e) => (
          <button key={e.id} type="button" className="action small" disabled={busy} title={e.blurb} onClick={() => onPick(e)}>
            {e.label}
          </button>
        ))}
      </div>
    </section>
  );
}
