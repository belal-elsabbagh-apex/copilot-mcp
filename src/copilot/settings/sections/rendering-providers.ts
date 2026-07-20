import type { SettingsSection } from "../types.js";

export const renderingProvidersSection: SettingsSection = {
  key: "rendering-providers",
  label: "Rendering providers (clinic)",
  group: "providers",
  path: "/api/v1/clinic/ehr/providers",
  kind: "list",
  matchKey: "name",
};
