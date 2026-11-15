import { useEffect, useRef } from "react";
import type { CellView } from "./JobPanel";
import { SeriesChart } from "./SeriesChart";

/**
 * One map cell's own chart, under the region's: the cell's values and
 * anomalies month by month, and its trend test from the trend map. It shows
 * how one place can differ from the region's average.
 */
export function CellChart({ cell, title, onClose }: { cell: CellView; title: string; onClose?: () => void }) {
  const ref = useRef<HTMLElement>(null);
  // Bring the new chart into view under the region's.
  useEffect(() => {
    ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [cell]);
  return (
    <section className="cell-chart" ref={ref} aria-label="One map cell">
      <p className="cell-trend">{cell.trend}</p>
      <SeriesChart
        series={cell.series}
        title={`${title}, one map cell`}
        actions={
          <button type="button" className="action small" onClick={onClose}>
            Close
          </button>
        }
      />
    </section>
  );
}
