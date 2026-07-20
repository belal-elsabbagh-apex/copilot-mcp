import type { SettingsSection } from "../types.js";

export const referringEntitiesSection: SettingsSection = {
  key: "referring-entities",
  label: "Referring entities",
  tags: ["providers"],
  path: "/api/v1/settings/referring-entities",
  kind: "list",
  envelope: "data",
  matchKey: "name",
};
