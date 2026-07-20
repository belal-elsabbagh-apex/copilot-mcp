import type { SettingsSection } from "../types.js";

export const locationGroupsSection: SettingsSection = {
  key: "location-groups",
  label: "Location groups",
  tags: ["locations"],
  path: "/api/v1/settings/locations/groups",
  kind: "list",
  matchKey: "name",
};
