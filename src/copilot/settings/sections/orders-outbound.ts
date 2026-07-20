import type { SettingsSection } from "../types.js";

export const ordersOutboundSection: SettingsSection = {
  key: "orders-outbound",
  label: "Outbound order settings",
  tags: ["orders"],
  path: "/api/v1/settings/orders/outbound",
  kind: "object",
};
