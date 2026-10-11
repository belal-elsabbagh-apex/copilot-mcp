// Apex domain errors. Both extend ExpectedError: an unconfigured server or a structured
// HTTP answer from Apex is user/upstream state, not a bug in this server, so classify()
// (mcp/feedback.ts) treats them as `expected` by identity.

import { ExpectedError } from "../shared/expected-error.js";
import { prop } from "../shared/util.js";

const DETAIL_CAP = 300;
const FORBIDDEN_HINT =
  " — the Apex account lacks the role for this action (e.g. jobs:download_artifacts, jobs:force_cancel); ask an Apex admin to grant it";

export class ApexNotConfiguredError extends ExpectedError {
  // Names of the fields neither the file nor the environment supplied.
  readonly missing: string[];
  // true = some, but not all, of the fields are set (a half-finished setup, as opposed
  // to Apex simply not being used) — `doctor` flags only that case.
  readonly partial: boolean;

  constructor(missing: { field: string; envVar: string }[], configured: number) {
    super(
      `Apex is not configured — missing ${missing.map((m) => m.field).join(", ")}. ` +
        `Set ${missing.map((m) => `apex.${m.field}`).join(", ")} in the config file, or the ` +
        `environment variable${missing.length === 1 ? "" : "s"} ${missing.map((m) => m.envVar).join(", ")} ` +
        "(the file wins per field; env vars only fill what the file leaves out).",
    );
    this.name = "ApexNotConfiguredError";
    this.missing = missing.map((m) => m.field);
    this.partial = configured > 0;
  }
}

// A non-2xx answer from the Apex API (or a login that returned no session cookie).
// `path` is relative to /api/v1 so messages and callers branch on `.status` / `.path`
// instead of regexing text. Message = Apex's own `detail`, already capped by the client.
export class ApexApiError extends ExpectedError {
  readonly status: number;
  readonly method: string;
  readonly path: string;

  constructor(method: string, path: string, status: number, detail: string) {
    super(`Apex ${method} ${path} -> ${status}: ${detail}`);
    this.name = "ApexApiError";
    this.status = status;
    this.method = method;
    this.path = path;
  }
}

// Message from an Apex error body: `detail` as a string, or pydantic's [{loc,msg}] list.
export function apexErrorDetail(body: unknown, status: number): string {
  const detail = prop(body, "detail");
  const text =
    typeof detail === "string"
      ? detail
      : Array.isArray(detail)
        ? detail.map(pydanticMessage).join("; ")
        : typeof body === "string"
          ? body
          : JSON.stringify(body);
  const capped = text.length > DETAIL_CAP ? `${text.slice(0, DETAIL_CAP)}…` : text;
  return status === 403 ? `${capped}${FORBIDDEN_HINT}` : capped;
}

function pydanticMessage(item: unknown): string {
  const msg = prop(item, "msg");
  const loc = prop(item, "loc");
  const where = Array.isArray(loc) ? loc.join(".") : "";
  const text = typeof msg === "string" ? msg : JSON.stringify(item);
  return where ? `${where}: ${text}` : text;
}
