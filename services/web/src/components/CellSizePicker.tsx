import type { Variable } from "@gen/gateway/v1/gateway_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";
import type { Box } from "../lib/bbox";
import { previewGrid } from "../lib/grid";

interface Props {
  variable: Variable | undefined;
  /** Only map sources make a grid. */
  source: Source | undefined;
  box: Box | undefined;
  /** Native pixels per cell side, or undefined for the variable's default. */
  block: number | undefined;
  onBlock: (b: number | undefined) => void;
}

const SIZES = [1, 2, 4, 8, 16];

function km(v: number): string {
  return v >= 10 ? `${Math.round(v)}` : v.toFixed(1);
}

/**
 * How coarse the heatmap's cells are, for gridded variables: square blocks of
 * native pixels. Smaller cells show smaller features but are noisier; bigger
 * cells find faint, widespread trends more reliably.
 */
export function CellSizePicker({ variable, source, box, block, onBlock }: Props) {
  if (!variable?.grid || (source ?? variable.defaultSource) !== Source.HARMONY_SUBSET) return null;
  const preview = box ? previewGrid(variable, box, block) : undefined;
  const auto = variable.defaultBlockPixels || undefined;
  return (
    <div className="field cell-size">
      <label htmlFor="cell-size">Map cell size</label>
      <select
        id="cell-size"
        value={block ?? ""}
        onChange={(e) => onBlock(e.target.value === "" ? undefined : Number(e.target.value))}
      >
        <option value="">{auto ? `Default (${auto} × ${auto} pixels)` : "Default"}</option>
        {SIZES.map((s) => (
          <option key={s} value={s}>
            {s === 1 ? "1 pixel (native)" : `${s} × ${s} pixels`}
          </option>
        ))}
      </select>
      {preview && (
        <span className="note">
          About {preview.rows} × {preview.cols} cells, each about {km(preview.km.ns)} × {km(preview.km.ew)} km
          {block && preview.k !== block ? `. Adjusted to ${preview.k} × ${preview.k} pixels to fit the rectangle` : ""}.
          Smaller cells show smaller features but are noisier.
        </span>
      )}
    </div>
  );
}
