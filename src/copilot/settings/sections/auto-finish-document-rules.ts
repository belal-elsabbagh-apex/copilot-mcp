import type { SettingsSection } from "../types.js";

export const autoFinishDocumentRulesSection: SettingsSection = {
  key: "auto-finish-document-rules",
  label: "Auto-finish document rules",
  tags: ["documents"],
  path: "/api/v1/settings/auto-finish-document-rules",
  kind: "object",
};
