import { useEffect, useRef, useState } from "react";
import type { FeatureCollection } from "geojson";
import {
  type GeoJSONSource,
  type LayerSpecification,
  Map as MapLibreMap,
  type MapMouseEvent,
  NavigationControl,
  setWorkerUrl,
  type StyleSpecification,
} from "maplibre-gl";
import workerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url";
import "maplibre-gl/dist/maplibre-gl.css";
import { type Box, type Corner, fromCorners } from "../lib/bbox";
import { useDarkTheme } from "../lib/useDarkTheme";

interface Props {
  box: Box | undefined;
  onBox: (b: Box) => void;
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
export function MapView({ box, onBox }: Props) {
  const container = useRef<HTMLDivElement>(null);
  const map = useRef<MapLibreMap | undefined>(undefined);
  const [drawing, setDrawing] = useState(false);
  const dark = useDarkTheme();

  // The map's handlers outlive renders, so they read the latest values here.
  const latest = useRef({ box, onBox, drawing, dark });
  latest.current = { box, onBox, drawing, dark };

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
      created.on("style.load", () => addRectangle(created, latest.current.box, latest.current.dark));

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
    </div>
  );
}
