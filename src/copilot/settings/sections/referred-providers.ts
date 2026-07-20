import { deriveReferredProviders, specialitiesSyncer } from "../specialities.js";
import type { SettingsSection } from "../types.js";

export const referredProvidersSection: SettingsSection = {
  key: "referred-providers",
  label: "Referred (referring) providers",
  tags: ["providers"],
  path: "/api/v1/settings/orders/outbound/types/*/specialities", // crawled (see derive)
  kind: "list",
  matchKey: "name",
  derive: deriveReferredProviders,
  sync: specialitiesSyncer,
};
