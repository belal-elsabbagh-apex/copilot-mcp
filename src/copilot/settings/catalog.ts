// Section catalog (derived from the captured HAR) — assembled from ./sections/*, one file
// per catalog key.

import { autoFinishDocumentRulesSection } from "./sections/auto-finish-document-rules.js";
import { clinicPayersSection } from "./sections/clinic-payers.js";
import { documentReviewingRulesAutoSection } from "./sections/document-reviewing-rules-auto.js";
import { documentReviewingRulesManualSection } from "./sections/document-reviewing-rules-manual.js";
import { documentRoutingRulesSection } from "./sections/document-routing-rules.js";
import { eligibilityVisibilitySection } from "./sections/eligibility-visibility.js";
import { emrSection } from "./sections/emr-details.js";
import { fileManagerSection } from "./sections/file-manager.js";
import { locationFaxInfoSection } from "./sections/location-fax-info.js";
import { locationGroupsSection } from "./sections/location-groups.js";
import { locationRegionsSection } from "./sections/location-regions.js";
import { locationsSection } from "./sections/locations.js";
import { ordersSection } from "./sections/orders.js";
import { ordersOutboundSection } from "./sections/orders-outbound.js";
import { ordersOutboundTypesSection } from "./sections/orders-outbound-types.js";
import { referredFacilitiesSection } from "./sections/referred-facilities.js";
import { referredProvidersSection } from "./sections/referred-providers.js";
import { referringEntitiesSection } from "./sections/referring-entities.js";
import { renderingProvidersSection } from "./sections/rendering-providers.js";
import { specialtiesSection } from "./sections/specialties.js";
import type { SettingSectionInfo, SettingsSection } from "./types.js";

export const SETTINGS_CATALOG: readonly SettingsSection[] = [
  fileManagerSection,
  ordersOutboundSection,
  ordersOutboundTypesSection,
  autoFinishDocumentRulesSection,
  eligibilityVisibilitySection,
  locationsSection,
  locationGroupsSection,
  locationRegionsSection,
  locationFaxInfoSection,
  clinicPayersSection,
  documentRoutingRulesSection,
  documentReviewingRulesAutoSection,
  documentReviewingRulesManualSection,
  referringEntitiesSection,
  renderingProvidersSection,
  specialtiesSection,
  referredProvidersSection,
  referredFacilitiesSection,
  ordersSection,
];

// Distinct tags across the catalog, in catalog order.
export const settingTags = (): string[] => [...new Set(SETTINGS_CATALOG.flatMap((s) => s.tags))];

export function catalogWithEmr(emr: string | undefined): SettingsSection[] {
  return emr ? [...SETTINGS_CATALOG, emrSection(emr)] : [...SETTINGS_CATALOG];
}

// Pick sections from the catalog, narrowing by tag (any match) and/or exact key. Both
// filters apply together (AND). Unknown tags/keys throw a clear, value-listing error.
export function selectSections(
  keys: string[] | undefined,
  tags: string[] | undefined,
  emr: string | undefined,
): SettingsSection[] {
  let picked = catalogWithEmr(emr);
  if (tags?.length) {
    const valid = new Set(picked.flatMap((s) => s.tags));
    const missing = tags.filter((t) => !valid.has(t));
    if (missing.length)
      throw new Error(`unknown tag(s): ${missing.join(", ")}. Known: ${[...valid].join(", ")}`);
    const want = new Set(tags);
    picked = picked.filter((s) => s.tags.some((t) => want.has(t)));
  }
  if (keys?.length) {
    const missing = keys.filter((k) => !catalogWithEmr(emr).some((s) => s.key === k));
    if (missing.length)
      throw new Error(
        `unknown section(s): ${missing.join(", ")}. Known: ${catalogWithEmr(emr)
          .map((s) => s.key)
          .join(", ")}${emr ? "" : " (pass `emr` to include emr-details)"}`,
      );
    const want = new Set(keys);
    picked = picked.filter((s) => want.has(s.key));
  }
  return picked;
}

// ---- Section listing (companion tool — pure, no network) ------------------

// List the catalog sections diff_settings can compare, optionally narrowed by a tag
// and/or an explicit set of section keys (both applied as AND). Pure: reads the
// static catalog, no creds/network. Unknown tag/keys throw a clear, value-listing error.
export function listSettingSections(opts: { tag?: string; sections?: string[]; emr?: string }): {
  tags: string[];
  sections: SettingSectionInfo[];
} {
  const all = catalogWithEmr(opts.emr);
  const allTags = [...new Set(all.flatMap((s) => s.tags))];
  if (opts.tag && !allTags.includes(opts.tag))
    throw new Error(`unknown tag: ${opts.tag}. Known: ${allTags.join(", ")}`);
  if (opts.sections?.length) {
    const known = new Set(all.map((s) => s.key));
    const missing = opts.sections.filter((k) => !known.has(k));
    if (missing.length)
      throw new Error(
        `unknown section(s): ${missing.join(", ")}. Known: ${[...known].join(", ")}` +
          (opts.emr ? "" : " (pass `emr` to include emr-details)"),
      );
  }
  const want = opts.sections?.length ? new Set(opts.sections) : undefined;
  const sections = all
    .filter((s) => (!opts.tag || s.tags.includes(opts.tag)) && (!want || want.has(s.key)))
    .map((s) => ({
      key: s.key,
      label: s.label,
      tags: [...s.tags],
      kind: s.kind,
      derived: !!s.derive,
      ...(s.matchKey ? { matchKey: s.matchKey } : {}),
    }));
  return { tags: allTags, sections };
}
