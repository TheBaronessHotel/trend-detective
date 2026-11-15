// The rectangle a job covers, in degrees.

export interface Box {
  west: number;
  south: number;
  east: number;
  north: number;
}

export interface Corner {
  lat: number;
  lng: number;
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v));
}

/** Rounds to a hundredth of a degree, about a kilometre. */
function round(v: number): number {
  return Math.round(v * 100) / 100;
}

/**
 * The rectangle between two corners the user dragged between, in either
 * order. Longitudes are wrapped to -180..180 first, because the map keeps
 * counting past the date line when it's panned around the globe.
 */
export function fromCorners(a: Corner, b: Corner): Box {
  const wrap = (lng: number) => ((((lng + 180) % 360) + 360) % 360) - 180;
  const lngA = clamp(round(wrap(a.lng)), -180, 180);
  const lngB = clamp(round(wrap(b.lng)), -180, 180);
  const latA = clamp(round(a.lat), -90, 90);
  const latB = clamp(round(b.lat), -90, 90);
  return {
    west: Math.min(lngA, lngB),
    east: Math.max(lngA, lngB),
    south: Math.min(latA, latB),
    north: Math.max(latA, latB),
  };
}

/** What's wrong with a rectangle, in words, or undefined when it's fine. */
export function problem(b: Box | undefined): string | undefined {
  if (!b) return "Draw a rectangle on the map.";
  const values = [b.west, b.south, b.east, b.north];
  if (values.some((v) => !Number.isFinite(v))) return "Every edge needs a number.";
  if (b.west < -180 || b.east > 180 || b.south < -90 || b.north > 90) {
    return "The rectangle goes past the edge of the map.";
  }
  if (b.west >= b.east) return "West has to be less than east.";
  if (b.south >= b.north) return "South has to be less than north.";
  return undefined;
}

export function center(b: Box): Corner {
  return { lat: (b.south + b.north) / 2, lng: (b.west + b.east) / 2 };
}

function lonLabel(v: number): string {
  return `${Math.abs(v)}°${v < 0 ? "W" : v > 0 ? "E" : ""}`;
}

function latLabel(v: number): string {
  return `${Math.abs(v)}°${v < 0 ? "S" : v > 0 ? "N" : ""}`;
}

/** "74°E to 78°E, 28°N to 32°N", to two decimals, as a map cell's edges need. */
export function describe(b: Box): string {
  return `${lonLabel(round(b.west))} to ${lonLabel(round(b.east))}, ${latLabel(round(b.south))} to ${latLabel(round(b.north))}`;
}

/** "28.61°N, 77.21°E", for a point. */
export function describePoint(p: { lat: number; lon: number }): string {
  return `${latLabel(round(p.lat))}, ${lonLabel(round(p.lon))}`;
}
