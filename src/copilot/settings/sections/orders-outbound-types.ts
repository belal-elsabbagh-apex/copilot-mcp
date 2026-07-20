import type { SettingsSection } from "../types.js";

export const ordersOutboundTypesSection: SettingsSection = {
  key: "orders-outbound-types",
  label: "Outbound order types",
  group: "orders",
  path: "/api/v1/settings/orders/outbound/types",
  kind: "list",
  matchKey: "name",
};
