// get_job / get_job_logs accept one `jobId` XOR a `jobIds` batch; the answer shape follows.

import type { Env } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import type { Orchestrator } from "./types.js";

// A single id keeps failing the call on a bad lookup; a batch isolates failures per entry.
export function resolveJobIds(
  jobId: string | undefined,
  jobIds: string[] | undefined,
): { ids: string[]; batch: boolean } {
  if (jobId && jobIds) throw new ExpectedError("provide only one of jobId or jobIds");
  if (jobIds) return { ids: jobIds, batch: true };
  if (jobId) return { ids: [jobId], batch: false };
  throw new ExpectedError("provide either jobId or jobIds");
}

// Single => the entry's fields flat under the envelope; batch => { count, jobs: entries }.
export function jobEntriesEnvelope(
  orchestrator: Orchestrator,
  env: Env,
  folder: string,
  batch: boolean,
  entries: object[],
): object {
  if (batch) return { orchestrator, env, folder, count: entries.length, jobs: entries };
  return { orchestrator, env, folder, ...entries[0] };
}
