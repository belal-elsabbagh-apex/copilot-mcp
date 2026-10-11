// Apex domain errors. They extend ExpectedError: an unconfigured server or a structured
// HTTP answer from Apex is user/upstream state, not a bug in this server, so classify()
// (mcp/feedback.ts) treats them as `expected` by identity.

import { ExpectedError } from "../shared/expected-error.js";

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
