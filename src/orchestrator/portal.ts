// Portal helpers shared by every backend's pull_queue_item.

import type { ResolvedPortal } from "../mcp/reference.js";
import type { PortalRef } from "./types.js";

// Build a PHI-light filename slug from a queue item's member name.
export function memberFilename(sc: Record<string, unknown>): string {
  const s = (v: unknown): string => (typeof v === "string" ? v.trim().toLowerCase() : "");
  const first = s(sc["MemberFirstName"]);
  const last = s(sc["MemberLastName"]);
  let slug = `${first}-${last}`.replace(/^-|-$/g, "");
  if (!slug) slug = s(sc["MemberFullName"]) || "unknown";
  slug = slug.replace(/,/g, "").replace(/\s+/g, "-");
  return `${slug}.yaml`;
}

export const toPortalRef = (p: ResolvedPortal): PortalRef => ({
  matched: p.matched,
  key: p.key,
  portalDir: p.portalDir,
  buildArtifact: p.buildArtifact,
  family: p.family,
});
