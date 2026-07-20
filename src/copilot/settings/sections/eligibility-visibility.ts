import type { SettingsSection } from "../types.js";

export const eligibilityVisibilitySection: SettingsSection = {
  key: "eligibility-visibility",
  label: "Eligibility visibility",
  tags: ["eligibility"],
  path: "/api/v1/settings/eligibility/eligibilityVisibility",
  kind: "object",
};
