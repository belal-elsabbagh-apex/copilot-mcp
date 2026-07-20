import type { SettingsSection } from "../types.js";

export const fileManagerSection: SettingsSection = {
  key: "file-manager",
  label: "File manager settings",
  tags: ["file-manager"],
  path: "/api/v1/settings/fileManager",
  kind: "object",
  envelope: "data.fileManagerSettings",
};
