// The gateway client. Every call goes through Connect, using the code
// generated from proto/ into gen/ts.

import { ConnectError, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { BlobRef } from "@gen/common/v1/common_pb";
import { GatewayService } from "@gen/gateway/v1/gateway_pb";

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
