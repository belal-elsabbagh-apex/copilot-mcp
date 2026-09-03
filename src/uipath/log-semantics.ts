// Semantic classification of a job's robot logs: level severity and free-text
// failure-language detection. A leaf module (type-only import) so it can be
// imported at runtime by both uipath.ts (refineJobLogs) and log-digest.ts
// (digestLogs) without creating a cycle.

import type { JobLog } from "./uipath.js";

const ERROR_LEVELS = new Set(["error", "fatal", "critical"]);
const levelOf = (log: JobLog): string => (log.Level || "").toLowerCase();

// Words/phrases that suggest a failure even when the log's own level is benign.
const FAILURE_TERMS = [
  "exceptions?",
  "errors?",
  "errored",
  "faults?",
  "faulted",
  "fail(?:s|ed|ing|ure)?",
  "crash(?:es|ed|ing)?",
  "abort(?:s|ed|ing)?",
  "terminat(?:e|ed|es|ing|ion)",
  "kill(?:s|ed)?",
  "halt(?:s|ed|ing)?",
  "panic(?:ked|king)?",
  "fatal",
  "critical",
  "severe",
  "unable to",
  "not able",
  "cannot",
  "can[’'`]?t",
  "could ?n[o’'`]?t",
  "did ?n[o’'`]?t",
  "was ?n[o’'`]?t able",
  "no response",
  "not found",
  "missing",
  "invalid",
  "unexpected",
  "unhandled",
  "illegal",
  "denied",
  "rejected",
  "refused",
  "unauthori[sz]ed",
  "forbidden",
  "expired",
  "access denied",
  "permission denied",
  "invalid credentials",
  "time(?:d)? ?out",
  "timeout",
  "unreachable",
  "disconnected",
  "connection (?:refused|reset|lost|closed|error)",
  "reset by peer",
  "retries exhausted",
  "max(?:imum)? retries",
  "gave up",
  "giving up",
  "stack ?trace",
  "traceback",
  "null ?reference",
  "null ?pointer",
  "out of memory",
  "stack overflow",
  "overflow",
  "deadlock",
  "segfault",
  "segmentation fault",
  "corrupt(?:s|ed|ion|ing)?",
  "broken",
  "bad request",
  "internal server error",
  "service unavailable",
  "bad gateway",
  "gateway timeout",
  "too many requests",
];
const FAILURE_PATTERN = new RegExp(`\\b(?:${FAILURE_TERMS.join("|")})\\b`, "i");

// Free-text failure phrases front-load their meaning ("ABORT: ...", "Unable to find submit
// button", "the request timed out" — all comfortably under this). Embedded data blobs (a full
// result.json dump, HTML page titles, screenshot paths) can run many hundreds of characters and
// incidentally contain one of these generic words deep inside with no bearing on the actual
// outcome — bounding the scan avoids flagging those while still catching every real message.
const FAILURE_SCAN_WINDOW = 200;
const hasFailureLanguage = (message: string): boolean =>
  FAILURE_PATTERN.test(message.slice(0, FAILURE_SCAN_WINDOW));

export const isFailureLog = (log: JobLog): boolean =>
  ERROR_LEVELS.has(levelOf(log)) || hasFailureLanguage(log.Message || "");
