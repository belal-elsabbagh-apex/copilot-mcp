// Read-only orchestration: diff_settings (cross-env) and get_settings (single-env).

import { resolveCreds } from "../../config/config.js";
import { prop } from "../../shared/util.js";
import { type HttpClient, login, makeClient } from "../copilot-client.js";
import { selectSections } from "./catalog.js";
import { diffList, diffObjects, normalizeSectionValue, stripNoise } from "./diff-engine.js";
import { toMessage } from "./internal.js";
import type {
  DiffSettingsOpts,
  DiffSettingsResult,
  GetSettingsOpts,
  GetSettingsResult,
  GetSettingsSection,
  SectionResult,
  SettingsSection,
} from "./types.js";

export const getEnvelope = (data: unknown, envelope?: string): unknown => {
  if (!envelope) return data;
  let cur = data;
  for (const part of envelope.split(".")) cur = prop(cur, part);
  return cur;
};

export async function fetchSection(client: HttpClient, section: SettingsSection): Promise<unknown> {
  if (section.derive) return section.derive(client);
  const r = await client.req("GET", section.path);
  if (r.status >= 400)
    throw new Error(`GET ${section.path} -> ${r.status}: ${r.text.slice(0, 200)}`);
  return getEnvelope(r.data, section.envelope);
}

function diffSection(
  section: SettingsSection,
  prodData: unknown,
  preData: unknown,
): Omit<SectionResult, "key" | "label"> {
  if (section.kind === "object") {
    const ignore = new Set(section.ignore ?? []);
    const diffs = diffObjects(stripNoise(prodData, ignore), stripNoise(preData, ignore));
    return { kind: "object", equal: diffs.length === 0, diffs };
  }
  const ld = diffList(
    Array.isArray(prodData) ? prodData : [],
    Array.isArray(preData) ? preData : [],
    section,
  );
  const equal = !ld.onlyInProd.length && !ld.onlyInPreProd.length && !ld.changed.length;
  return { kind: "list", equal, ...ld };
}

// Log into both envs for the account, fetch every selected settings section, and
// return the normalized prod<->pre-prod diff.
export async function diffSettings(opts: DiffSettingsOpts): Promise<DiffSettingsResult> {
  // Select first so an unknown group/section fails fast (no needless login).
  const chosen = selectSections(opts.sections, opts.groups, opts.emr);

  const creds = resolveCreds(opts.profile ?? null);
  const prod = makeClient(creds.prod.be, "prod");
  const pre = makeClient(creds.pre_prod.be, "pre_prod");
  await Promise.all([
    login(prod, creds.prod.email, creds.prod.password),
    login(pre, creds.pre_prod.email, creds.pre_prod.password),
  ]);

  const results: SectionResult[] = [];
  let withDiffs = 0;

  for (const section of chosen) {
    let result: SectionResult;
    try {
      const [pData, qData] = await Promise.all([
        fetchSection(prod, section),
        fetchSection(pre, section),
      ]);
      result = { key: section.key, label: section.label, ...diffSection(section, pData, qData) };
    } catch (e) {
      result = {
        key: section.key,
        label: section.label,
        kind: section.kind,
        equal: false,
        error: toMessage(e),
      };
    }
    if (!result.equal) withDiffs++;
    results.push(result);
  }

  return {
    account: opts.profile ?? "(default)",
    prodBase: creds.prod.be,
    preProdBase: creds.pre_prod.be,
    sectionsCompared: chosen.length,
    sectionsWithDiffs: withDiffs,
    sections: opts.includeUnchanged ? results : results.filter((s) => !s.equal),
  };
}

// Fetch the selected settings sections from ONE env — the single-env counterpart to
// diffSettings. Read-only. Stripped by default (same noise-stripping as diffSettings);
// normalized=false returns the raw payload with real UIDs/timestamps visible.
export async function getSettings(opts: GetSettingsOpts): Promise<GetSettingsResult> {
  // Select first so an unknown group/section fails fast (no needless login).
  const chosen = selectSections(opts.sections, opts.groups, opts.emr);

  const creds = resolveCreds(opts.profile ?? null)[opts.env];
  const client = makeClient(creds.be, opts.env);
  await login(client, creds.email, creds.password);

  const sections: GetSettingsSection[] = [];
  for (const section of chosen) {
    const head = { key: section.key, label: section.label, group: section.group };
    try {
      const raw = await fetchSection(client, section);
      const data = opts.normalized ? normalizeSectionValue(section, raw) : raw;
      sections.push({
        ...head,
        kind: section.kind,
        ...(Array.isArray(data) ? { count: data.length } : {}),
        data,
      });
    } catch (e) {
      sections.push({ ...head, kind: section.kind, error: toMessage(e) });
    }
  }

  return {
    account: opts.profile ?? "(default)",
    env: opts.env,
    base: creds.be,
    sectionsFetched: chosen.length,
    sections,
  };
}
