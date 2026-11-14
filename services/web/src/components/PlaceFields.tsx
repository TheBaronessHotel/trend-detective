import { useEffect, useState } from "react";
import { type Box, describe, problem } from "../lib/bbox";

interface Props {
  box: Box | undefined;
  onBox: (b: Box) => void;
}

type Edge = keyof Box;

const edges: { key: Edge; label: string; min: number; max: number }[] = [
  { key: "west", label: "West", min: -180, max: 180 },
  { key: "east", label: "East", min: -180, max: 180 },
  { key: "south", label: "South", min: -90, max: 90 },
  { key: "north", label: "North", min: -90, max: 90 },
];

/**
 * Step 2: where. The map draws the rectangle, and these fields show it and
 * let you type exact edges. A half-typed number stays in the field until it
 * parses, instead of jumping back.
 */
export function PlaceFields({ box, onBox }: Props) {
  const [text, setText] = useState<Record<Edge, string>>({ west: "", east: "", south: "", north: "" });

  useEffect(() => {
    if (box) {
      setText({ west: String(box.west), east: String(box.east), south: String(box.south), north: String(box.north) });
    }
  }, [box]);

  function edit(key: Edge, value: string) {
    const next = { ...text, [key]: value };
    setText(next);
    const parsed = {
      west: Number(next.west),
      east: Number(next.east),
      south: Number(next.south),
      north: Number(next.north),
    };
    if (Object.values(next).every((v) => v.trim() !== "") && !problem(parsed)) onBox(parsed);
  }

  const issue = problem(box);

  return (
    <fieldset className="step">
      <legend className="step-title">
        <span className="step-num">2</span> Place
      </legend>
      <div className="fields">
        {edges.map((e) => (
          <label key={e.key} className="field">
            <span>{e.label}</span>
            <input
              type="number"
              inputMode="decimal"
              step="0.25"
              min={e.min}
              max={e.max}
              placeholder="degrees"
              value={text[e.key]}
              onChange={(ev) => edit(e.key, ev.target.value)}
            />
          </label>
        ))}
      </div>
      <p className="note">{issue ?? describe(box as Box)}</p>
    </fieldset>
  );
}
