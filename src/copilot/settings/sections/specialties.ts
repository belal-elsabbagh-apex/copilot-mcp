import { deriveSpecialties, specialitiesSyncer } from "../specialities.js";
import type { SettingsSection } from "../types.js";

export const specialtiesSection: SettingsSection = {
  key: "specialties",
  label: "Specialties (across order types)",
  tags: ["orders"],
  path: "/api/v1/settings/orders/outbound/types/*/specialities", // crawled (see derive)
  kind: "list",
  matchKey: "name",
  derive: deriveSpecialties,
  sync: specialitiesSyncer,
};
