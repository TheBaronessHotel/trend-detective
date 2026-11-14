import type { Variable } from "@gen/gateway/v1/gateway_pb";
import { formatRange } from "../lib/dates";
import { domainHint, stepLabel } from "../lib/sources";

interface Props {
  variables: Variable[];
  selected: Variable | undefined;
  onSelect: (id: string) => void;
}

/** Step 1: what to look at. Each row shows enough to pick it. */
export function VariablePicker({ variables, selected, onSelect }: Props) {
  return (
    <fieldset className="step">
      <legend className="step-title">
        <span className="step-num">1</span> Variable
      </legend>
      <div className="options" role="radiogroup" aria-label="Variable">
        {variables.map((v) => {
          const on = v.id === selected?.id;
          return (
            <label key={v.id} className="option" aria-checked={on}>
              <input type="radio" name="variable" value={v.id} checked={on} onChange={() => onSelect(v.id)} />
              <span className="option-mark" aria-hidden="true">
                {on ? "●" : "○"}
              </span>
              <span className="option-name">{v.name}</span>
              <span className="option-meta">
                {v.units} · {stepLabel(v.step)} · {formatRange(v.available)}
              </span>
            </label>
          );
        })}
      </div>
      {selected && (
        <div className="details">
          <p>{selected.definition}</p>
          <ul className="chips">
            <li className="chip">{selected.grid ? "map and series" : "series only"}</li>
            <li className="chip">{selected.units}</li>
            <li className="chip">{stepLabel(selected.step)}</li>
          </ul>
          {domainHint(selected.domain) && <p className="note">{domainHint(selected.domain)}</p>}
        </div>
      )}
    </fieldset>
  );
}
