import { deriveReferredFacilities, specialitiesSyncer } from "../specialities.js";
import type { SettingsSection } from "../types.js";

export const referredFacilitiesSection: SettingsSection = {
  key: "referred-facilities",
  label: "Referred facilities",
  tags: ["providers"],
  path: "/api/v1/settings/orders/outbound/types/*/specialities", // crawled (see derive)
  kind: "list",
  matchKey: "name",
  derive: deriveReferredFacilities,
  sync: specialitiesSyncer,
};
