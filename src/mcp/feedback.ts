// On-failure feedback: when a tool fails for a reason that looks like a bug in
// THIS server (not user/config/upstream state), the error response carries a
// `reportIssue` field with a prefilled GitHub new-issue URL so reporting is one click.
//
// Why classify at all: the user asked to nudge only on failures of unknown origin
// or the server's own responsibility — never on expected, user-actionable errors
// (bad profile, missing/invalid config, bad input, not-found, auth, any HTTP error
// the upstream system returned, or the sync_settings not-implemented stub).
//
// Why this lives here and runs on the raw error: every error in the codebase is a
// plain `Error` with the HTTP status baked into the message string (no `.status`),
// and server.ts's toMessage() throws away everything but `.message`. So the only
// reliable place to classify is the catch site, on the original error object —
// which is exactly what toolError() does.

import { getFeedbackConfig } from "../config/config.js";

// Default repo for issues; overridable via config (feedback.repositoryUrl).
export const REPO_URL = "https://github.com/belal-elsabbagh-apex/copilot-mcp";

// Base marker for "known, user-actionable failure — not a bug". Throwing a subclass
// of this (or being one, like NotImplementedError) classifies as `expected` by identity,
// independent of message wording.
export class ExpectedError extends Error {}

// Message shapes that mean "expected" even on a plain Error (the codebase throws those):
//  - an upstream system returned a structured HTTP response (4xx OR 5xx): reaching the
//    external system and getting a status back is not an MCP bug, it's upstream/user state.
const HTTP_RESPONSE = /(?:->|failed)\s*\d{3}\b|\b\d{3}:\s/;
//  - a known validation / user-input / config guard fired.
const USER_PHRASE =
  /unknown profile|not found|is required|provide either|no config found|config from .* is invalid|not in profile|missing the|is missing|expected 'prod'/i;

/**
 * Decide whether a failure warrants suggesting a GitHub issue.
 * `expected` = user/config/upstream state (stay quiet); `unknown` = looks like our bug.
 */
export function classify(e: unknown): "expected" | "unknown" {
  if (e instanceof ExpectedError) return "expected";
  const msg = e instanceof Error ? e.message : String(e);
  if (HTTP_RESPONSE.test(msg)) return "expected";
  if (USER_PHRASE.test(msg)) return "expected";
  return "unknown";
}

const firstLine = (s: string): string => (s.split("\n")[0] ?? s).slice(0, 120);

const newIssueUrl = (repo: string, title: string, body: string, labels: string[]): string => {
  const q = `title=${encodeURIComponent(title)}&body=${encodeURIComponent(body)}&labels=${encodeURIComponent(labels.join(","))}`;
  return `${repo}/issues/new?${q}`;
};

/**
 * Build a prefilled GitHub new-issue URL (title + body + bug label). Carries only the
 * tool name, the error message, and the server version — no tool args, no credentials.
 */
export function issueUrl(opts: {
  tool: string;
  message: string;
  version: string;
  repositoryUrl?: string;
}): string {
  const base = (opts.repositoryUrl ?? REPO_URL).replace(/\/+$/, "");
  const title = `[tool: ${opts.tool}] ${firstLine(opts.message)}`;
  const body = [
    `**Tool:** \`${opts.tool}\``,
    `**Server version:** ${opts.version}`,
    "",
    "**Error:**",
    "```",
    opts.message,
    "```",
    "",
    "_Auto-suggested by copilot-mcp on an unexpected tool failure. Please add what you were doing._",
  ].join("\n");
  return newIssueUrl(base, title, body, ["bug"]);
}

export type McpIssueKind = "bug" | "feedback";

export interface McpIssuePayload {
  repo: string;
  title: string;
  body: string;
  labels: string[];
  url: string; // prefilled new-issue link — works with no GitHub tooling in the host
}

/**
 * Build a GitHub issue payload for a user-initiated report about this MCP server —
 * a bug OR general feedback (idea, friction, docs gap); no tool failure required.
 * BUILD ONLY — posting is the host's job (GitHub MCP / gh), or the user clicks `url`.
 * Deliberately independent of feedback.enabled: that flag only gates the automatic
 * nudge on failures, never an explicit ask.
 */
export function formatMcpIssue(opts: {
  kind: McpIssueKind;
  title: string;
  details: string;
  tool?: string;
  version: string;
  repositoryUrl?: string;
}): McpIssuePayload {
  const repo = (opts.repositoryUrl ?? REPO_URL).replace(/\/+$/, "");
  const labels = opts.kind === "bug" ? ["bug"] : ["enhancement"];
  const title = `[${opts.kind}] ${firstLine(opts.title)}`;
  const body = [
    ...(opts.tool ? [`**Tool:** \`${opts.tool}\``] : []),
    `**Server version:** ${opts.version}`,
    "",
    opts.details,
    "",
    "_Filed via copilot-mcp build_mcp_issue._",
  ].join("\n");
  return { repo, title, body, labels, url: newIssueUrl(repo, title, body, labels) };
}

type ErrorResponse = {
  isError: true;
  content: { type: "text"; text: string }[];
};

/**
 * Catch-site error helper. Returns the same shape as server.ts's err(), but for
 * failures classified as `unknown` (and when feedback is enabled) appends a
 * `reportIssue` block with a prefilled issue URL. Never throws — config/version
 * lookups fall back to safe defaults so the error path can't fail.
 */
export function toolError(tool: string, e: unknown, version: string): ErrorResponse {
  const message = e instanceof Error ? e.message : String(e);
  const payload: { error: string; reportIssue?: { message: string; url: string } } = {
    error: message,
  };

  if (classify(e) === "unknown") {
    let enabled = true;
    let repositoryUrl: string | undefined;
    try {
      const cfg = getFeedbackConfig();
      enabled = cfg.enabled;
      repositoryUrl = cfg.repositoryUrl;
    } catch {
      // config unavailable — keep defaults (enabled, default repo)
    }
    if (enabled) {
      payload.reportIssue = {
        message: "This looks like a bug in copilot-mcp — please report it.",
        url: issueUrl({ tool, message, version, ...(repositoryUrl ? { repositoryUrl } : {}) }),
      };
    }
  }

  return {
    isError: true,
    content: [{ type: "text", text: JSON.stringify(payload, null, 2) }],
  };
}
