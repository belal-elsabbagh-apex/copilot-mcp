import type { SettingsSection } from "../types.js";

export const documentRoutingRulesSection: SettingsSection = {
  key: "document-routing-rules",
  label: "Document routing rules",
  group: "documents",
  path: "/api/v1/settings/document-routing-rules?pageSize=200&pageNumber=0",
  kind: "list",
  envelope: "data",
  // no stable cross-env key -> content-set diff
};
