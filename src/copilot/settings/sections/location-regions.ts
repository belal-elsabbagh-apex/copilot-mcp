import type { SettingsSection } from "../types.js";

export const locationRegionsSection: SettingsSection = {
  key: "location-regions",
  label: "Location regions",
  group: "locations",
  path: "/api/v1/settings/locations/regions",
  kind: "list",
  matchKey: "name",
};
