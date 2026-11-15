// The gateway client. Every call goes through Connect, using the code
// generated from proto/ into gen/ts.

import { fromJson } from "@bufbuild/protobuf";
import { ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { BlobRef } from "@gen/common/v1/common_pb";
import { Artifact, GatewayService } from "@gen/gateway/v1/gateway_pb";
import { type Grid, GridSchema, type Series, SeriesSchema } from "@gen/preprocess/v1/data_pb";
import {
  type GridAnalysis,
  GridAnalysisSchema,
  type SeriesAnalysis,
  SeriesAnalysisSchema,
} from "@gen/analysis/v1/results_pb";
import { type MonthFrames, monthFrames, readCube } from "./lib/months";

/** Where the browser reaches the gateway. Set VITE_GATEWAY_URL to change it. */
export const gatewayUrl: string = import.meta.env.VITE_GATEWAY_URL ?? "http://localhost:8080";

export const gateway = createClient(GatewayService, createConnectTransport({ baseUrl: gatewayUrl }));

/**
 * The message to show for a failed call. The gateway writes its refusals for
 * people to read, so a ConnectError's own message is shown as it is.
 */
export function errorMessage(err: unknown): string {
  if (err instanceof ConnectError) {
    return err.rawMessage || "The gateway refused the request.";
  }
  if (err instanceof TypeError) {
    return `Couldn't reach the gateway at ${gatewayUrl}. Is the stack running?`;
  }
  return err instanceof Error ? err.message : String(err);
}

/**
 * Uploads a CSV through a signed URL and returns the reference a job needs.
 * The gateway checks the name and size before signing.
 */
export async function uploadCsv(file: File, signal?: AbortSignal): Promise<BlobRef> {
  const res = await gateway.createUploadURL({ filename: file.name, sizeBytes: BigInt(file.size) }, { signal });
  const put = await fetch(res.url, { method: "PUT", body: file, signal });
  if (!put.ok || !res.upload) {
    throw new Error(`The upload failed (HTTP ${put.status}).`);
  }
  return res.upload;
}

/** Downloads one of a job's results through the signed URL the gateway gives. */
async function download(jobId: string, artifact: Artifact, signal?: AbortSignal): Promise<Response> {
  const { url } = await gateway.getResultURL({ jobId, artifact }, { signal });
  const res = await fetch(url, { signal });
  if (!res.ok) throw new Error(`The download failed (HTTP ${res.status}).`);
  return res;
}

/**
 * The region's series that preprocess wrote, as protobuf JSON. Fields added to
 * the contract later are skipped rather than refused.
 */
export async function loadSeries(jobId: string, signal?: AbortSignal): Promise<Series> {
  const res = await download(jobId, Artifact.SERIES, signal);
  return fromJson(SeriesSchema, await res.json(), { ignoreUnknownFields: true });
}

/** The trend tests on the region's series, as analysis wrote them. */
export async function loadSeriesAnalysis(jobId: string, signal?: AbortSignal): Promise<SeriesAnalysis> {
  const res = await download(jobId, Artifact.SERIES_ANALYSIS, signal);
  return fromJson(SeriesAnalysisSchema, await res.json(), { ignoreUnknownFields: true });
}

/** The map grid's cells, edges and block size, as preprocess wrote them. */
export async function loadGrid(jobId: string, signal?: AbortSignal): Promise<Grid> {
  const res = await download(jobId, Artifact.GRID, signal);
  return fromJson(GridSchema, await res.json(), { ignoreUnknownFields: true });
}

/**
 * The grid's month-by-month values and anomalies, for the time slider.
 * undefined when the grid has no values, or they don't match its size.
 */
export async function loadGridMonths(jobId: string, grid: Grid, signal?: AbortSignal): Promise<MonthFrames | undefined> {
  const { dataUrl, anomaliesUrl } = await gateway.getResultURL({ jobId, artifact: Artifact.GRID }, { signal });
  if (!dataUrl) return undefined;
  const blob = async (url: string) => {
    const res = await fetch(url, { signal });
    if (!res.ok) throw new Error(`The download failed (HTTP ${res.status}).`);
    return readCube(await res.arrayBuffer(), grid);
  };
  const [values, anomalies] = await Promise.all([blob(dataUrl), anomaliesUrl ? blob(anomaliesUrl) : undefined]);
  return values ? monthFrames(grid, values, anomalies) : undefined;
}

/** The trend tests on every grid cell, for the heatmap. */
export async function loadGridAnalysis(jobId: string, signal?: AbortSignal): Promise<GridAnalysis> {
  const res = await download(jobId, Artifact.GRID_ANALYSIS, signal);
  return fromJson(GridAnalysisSchema, await res.json(), { ignoreUnknownFields: true });
}

/**
 * Saves the series as a CSV file. It goes through a blob URL because browsers
 * ignore a download name on a link to another origin.
 */
export async function saveSeriesCsv(jobId: string, filename: string): Promise<void> {
  const blob = await (await download(jobId, Artifact.SERIES_CSV)).blob();
  const href = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = href;
  a.download = filename;
  a.click();
  // Give the browser a moment to start the download before letting go.
  setTimeout(() => URL.revokeObjectURL(href), 1000);
}
