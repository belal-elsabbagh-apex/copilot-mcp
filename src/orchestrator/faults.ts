// Backend-neutral fault helpers: the dedupe signature and the GitHub issue payload shape
// shared by every orchestrator's `build_faulted_job_issue`. The `<!-- fault-signature -->`
// marker in an issue body and the search query that finds it MUST stay in lockstep, so
// both strings are built here, once.

// Default target repo (the RPA automation project). Override per call via opts.repo.
export const DEFAULT_REPO = "Apex-Medical-AI-Inc/RPAPlaywright";

export interface FaultedJobIssue {
  repo: string;
  title: string;
  body: string;
  labels: string[];
  faultSignature: string; // stable, normalized error — the dedupe key
  searchQuery: string; // GitHub query to find an existing issue for this fault
  recurrenceComment: string; // post this on the existing issue when one is found
}

export const firstLine = (s: string): string => (s.split("\n")[0] ?? s).trim().slice(0, 120);

const GUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi;
const ISO_TS = /\d{4}-\d{2}-\d{2}[t ]\d{2}:\d{2}:\d{2}(?:\.\d+)?z?/gi;

// Collapse a raw error into a stable signature so the SAME recurring fault (a
// different job Key / ids / timestamps each run) maps to one issue. Strips GUIDs,
// ISO timestamps and long digit runs, drops quotes/backticks (they break GitHub
// search phrases), lowercases, collapses whitespace and truncates.
export function normalizeError(msg: string): string {
  return msg
    .toLowerCase()
    .replace(GUID, "<id>")
    .replace(ISO_TS, "<ts>")
    .replace(/\d{4,}/g, "<n>")
    .replace(/["`]/g, "")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 160);
}

// Hidden marker appended to an issue body; faultSearchQuery finds it by its phrase.
export const signatureMarker = (signature: string): string =>
  `<!-- fault-signature: ${signature} -->`;

export const faultSearchQuery = (repo: string, signature: string): string =>
  `repo:${repo} is:issue is:open in:body "fault-signature: ${signature}"`;
