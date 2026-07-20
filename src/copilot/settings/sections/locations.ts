import type { SettingsSection } from "../types.js";

export const locationsSection: SettingsSection = {
  key: "locations",
  label: "Locations",
  group: "locations",
  path: "/api/v1/settings/locations",
  kind: "list",
  matchKey: "name",
  ignore: ["email"], // dummy locations carry a randomized per-env email
};
