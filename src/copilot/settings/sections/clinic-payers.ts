import type { SettingsSection } from "../types.js";

export const clinicPayersSection: SettingsSection = {
  key: "clinic-payers",
  label: "Clinic payers",
  tags: ["payers"],
  path: "/api/v1/settings/clinic-payers",
  kind: "list",
  envelope: "data",
  matchKey: "Name",
  ignore: ["providerId"],
};
