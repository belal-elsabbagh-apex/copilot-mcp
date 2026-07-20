import type { SettingsSection } from "../types.js";

export const locationFaxInfoSection: SettingsSection = {
  key: "location-fax-info",
  label: "Location fax info",
  group: "locations",
  path: "/api/v1/settings/locationFaxInfo",
  kind: "list",
  matchKey: "location",
  ignore: ["clinicLogo"], // points at the per-env CDN host
};
