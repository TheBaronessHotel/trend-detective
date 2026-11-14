// Plain-language names for the protos' enums.

import { Format, Source } from "@gen/ingest/v1/ingest_pb";
import { TimeStep } from "@gen/common/v1/common_pb";
import { Domain } from "@gen/gateway/v1/gateway_pb";

interface SourceInfo {
  label: string;
  hint: string;
}

const sources: Record<Source, SourceInfo> = {
  [Source.UNSPECIFIED]: { label: "Default", hint: "" },
  [Source.HARMONY_SUBSET]: {
    label: "Map from NASA Harmony",
    hint: "Every grid cell in the rectangle, one file per month. Takes a minute or two.",
  },
  [Source.GIOVANNI_AREA]: {
    label: "Area average from Giovanni",
    hint: "One average for the whole rectangle per time step. NASA takes a few minutes.",
  },
  [Source.GIOVANNI_POINT]: {
    label: "One point from Giovanni",
    hint: "The values at the rectangle's center. Ready in seconds.",
  },
  [Source.POWER]: {
    label: "One point from NASA POWER",
    hint: "Monthly values at the rectangle's center. Ready in seconds.",
  },
  [Source.CSV_UPLOAD]: {
    label: "Your own CSV file",
    hint: "Two columns: the date, like 2010-03-01 or 2010-03, and the value.",
  },
  [Source.BUNDLE]: {
    label: "Copy shipped with the app",
    hint: "Works offline, for one fixed place.",
  },
  [Source.APPEEARS]: {
    label: "Area average from NASA AppEEARS",
    hint: "1 km MODIS satellite images, averaged over the rectangle. NASA takes requests in turn, so it takes a few minutes, longer when it's busy.",
  },
};

export function sourceLabel(s: Source): string {
  return sources[s]?.label ?? "Unknown source";
}

export function sourceHint(s: Source): string {
  return sources[s]?.hint ?? "";
}

const formats: Partial<Record<Format, string>> = {
  [Format.NETCDF4]: "NetCDF-4 files",
  [Format.GIOVANNI_CSV]: "Giovanni area-average CSV",
  [Format.GIOVANNI_POINT_CSV]: "Giovanni point CSV",
  [Format.POWER_JSON]: "NASA POWER JSON",
  [Format.USER_CSV]: "Your CSV, cleaned up",
  [Format.GEOTIFF]: "GeoTIFF files",
};

export function formatLabel(f: Format): string {
  return formats[f] ?? "Unknown format";
}

export function stepLabel(s: TimeStep): string {
  switch (s) {
    case TimeStep.DAILY:
      return "daily";
    case TimeStep.WEEKLY:
      return "weekly";
    case TimeStep.MONTHLY:
      return "monthly";
    default:
      return "";
  }
}

export function domainHint(d: Domain): string {
  switch (d) {
    case Domain.LAND:
      return "Land only: grid cells over the sea are empty.";
    case Domain.OCEAN:
      return "Ocean only: grid cells over land are empty.";
    default:
      return "";
  }
}
