// get_job_artifacts: list a job's artifacts, or download chosen ones to a private cache dir.
// Artifacts (screenshots, trace.zip, recordings, job.log) can contain PHI, so they are
// written only under ~/.cache/copilot-mcp/artifacts/<jobId>/ (dir 0700, files 0600) — never
// into a repo — and only the paths come back, not the bytes.

import { chmodSync, mkdirSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import type { Env } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import { type ApexArtifact, downloadArtifact, listArtifacts } from "./api.js";
import { resolveFolderId } from "./client.js";

export interface ArtifactInfo {
  filename: string;
  type: string; // logs | videos | other
  sizeBytes: number;
}

export interface DownloadedArtifact {
  filename: string;
  path: string;
  sizeBytes: number;
}

export const defaultArtifactsRoot = (): string =>
  join(homedir(), ".cache", "copilot-mcp", "artifacts");

// The job id becomes a directory name, so it must not be able to climb out of the root.
const SAFE_JOB_ID = /^[\w-]+$/;
// Same for server-supplied filenames: a bare name only, never a path.
const isBareFilename = (name: string): boolean =>
  /^[^/\\]+$/.test(name) && name !== "." && name !== "..";

export async function listJobArtifacts(
  env: Env,
  jobId: string,
  folder?: string,
): Promise<{ folder: string; artifacts: ArtifactInfo[] }> {
  const [resolved, rows] = await Promise.all([
    resolveFolderId(env, folder),
    listArtifacts(env, folder, jobId),
  ]);
  return {
    folder: resolved.name,
    artifacts: rows.map((a) => ({
      filename: a.filename,
      type: a.artifact_type,
      sizeBytes: a.size_bytes,
    })),
  };
}

function choose(rows: ApexArtifact[], names: string[] | "all"): ApexArtifact[] {
  if (names === "all") return rows;
  const missing = names.filter((n) => !rows.some((a) => a.filename === n));
  if (missing.length > 0) {
    const available = rows.map((a) => a.filename).join(", ") || "(none)";
    throw new ExpectedError(
      `no artifact named ${missing.map((n) => `'${n}'`).join(", ")} on this job; available: ${available}`,
    );
  }
  return rows.filter((a) => names.includes(a.filename));
}

// `folder` is only the listing's folder-name override; downloads need Bearer, not a folder header.
export async function downloadJobArtifacts(
  env: Env,
  jobId: string,
  names: string[] | "all",
  rootDir: string = defaultArtifactsRoot(),
  folder?: string | undefined,
): Promise<{ dir: string; files: DownloadedArtifact[] }> {
  if (!SAFE_JOB_ID.test(jobId)) throw new ExpectedError(`'${jobId}' is not a valid job id`);
  const chosen = choose(await listArtifacts(env, folder, jobId), names);
  const unsafe = chosen.find((a) => !isBareFilename(a.filename));
  if (unsafe)
    throw new ExpectedError(`refusing artifact with a path in its name: '${unsafe.filename}'`);

  const dir = join(rootDir, jobId);
  for (const d of [rootDir, dir]) {
    mkdirSync(d, { recursive: true, mode: 0o700 });
    chmodSync(d, 0o700);
  }
  const files: DownloadedArtifact[] = [];
  for (const a of chosen) {
    const { bytes } = await downloadArtifact(env, a);
    const path = join(dir, a.filename);
    await writeFile(path, bytes, { mode: 0o600 });
    files.push({ filename: a.filename, path, sizeBytes: bytes.byteLength });
  }
  return { dir, files };
}
