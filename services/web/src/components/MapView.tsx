import { type ReactNode, useEffect, useRef, useState } from "react";
import type { FeatureCollection } from "geojson";
import {
  type ExpressionSpecification,
  type GeoJSONSource,
  type LayerSpecification,
  Map as MapLibreMap,
  type MapLayerMouseEvent,
  type MapMouseEvent,
  NavigationControl,
  Popup,
  setWorkerUrl,
  type StyleSpecification,
} from "maplibre-gl";
import workerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url";
import "maplibre-gl/dist/maplibre-gl.css";
import { type Box, type Corner, fromCorners } from "../lib/bbox";
import { DIVERGING, type Heatmap } from "../lib/grid";
import { cellAt, type MonthFrames, type MonthsKind, SEQUENTIAL } from "../lib/months";
import { formatSigned, formatTime, formatValue } from "../lib/series";
import { useDarkTheme } from "../lib/useDarkTheme";

interface Props {
  box: Box | undefined;
  onBox: (b: Box) => void;
  /** Per-cell trends from a finished job, drawn over the map. */
  heatmap?: Heatmap;
  /** Bumped to move the view to the rectangle. */
  focus?: number;
  /** The month-by-month map, shown instead of the heatmap while it's set. */
  months?: MonthsView;
  /** Drawn over the map, at the bottom. */
  children?: ReactNode;
  /** A heatmap or month-by-month cell was clicked: its index in the Grid. */
  onCell?: (cell: number) => void;
}

export interface MonthsView {
  frames: MonthFrames;
  kind: MonthsKind;
  /** The time step shown. */
  t: number;
}

// MapLibre picks its worker file by a name built at run time, which no
// bundler can see. Vite builds the worker here and hands over its URL.
setWorkerUrl(workerUrl);

// OpenStreetMap through OpenFreeMap's vector styles: gray "positron" for the
// light theme, "dark" for the dark one. Free, and no key needed.
const styleUrls = {
  light: "https://tiles.openfreemap.org/styles/positron",
  dark: "https://tiles.openfreemap.org/styles/dark",
};

const sourceId = "bbox";

/** The rectangle's ink, so it stands out on either map. */
const ink = (dark: boolean) => (dark ? "#ffffff" : "#111111");

/**
 * Adjusts one layer of the OpenFreeMap styles:
 * - one name per label, English where OpenStreetMap has it, else the name in
 *   Latin script; the styles stack two scripts, which crowds the globe;
 * - the dark style asks for a "circle-11" city dot that its sprite sheet
 *   calls "circle_11".
 */
function adaptLayer(layer: LayerSpecification): LayerSpecification {
  if (layer.type !== "symbol" || !layer.layout) return layer;
  const layout = { ...layer.layout };
  if (JSON.stringify(layout["text-field"] ?? "").includes("name:nonlatin")) {
    layout["text-field"] = ["coalesce", ["get", "name_en"], ["get", "name:latin"], ["get", "name"]];
  }
  if (layout["icon-image"] !== undefined) {
    layout["icon-image"] = JSON.parse(JSON.stringify(layout["icon-image"]).replaceAll('"circle-11"', '"circle_11"'));
  }
  return { ...layer, layout } as LayerSpecification;
}

/**
 * Brings the dark style in line with the light one:
 * - sea names in the gray of its country names; it prints them in near-black,
 *   which vanishes on its dark water;
 * - ocean names too, as points, where it only names seas along lines;
 * - region names (states, provinces) from zoom 5, not across the whole globe.
 */
function likeLight(layer: LayerSpecification): LayerSpecification[] {
  if (layer.type !== "symbol") return [layer];
  if (layer.id === "place_state") return [{ ...layer, minzoom: 5 }];
  if (layer.id !== "water_name") return [layer];
  const seas: LayerSpecification = {
    ...layer,
    paint: { ...layer.paint, "text-color": "rgb(101,101,101)", "text-halo-color": "rgba(0,0,0,0.7)" },
  };
  const {
    "symbol-placement": _placement,
    "symbol-spacing": _spacing,
    "text-rotation-alignment": _rotation,
    ...pointLayout
  } = seas.layout ?? {};
  const oceans: LayerSpecification = {
    ...seas,
    id: "water_name_point",
    filter: ["match", ["geometry-type"], ["MultiPoint", "Point"], true, false],
    layout: pointLayout,
  };
  return [seas, oceans];
}

/** A plain sphere, for when the map style can't load (offline, blocked). */
function bareGlobe(dark: boolean): StyleSpecification {
  return {
    version: 8,
    sources: {},
    layers: [{ id: "background", type: "background", paint: { "background-color": dark ? "#1b1b1d" : "#c2c8ca" } }],
  };
}

const styles = new Map<boolean, Promise<StyleSpecification>>();

/** The map style for a theme, drawn on a globe. Fetched once per theme. */
function globeStyle(dark: boolean): Promise<StyleSpecification> {
  let style = styles.get(dark);
  if (!style) {
    style = fetch(dark ? styleUrls.dark : styleUrls.light)
      .then((res) => (res.ok ? (res.json() as Promise<StyleSpecification>) : bareGlobe(dark)))
      .catch(() => bareGlobe(dark))
      .then((s) => ({
        ...s,
        projection: { type: "globe" },
        // No glow around the globe; the edge comes from CSS (see .map canvas).
        sky: { "atmosphere-blend": 0 },
        layers: s.layers.map(adaptLayer).flatMap((l) => (dark ? likeLight(l) : [l])),
      }));
    styles.set(dark, style);
  }
  return style;
}

function polygon(b: Box | undefined): FeatureCollection {
  if (!b) return { type: "FeatureCollection", features: [] };
  const ring = [
    [b.west, b.south],
    [b.east, b.south],
    [b.east, b.north],
    [b.west, b.north],
    [b.west, b.south],
  ];
  return {
    type: "FeatureCollection",
    features: [{ type: "Feature", properties: {}, geometry: { type: "Polygon", coordinates: [ring] } }],
  };
}

const heatSource = "heatmap";

function heatColour(max: number): ExpressionSpecification {
  const m = max > 0 ? max : 1;
  return ["interpolate", ["linear"], ["get", "slope"], -m, DIVERGING.low, 0, DIVERGING.mid, m, DIVERGING.high];
}

/**
 * The heatmap: each cell coloured by its trend, and the cells that pass the
 * test after the false discovery rate correction outlined in ink. It goes
 * under the rectangle's outline, which stays on top.
 */
function addHeatmap(map: MapLibreMap, h: Heatmap | undefined, dark: boolean) {
  if (!h) return;
  const source = map.getSource(heatSource) as GeoJSONSource | undefined;
  if (source) source.setData(h.cells);
  else map.addSource(heatSource, { type: "geojson", data: h.cells });
  const before = map.getLayer("bbox-line") ? "bbox-line" : undefined;
  if (map.getLayer("heat-fill")) map.setPaintProperty("heat-fill", "fill-color", heatColour(h.max));
  else
    map.addLayer(
      { id: "heat-fill", type: "fill", source: heatSource, paint: { "fill-color": heatColour(h.max), "fill-opacity": 0.75 } },
      before,
    );
  if (!map.getLayer("heat-sig")) {
    map.addLayer(
      {
        id: "heat-sig",
        type: "line",
        source: heatSource,
        filter: ["==", ["get", "significant"], true],
        // Thin, so a map where every cell passes still shows its colours.
        paint: { "line-color": ink(dark), "line-width": 1, "line-opacity": dark ? 0.45 : 0.7 },
      },
      before,
    );
  }
}

function showHeatmap(map: MapLibreMap, shown: boolean) {
  for (const id of ["heat-sig", "heat-fill"]) {
    if (map.getLayer(id)) map.setLayoutProperty(id, "visibility", shown ? "visible" : "none");
  }
}

function removeHeatmap(map: MapLibreMap) {
  for (const id of ["heat-sig", "heat-fill"]) if (map.getLayer(id)) map.removeLayer(id);
  if (map.getSource(heatSource)) map.removeSource(heatSource);
}

const monthsSource = "months";

/**
 * The month's colour scale, fixed across every frame so months compare:
 * values on the sequential scale, anomalies on the trend's diverging one,
 * centred on zero.
 */
function monthsColour(m: MonthsView, dark: boolean): ExpressionSpecification {
  const v: ExpressionSpecification = ["number", ["feature-state", "v"], 0];
  if (m.kind === "anomalies") {
    const a = m.frames.anomalyMax;
    return ["interpolate", ["linear"], v, -a, DIVERGING.low, 0, DIVERGING.mid, a, DIVERGING.high];
  }
  const [lo, hi] = m.frames.valueRange;
  const ramp = dark ? SEQUENTIAL.dark : SEQUENTIAL.light;
  const stops = ramp.flatMap((c, i) => [lo + ((hi - lo) * i) / (ramp.length - 1), c]);
  return ["interpolate", ["linear"], v, ...stops] as ExpressionSpecification;
}

/** The month-by-month cells, under the rectangle's outline, with the heatmap hidden. */
function addMonths(map: MapLibreMap, m: MonthsView | undefined, dark: boolean, shown: { current?: MonthFrames }) {
  if (!m) return;
  const source = map.getSource(monthsSource) as GeoJSONSource | undefined;
  if (!source) map.addSource(monthsSource, { type: "geojson", data: m.frames.cells });
  else if (shown.current !== m.frames) source.setData(m.frames.cells);
  shown.current = m.frames;
  const colour = monthsColour(m, dark);
  if (map.getLayer("months-fill")) map.setPaintProperty("months-fill", "fill-color", colour);
  else
    map.addLayer(
      {
        id: "months-fill",
        type: "fill",
        source: monthsSource,
        paint: { "fill-color": colour, "fill-opacity": ["case", ["boolean", ["feature-state", "has"], false], 0.8, 0] },
      },
      map.getLayer("bbox-line") ? "bbox-line" : undefined,
    );
  showHeatmap(map, false);
}

/** Colours every cell for the time step shown. */
function paintMonth(map: MapLibreMap, m: MonthsView | undefined) {
  if (!m || !map.getSource(monthsSource)) return;
  for (const f of m.frames.cells.features) {
    const i = f.id as number;
    const v = cellAt(m.frames, m.kind, m.t, i);
    map.setFeatureState({ source: monthsSource, id: i }, v === undefined ? { has: false } : { has: true, v });
  }
}

function removeMonths(map: MapLibreMap, shown: { current?: MonthFrames }) {
  if (map.getLayer("months-fill")) map.removeLayer("months-fill");
  if (map.getSource(monthsSource)) map.removeSource(monthsSource);
  shown.current = undefined;
  showHeatmap(map, true);
}

/** A cell's number in the month shown, for the hover popup. */
function monthText(m: MonthsView, cell: number): string {
  const when = formatTime(m.frames.times[m.t] ?? 0, m.frames.step);
  const v = cellAt(m.frames, m.kind, m.t, cell);
  const units = m.frames.units ? ` ${m.frames.units}` : "";
  if (v === undefined) return `${when}: no data`;
  return m.kind === "anomalies" ? `${when}: ${formatSigned(v)}${units} from normal` : `${when}: ${formatValue(v)}${units}`;
}

/**
 * Runs fn once the map can take layers. isStyleLoaded is false while tiles
 * load too, say right after the view moves, so then it waits for the map to
 * settle. fn reads the latest props itself, as it may run later.
 */
function whenReady(map: MapLibreMap, fn: () => void) {
  if (map.isStyleLoaded()) fn();
  else map.once("idle", fn);
}

function addRectangle(map: MapLibreMap, b: Box | undefined, dark: boolean) {
  if (!map.getSource(sourceId)) map.addSource(sourceId, { type: "geojson", data: polygon(b) });
  if (!map.getLayer("bbox-fill")) {
    map.addLayer({ id: "bbox-fill", type: "fill", source: sourceId, paint: { "fill-color": ink(dark), "fill-opacity": 0.12 } });
  }
  if (!map.getLayer("bbox-line")) {
    map.addLayer({ id: "bbox-line", type: "line", source: sourceId, paint: { "line-color": ink(dark), "line-width": 2 } });
  }
}

function showRectangle(map: MapLibreMap | undefined, b: Box | undefined) {
  (map?.getSource(sourceId) as GeoJSONSource | undefined)?.setData(polygon(b));
}

/**
 * Step 2's globe. Drag to spin it and scroll to zoom. Press "Draw a
 * rectangle" and drag to draw one instead; spinning is off while drawing.
 * The rectangle can also be typed in beside the globe.
 */
export function MapView({ box, onBox, heatmap, focus, months, children, onCell }: Props) {
  const container = useRef<HTMLDivElement>(null);
  const map = useRef<MapLibreMap | undefined>(undefined);
  const [drawing, setDrawing] = useState(false);
  const dark = useDarkTheme();

  // The map's handlers outlive renders, so they read the latest values here.
  const latest = useRef({ box, onBox, drawing, dark, heatmap, months, onCell });
  latest.current = { box, onBox, drawing, dark, heatmap, months, onCell };
  // The frames the months source holds, so new ones replace its data.
  const shownFrames = useRef<MonthFrames | undefined>(undefined);

  // Which theme's style the map shows, or is loading. A quick double toggle
  // can leave an older style still loading; only the newest one is applied.
  const shownDark = useRef<boolean | undefined>(undefined);
  const showTheme = (m: MapLibreMap, d: boolean) => {
    if (shownDark.current === d) return;
    shownDark.current = d;
    void globeStyle(d).then((s) => {
      // setStyle fires style.load, which re-adds the rectangle.
      if (shownDark.current === d) m.setStyle(s, { diff: false });
    });
  };

  useEffect(() => {
    let cancelled = false;
    let m: MapLibreMap | undefined;
    const startDark = latest.current.dark;
    shownDark.current = startDark;
    void globeStyle(startDark).then((style) => {
      if (cancelled || !container.current) return;
      const created = new MapLibreMap({
        container: container.current,
        style,
        center: [30, 22],
        zoom: 2.1,
        minZoom: 0.6,
        maxZoom: 11,
        dragRotate: false,
        pitchWithRotate: false,
        touchPitch: false,
        boxZoom: false,
        attributionControl: { compact: true },
      });
      m = created;
      created.touchZoomRotate.disableRotation();
      created.addControl(new NavigationControl({ showCompass: false }), "bottom-right");
      // Every style, the first and each one after a theme change, gets the rectangle.
      created.on("style.load", () => {
        addRectangle(created, latest.current.box, latest.current.dark);
        addHeatmap(created, latest.current.heatmap, latest.current.dark);
        // A new style has none of the months' cells or their colours.
        shownFrames.current = undefined;
        addMonths(created, latest.current.months, latest.current.dark, shownFrames);
        paintMonth(created, latest.current.months);
      });

      // A cell's numbers, on hover.
      const popup = new Popup({ closeButton: false, closeOnClick: false, className: "heat-popup" });
      created.on("mousemove", "heat-fill", (e: MapLayerMouseEvent) => {
        const f = e.features?.[0];
        const h = latest.current.heatmap;
        if (!f || !h) return;
        const slope = Number(f.properties?.slope);
        const sure = f.properties?.significant === true || f.properties?.significant === "true";
        const months = Number(f.properties?.months);
        const count = months >= 0 ? ` · ${months} months with data` : "";
        popup
          .setLngLat(e.lngLat)
          .setText(`${formatSigned(slope)}${h.units ? ` ${h.units}` : ""} per decade, ${sure ? "passes the test" : "could be chance"}${count}`)
          .addTo(created);
      });
      created.on("mouseleave", "heat-fill", () => popup.remove());
      created.on("mousemove", "months-fill", (e: MapLayerMouseEvent) => {
        const f = e.features?.[0];
        const m = latest.current.months;
        if (!f || f.id === undefined || !m) return;
        popup.setLngLat(e.lngLat).setText(monthText(m, Number(f.id))).addTo(created);
      });
      created.on("mouseleave", "months-fill", () => popup.remove());

      // A click on a cell opens its own chart; the pointer says cells are clickable.
      const clickCell = (cell: number | undefined) => {
        if (cell !== undefined && Number.isInteger(cell) && !latest.current.drawing) latest.current.onCell?.(cell);
      };
      created.on("click", "heat-fill", (e: MapLayerMouseEvent) => clickCell(Number(e.features?.[0]?.properties?.cell)));
      created.on("click", "months-fill", (e: MapLayerMouseEvent) => clickCell(Number(e.features?.[0]?.id)));
      for (const layer of ["heat-fill", "months-fill"]) {
        created.on("mouseenter", layer, () => {
          if (!latest.current.drawing) created.getCanvas().style.cursor = "pointer";
        });
        created.on("mouseleave", layer, () => {
          created.getCanvas().style.cursor = "";
        });
      }

      let start: Corner | undefined;
      const corner = (e: MapMouseEvent): Corner => ({ lat: e.lngLat.lat, lng: e.lngLat.lng });
      created.on("mousedown", (e: MapMouseEvent) => {
        if (!latest.current.drawing) return;
        e.preventDefault();
        start = corner(e);
      });
      created.on("mousemove", (e: MapMouseEvent) => {
        if (start) showRectangle(created, fromCorners(start, corner(e)));
      });
      created.on("mouseup", (e: MapMouseEvent) => {
        if (!start) return;
        const b = fromCorners(start, corner(e));
        start = undefined;
        setDrawing(false);
        // A click without a drag draws nothing and keeps the old rectangle.
        if (b.east > b.west && b.north > b.south) latest.current.onBox(b);
        else showRectangle(created, latest.current.box);
      });
      map.current = created;
      // The theme may have changed while the first style loaded.
      showTheme(created, latest.current.dark);
    });
    return () => {
      cancelled = true;
      m?.remove();
      map.current = undefined;
    };
  }, []);

  useEffect(() => {
    showRectangle(map.current, box);
  }, [box]);

  useEffect(() => {
    const b = latest.current.box;
    if (focus && b) map.current?.fitBounds([b.west, b.south, b.east, b.north], { padding: 64, maxZoom: 7 });
  }, [focus]);

  // A new heatmap goes on the map, and the view moves to it the first time;
  // switching the test it's drawn from keeps the view where it is.
  const hadHeatmap = useRef(false);
  useEffect(() => {
    const m = map.current;
    if (!m) return;
    whenReady(m, () => {
      const { heatmap: h, months: mv, dark: d } = latest.current;
      if (!h) {
        removeHeatmap(m);
        return;
      }
      addHeatmap(m, h, d);
      // The month-by-month map, if it's on, stays on top.
      if (mv) showHeatmap(m, false);
    });
    const b = latest.current.box;
    if (heatmap && b && !hadHeatmap.current) m.fitBounds([b.west, b.south, b.east, b.north], { padding: 48, maxZoom: 7 });
    hadHeatmap.current = !!heatmap;
  }, [heatmap]);

  // The month-by-month map replaces the heatmap while it's on.
  const frames = months?.frames;
  const kind = months?.kind;
  useEffect(() => {
    const m = map.current;
    if (!m) return;
    whenReady(m, () => {
      const mv = latest.current.months;
      if (!mv) removeMonths(m, shownFrames);
      else {
        addMonths(m, mv, latest.current.dark, shownFrames);
        paintMonth(m, mv);
      }
    });
  }, [frames, kind]);

  // New months, as when a job made no trend map, move the view to them too.
  useEffect(() => {
    const b = latest.current.box;
    if (frames && b) map.current?.fitBounds([b.west, b.south, b.east, b.north], { padding: 48, maxZoom: 7 });
  }, [frames]);

  useEffect(() => {
    const m = map.current;
    if (m && months && m.getSource(monthsSource)) paintMonth(m, months);
  }, [months]);

  useEffect(() => {
    if (map.current) showTheme(map.current, dark);
  }, [dark]);

  useEffect(() => {
    const m = map.current;
    if (!m) return;
    if (drawing) m.dragPan.disable();
    else m.dragPan.enable();
  }, [drawing]);

  function zoomToBox() {
    if (box) map.current?.fitBounds([box.west, box.south, box.east, box.north], { padding: 64, maxZoom: 7 });
  }

  return (
    <div className="map-wrap">
      <div className="map-toolbar">
        <button type="button" className="action" aria-pressed={drawing} onClick={() => setDrawing((d) => !d)}>
          {drawing ? "Drag on the globe" : box ? "Redraw" : "Draw a rectangle"}
        </button>
        {box && (
          <button type="button" className="action" onClick={zoomToBox}>
            Zoom to it
          </button>
        )}
      </div>
      <div ref={container} className={drawing ? "map drawing" : "map"} />
      {children}
    </div>
  );
}
