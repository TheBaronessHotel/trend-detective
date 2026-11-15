import { useMemo, useState } from "react";
import type { Topic, Variable } from "@gen/gateway/v1/gateway_pb";
import { browse, coverage, type Filter, topicCounts } from "../lib/browse";
import { formatRange } from "../lib/dates";
import { domainHint, stepLabel } from "../lib/sources";

interface Props {
  variables: Variable[];
  /** The browser's topics, in order, from ListVariables. */
  topics: Topic[];
  selected: Variable | undefined;
  onSelect: (id: string) => void;
}

/**
 * Step 1: what to look at. A browser like Giovanni's: search, a topic, and
 * "only with a map" narrow the list, which groups variables by measurement.
 */
export function VariablePicker({ variables, topics, selected, onSelect }: Props) {
  const [filter, setFilter] = useState<Filter>({ query: "", topic: "", mapOnly: false });
  const groups = useMemo(() => browse(variables, topics, filter), [variables, topics, filter]);
  const counts = useMemo(() => topicCounts(variables, filter), [variables, filter]);
  const shown = groups.reduce((n, g) => n + g.variables.length, 0);
  const hidden = selected && !groups.some((g) => g.variables.includes(selected));

  return (
    <fieldset className="step">
      <legend className="step-title">
        <span className="step-num">1</span> Variable
      </legend>
      <input
        type="search"
        className="browse-search"
        placeholder="Search: rain, ozone, ocean, MODIS…"
        aria-label="Search the variables"
        value={filter.query}
        onChange={(e) => setFilter({ ...filter, query: e.target.value })}
      />
      <div className="browse-topics" role="group" aria-label="Topic">
        {[{ name: "", label: "All" }, ...topics.map((t) => ({ name: t.name, label: t.name }))].map((t) => (
          <button
            key={t.name || "all"}
            type="button"
            className="action small"
            aria-pressed={filter.topic === t.name}
            disabled={!counts.get(t.name) && filter.topic !== t.name}
            onClick={() => setFilter({ ...filter, topic: t.name })}
          >
            {t.label} <span className="browse-count">{counts.get(t.name) ?? 0}</span>
          </button>
        ))}
      </div>
      <label className="browse-map">
        <input type="checkbox" checked={filter.mapOnly} onChange={(e) => setFilter({ ...filter, mapOnly: e.target.checked })} />
        Only with a map
      </label>

      <div className="options browse-list" role="radiogroup" aria-label="Variable">
        {groups.map((g) => (
          <div key={`${g.topic}/${g.measurement}`} className="browse-group">
            <p className="browse-group-title">
              {filter.topic ? g.measurement || g.topic : g.measurement ? `${g.topic} › ${g.measurement}` : g.topic}
            </p>
            {g.variables.map((v) => {
              const on = v.id === selected?.id;
              return (
                <label key={v.id} className="option" aria-checked={on}>
                  <input type="radio" name="variable" value={v.id} checked={on} onChange={() => onSelect(v.id)} />
                  <span className="option-mark" aria-hidden="true">
                    {on ? "●" : "○"}
                  </span>
                  <span className="option-name">{v.name}</span>
                  <span className="option-meta">
                    {v.units} · {v.product} · {formatRange(v.available)} · {coverage(v)}
                  </span>
                </label>
              );
            })}
          </div>
        ))}
        {shown === 0 && (
          <p className="note browse-empty">
            Nothing matches{filter.query ? ` "${filter.query}"` : ""}
            {filter.topic ? ` in ${filter.topic}` : ""}
            {filter.mapOnly ? " with a map" : ""}.
          </p>
        )}
      </div>
      <p className="note browse-total">
        {shown} of {variables.length} variables
      </p>

      {selected && (
        <div className="details">
          {hidden && (
            <p className="note">
              Picked: <strong>{selected.name}</strong>, hidden by the search.
            </p>
          )}
          <p>{selected.definition}</p>
          <ul className="chips">
            <li className="chip">{selected.grid ? "map and region average" : `${coverage(selected)} only`}</li>
            <li className="chip">{selected.product}</li>
            <li className="chip">{selected.units}</li>
            <li className="chip">{stepLabel(selected.step)}</li>
          </ul>
          {domainHint(selected.domain) && <p className="note">{domainHint(selected.domain)}</p>}
        </div>
      )}
    </fieldset>
  );
}
