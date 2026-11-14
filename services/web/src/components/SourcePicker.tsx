import type { Variable } from "@gen/gateway/v1/gateway_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";
import { sourceHint, sourceLabel } from "../lib/sources";

interface Props {
  variable: Variable | undefined;
  source: Source | undefined;
  onSource: (s: Source) => void;
  file: File | undefined;
  onFile: (f: File | undefined) => void;
}

/** Step 4: where the data comes from. A list only when there's a choice. */
export function SourcePicker({ variable, source, onSource, file, onFile }: Props) {
  if (!variable) return null;
  const current = source ?? variable.defaultSource;

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
