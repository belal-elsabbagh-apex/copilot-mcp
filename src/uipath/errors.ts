// The one error type for a non-2xx HTTP response anywhere in the UiPath domain
// (Orchestrator OData/REST calls in uipath.ts, the OAuth token endpoint in auth.ts).
// Carries enough structure (status/method/url/body) for callers to branch on `.status`
// — e.g. treating a 404 as "no such record" while letting a 401/500/etc. propagate —
// instead of regexing the message string.

import { ExpectedError } from "../mcp/feedback.js";

// Extends ExpectedError, not Error: reaching UiPath and getting a structured HTTP
// status back is upstream state, not a bug in this server, so classify()
// (mcp/feedback.ts) treats every UiPathApiError as `expected` by identity — same
// pattern as NotImplementedError (copilot/settings/sync-actions.ts).
export class UiPathApiError extends ExpectedError {
  readonly status: number;
  readonly method: string;
  readonly url: string;
  readonly body: unknown; // parsed JSON body when the response was JSON, else the raw text (capped)

  constructor(method: string, url: string, status: number, body: unknown) {
    super(`UiPath ${method} ${url} -> ${status}: ${snippet(body)}`);
    this.name = "UiPathApiError";
    this.status = status;
    this.method = method;
    this.url = url;
    this.body = body;
  }
}

function snippet(body: unknown): string {
  const text = typeof body === "string" ? body : JSON.stringify(body);
  return text.length > 300 ? `${text.slice(0, 300)}…` : text;
}

// True only for "the requested entity doesn't exist" — the one status a caller may
// fold into a null/empty result instead of propagating. Any other status (401, 500,
// a malformed request, ...) must propagate.
export function isNotFound(e: unknown): boolean {
  return e instanceof UiPathApiError && e.status === 404;
}
