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
  // Select first so an unknown tag/section fails fast (no needless login).
  const chosen = selectSections(opts.sections, opts.tags, opts.emr);

  const creds = resolveCreds(opts.profile ?? null);
  const prod = makeClient(creds.prod.be, "prod");
  const pre = makeClient(creds.pre_prod.be, "pre_prod");
  await Promise.all([
    login(prod, creds.prod.email, creds.prod.password),
    login(pre, creds.pre_prod.email, creds.pre_prod.password),
  ]);

  // Every section's prod/pre-prod fetch is independent of every other section's —
  // run them all concurrently instead of paying each section's round-trip in turn.
  // Order is preserved (Promise.all resolves in input order) and one section's
  // failure never blocks the rest (caught per-section, same as the sequential form).
  const results: SectionResult[] = await Promise.all(
    chosen.map(async (section): Promise<SectionResult> => {
      try {
        const [pData, qData] = await Promise.all([
          fetchSection(prod, section),
          fetchSection(pre, section),
        ]);
        return { key: section.key, label: section.label, ...diffSection(section, pData, qData) };
      } catch (e) {
        return {
          key: section.key,
          label: section.label,
          kind: section.kind,
          equal: false,
          error: toMessage(e),
        };
      }
    }),
  );
  const withDiffs = results.filter((r) => !r.equal).length;

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
  // Select first so an unknown tag/section fails fast (no needless login).
  const chosen = selectSections(opts.sections, opts.tags, opts.emr);

  const creds = resolveCreds(opts.profile ?? null)[opts.env];
  const client = makeClient(creds.be, opts.env);
  await login(client, creds.email, creds.password);

  // Same independence as diffSettings — fetch every section concurrently.
  const sections: GetSettingsSection[] = await Promise.all(
    chosen.map(async (section): Promise<GetSettingsSection> => {
      const head = { key: section.key, label: section.label, tags: [...section.tags] };
      try {
        const raw = await fetchSection(client, section);
        const data = opts.normalized ? normalizeSectionValue(section, raw) : raw;
        return {
          ...head,
          kind: section.kind,
          ...(Array.isArray(data) ? { count: data.length } : {}),
          data,
        };
      } catch (e) {
        return { ...head, kind: section.kind, error: toMessage(e) };
      }
    }),
  );

  return {
    account: opts.profile ?? "(default)",
    env: opts.env,
    base: creds.be,
    sectionsFetched: chosen.length,
    sections,
  };
}
