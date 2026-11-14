import { useEffect, useState } from "react";
import type { GetQuotaResponse, Variable } from "@gen/gateway/v1/gateway_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";
import { gateway } from "../api";
import { quotaMatters, quotaNotes } from "../lib/quota";
import { sourceHint, sourceLabel } from "../lib/sources";

interface Props {
  variable: Variable | undefined;
  source: Source | undefined;
  onSource: (s: Source) => void;
  file: File | undefined;
  onFile: (f: File | undefined) => void;
}

/**
 * The team's NASA allowances, read while a source that uses them is chosen
 * and every 30 seconds after. A failed read just shows nothing.
 */
function useQuota(active: boolean): GetQuotaResponse | undefined {
  const [quota, setQuota] = useState<GetQuotaResponse>();
  useEffect(() => {
    if (!active) return;
    let stopped = false;
    const read = () =>
      gateway
        .getQuota({})
        .then((q) => !stopped && setQuota(q))
        .catch(() => !stopped && setQuota(undefined));
    void read();
    const id = setInterval(read, 30_000);
    return () => {
      stopped = true;
      clearInterval(id);
    };
  }, [active]);
  return active ? quota : undefined;
}

/** Step 4: where the data comes from. A list only when there's a choice. */
export function SourcePicker({ variable, source, onSource, file, onFile }: Props) {
  const current = source ?? variable?.defaultSource ?? Source.UNSPECIFIED;
  const quota = useQuota(!!variable && quotaMatters(current));
  if (!variable) return null;

  return (
    <fieldset className="step">
      <legend className="step-title">
        <span className="step-num">4</span> Source
      </legend>
      {variable.sources.length > 1 ? (
        <div className="options" role="radiogroup" aria-label="Source">
          {variable.sources.map((s) => {
            const on = s === current;
            return (
              <label key={s} className="option" aria-checked={on}>
                <input type="radio" name="source" checked={on} onChange={() => onSource(s)} />
                <span className="option-mark" aria-hidden="true">
                  {on ? "●" : "○"}
                </span>
                <span className="option-name">
                  {sourceLabel(s)}
                  {s === variable.defaultSource && <span className="chip">default</span>}
                </span>
                <span className="option-meta">{sourceHint(s)}</span>
              </label>
            );
          })}
        </div>
      ) : (
        <p className="note">
          {sourceLabel(current)}. {sourceHint(current)}
        </p>
      )}
      {quotaNotes(quota, current).map((n) => (
        <p key={n} className="note">
          {n}
        </p>
      ))}
      {current === Source.CSV_UPLOAD && (
        <label className="field" style={{ marginTop: "1rem" }}>
          <span>Your CSV file</span>
          <input type="file" accept=".csv,text/csv" onChange={(e) => onFile(e.target.files?.[0])} />
          {file && (
            <span className="note">
              {file.name}, {Math.ceil(file.size / 1024)} KB
            </span>
          )}
        </label>
      )}
    </fieldset>
  );
}
