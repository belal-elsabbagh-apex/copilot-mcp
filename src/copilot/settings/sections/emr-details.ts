import type { SettingsSection } from "../types.js";

// emrDetailsSettings is keyed by the account's EMR type (e.g. NEXTGEN), so it is
// opt-in: only included when the caller passes `emr`.
export const emrSection = (emr: string): SettingsSection => ({
  key: "emr-details",
  label: `EMR details (${emr})`,
  group: "emr",
  path: `/api/v1/settings/emrDetailsSettings/${encodeURIComponent(emr)}`,
  kind: "object",
});
