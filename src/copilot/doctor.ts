// doctor: probe the MCP server's connections to its external APIs and report what is
// reachable. Read-only — it authenticates against the Copilot BE (prod + pre-prod),
// fingerprints the clinic scope that session actually has, and makes one cheap
// authenticated UiPath Orchestrator call per env, and — when configured — Apex: which
// config source supplied the account, that login works, and that each env's folder
// resolves to an id. Intended for setup/onboarding debugging ("are my creds + clinic +
// token + folders right?").

import { apexLogin, resolveFolderId as resolveApexFolder } from "../apex/client.js";
import { ApexNotConfiguredError } from "../apex/errors.js";
import type { ApexConfig, Env } from "../config/config.js";
import { getUipath, resolveApex, resolveAuth } from "../config/config.js";
import { prop, type StepProgress } from "../shared/util.js";
import { listRecentJobs, resolveFolder } from "../uipath/uipath.js";
import { connect } from "./session.js";

export interface DoctorCheck {
  name: string;
  target: string;
  ok: boolean;
  detail: string; // success note, or the failure reason
}

export interface DoctorReport {
  account: string;
  ok: boolean; // true only if every check passed
  checks: DoctorCheck[];
}

const toMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// Run one check, capturing success detail or the error message — never throws.
async function probe(
  name: string,
  target: string,
  fn: () => Promise<string>,
): Promise<DoctorCheck> {
  try {
    return { name, target, ok: true, detail: await fn() };
  } catch (e) {
    return { name, target, ok: false, detail: toMessage(e) };
  }
}

const ENVS: readonly Env[] = ["prod", "pre_prod"];

// Apex is optional, so the report treats "not configured" as fine: nothing is set at all =
// one passing check saying so (UiPath/Copilot results are unaffected). A half-finished setup
// (some but not all of apiUrl/username/password) is a failing check, since that is a mistake,
// not a choice. Otherwise: config source, login, and one folder-resolution check per env.
// Returned as thunks so runDoctor can count them for progress before running any.
export function apexProbes(): (() => Promise<DoctorCheck>)[] {
  let cfg: ApexConfig;
  try {
    cfg = resolveApex();
  } catch (e) {
    if (!(e instanceof ApexNotConfiguredError)) throw e;
    const check: DoctorCheck = e.partial
      ? { name: "apex config", target: "apex", ok: false, detail: e.message }
      : {
          name: "apex config",
          target: "apex",
          ok: true,
          detail:
            "not configured (optional) — set the apex section of the config file, or APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD, to enable Apex",
        };
    return [async () => check];
  }
  const { sources } = cfg;
  return [
    () =>
      probe(
        "apex config",
        cfg.apiUrl,
        async () =>
          `apiUrl from ${sources.apiUrl}, username from ${sources.username}, password from ${sources.password}`,
      ),
    () =>
      probe("apex login", cfg.apiUrl, async () => {
        await apexLogin();
        return "login OK";
      }),
    ...ENVS.map(
      (env) => () =>
        probe(`apex ${env} folder`, cfg.apiUrl, async () => {
          const folder = await resolveApexFolder(env);
          return `folder '${folder.name}' -> ${folder.id}`;
        }),
    ),
  ];
}

// Six independent probes: per env, an auth probe, a clinic-scope fingerprint, and an
// Orchestrator round-trip — plus the Apex probes (apexProbes). Each check is independent;
// one failure never aborts the others, and config resolution happens inside the probes so
// one misconfigured env fails one check instead of the whole report.
export async function runDoctor(opts: {
  profile?: string | null;
  onProgress?: StepProgress;
}): Promise<DoctorReport> {
  const account = opts.profile ?? "(default)";
  const uipath = getUipath();
  // Probe target = the env's BE base when the config resolves; the env name otherwise,
  // so a misconfigured env still surfaces as one failed check rather than a throw here.
  const beOf = (env: Env): string => {
    try {
      return resolveAuth(opts.profile ?? null, env).be;
    } catch {
      return env;
    }
  };

  const apex = apexProbes();
  const TOTAL_CHECKS = ENVS.length * 3 + apex.length;
  let settled = 0;
  const track = (c: DoctorCheck): DoctorCheck => {
    settled++;
    opts.onProgress?.(settled, TOTAL_CHECKS, `${c.name}: ${c.ok ? "ok" : "failed"}`);
    return c;
  };

  const checks = await Promise.all([
    ...ENVS.map((env) =>
      probe(`copilot ${env} auth`, beOf(env), async () => {
        const s = await connect(env, opts.profile ?? null);
        return s.mode === "support"
          ? `support-account login OK (switched to activeClinicId=${s.activeClinicId})`
          : "direct login OK";
      }).then(track),
    ),
    // The documented PHI-free clinic fingerprint (/orders/locations differs per clinic)
    // — the only cheap proof the session acts as the intended clinic. `connect` is
    // cached, so this costs no extra login.
    ...ENVS.map((env) =>
      probe(`copilot ${env} clinic scope`, beOf(env), async () => {
        const s = await connect(env, opts.profile ?? null);
        const r = await s.client.req("GET", "/api/v1/orders/locations");
        if (r.status >= 400) {
          throw new Error(`GET /orders/locations failed ${r.status}: ${r.text.slice(0, 200)}`);
        }
        const locations = prop(r.data, "locations");
        const n = Array.isArray(locations) ? locations.length : 0;
        const scope = s.mode === "support" ? ` (activeClinicId=${s.activeClinicId})` : "";
        const empty =
          n === 0 && s.mode === "support"
            ? " — 0 locations; a clinic-less token also returns 0, but activeClinicId is set, so this clinic really has none"
            : "";
        return `${n} locations visible${scope}${empty}`;
      }).then(track),
    ),
    ...ENVS.map((env) =>
      probe(`uipath ${env} folder`, uipath.orchestratorUrl, async () => {
        const folder = resolveFolder(env);
        const jobs = await listRecentJobs(undefined, 1, folder);
        const authMode = uipath.oauth ? "oauth" : "bearer";
        return `reachable via ${authMode} (folder '${folder ?? "(default)"}', ${jobs.length} recent job${
          jobs.length === 1 ? "" : "s"
        } visible)`;
      }).then(track),
    ),
    ...apex.map((run) => run().then(track)),
  ]);

  return { account, ok: checks.every((c) => c.ok), checks };
}
