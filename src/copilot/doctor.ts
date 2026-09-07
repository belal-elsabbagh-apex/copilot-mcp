// doctor: probe the MCP server's connections to its external APIs and report what is
// reachable. Read-only — it authenticates against the Copilot BE (prod + pre-prod),
// fingerprints the clinic scope that session actually has, and makes one cheap
// authenticated UiPath Orchestrator call per env. Intended for setup/onboarding
// debugging ("are my creds + clinic + token + folders right?").

import type { Env } from "../config/config.js";
import { getUipath, resolveAuth } from "../config/config.js";
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

// Six independent probes: per env, an auth probe, a clinic-scope fingerprint, and an
// Orchestrator round-trip. Each check is independent; one failure never aborts the
// others, and config resolution happens inside the probes so one misconfigured env
// fails one check instead of the whole report.
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

  const TOTAL_CHECKS = ENVS.length * 3;
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
  ]);

  return { account, ok: checks.every((c) => c.ok), checks };
}
