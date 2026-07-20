import type { SettingsSection } from "../types.js";

export const documentReviewingRulesAutoSection: SettingsSection = {
  key: "document-reviewing-rules-auto",
  label: "Document reviewing rules (Auto Review)",
  tags: ["documents"],
  path: "/api/v1/settings/document-reviewing-rules/Auto%20Review",
  kind: "list",
  envelope: "data",
};
