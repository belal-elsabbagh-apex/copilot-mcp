import type { SettingsSection } from "../types.js";

export const autoFinishDocumentRulesSection: SettingsSection = {
  key: "auto-finish-document-rules",
  label: "Auto-finish document rules",
  group: "documents",
  path: "/api/v1/settings/auto-finish-document-rules",
  kind: "object",
};
