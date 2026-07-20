import type { SettingsSection } from "../types.js";

export const documentReviewingRulesManualSection: SettingsSection = {
  key: "document-reviewing-rules-manual",
  label: "Document reviewing rules (Manual Review)",
  tags: ["documents"],
  path: "/api/v1/settings/document-reviewing-rules/Manual%20Review",
  kind: "list",
  envelope: "data",
};
