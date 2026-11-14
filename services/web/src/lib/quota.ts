// What the team's NASA allowances mean for the chosen source, in words.

import type { GetQuotaResponse } from "@gen/gateway/v1/gateway_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";

/** Sources that draw on the monthly Giovanni quota. */
const usesGiovanni = new Set([Source.GIOVANNI_AREA, Source.GIOVANNI_POINT]);
/** Sources that run as Harmony jobs, which take one of a few slots. */
const usesHarmony = new Set([Source.HARMONY_SUBSET, Source.GIOVANNI_AREA]);

export function quotaMatters(s: Source): boolean {
  return usesGiovanni.has(s) || usesHarmony.has(s);
}

/** Sentences about the allowances this source uses, or none. */
export function quotaNotes(q: GetQuotaResponse | undefined, s: Source): string[] {
  if (!q) return [];
  const notes: string[] = [];
  if (usesGiovanni.has(s) && q.giovanniUsedFraction >= 0) {
    const used = Math.round(q.giovanniUsedFraction * 100);
    notes.push(
      q.giovanniUsedFraction >= q.giovanniCutoff
        ? `Giovanni quota: ${used}% used this month, so new Giovanni jobs are paused until it resets.`
        : `Giovanni quota: ${used}% used this month.`,
    );
  }
  if (usesHarmony.has(s) && q.harmonyLimit > 0) {
    const busy = Math.min(q.harmonyInFlight, q.harmonyLimit);
    notes.push(
      busy >= q.harmonyLimit
        ? `NASA Harmony: all ${q.harmonyLimit} job slots are busy, so a new job waits for one to free up.`
        : `NASA Harmony: ${busy} of ${q.harmonyLimit} job slots busy.`,
    );
  }
  return notes;
}
