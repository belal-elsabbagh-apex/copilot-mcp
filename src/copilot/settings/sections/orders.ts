import { deriveOrderNames, ordersSyncer } from "../orders.js";
import type { SettingsSection } from "../types.js";

export const ordersSection: SettingsSection = {
  key: "orders",
  label: "Orders (across order types)",
  tags: ["orders"],
  path: "/api/v1/settings/orders/outbound/types/*/names", // crawled (see derive)
  kind: "list",
  matchKey: "name",
  derive: deriveOrderNames,
  sync: ordersSyncer,
};
