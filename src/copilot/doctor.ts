// doctor: probe the MCP server's connections to its external APIs and report what is
// reachable. Read-only — it logs into the Copilot BE (prod + pre-prod) and makes one
// cheap authenticated UiPath Orchestrator call per env. Intended for setup/onboarding
// debugging ("are my creds + token + folders right?").

import type { Env } from "../config/config.js";
import { getUipath, resolveCreds } from "../config/config.js";
import { listRecentJobs, resolveFolder } from "../uipath/uipath.js";
import { login, makeClient } from "./copilot-client.js";

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

// Probe Copilot BE (login) for both envs and UiPath Orchestrator (list 1 job) for both
// folders. Each check is independent; one failure never aborts the others.
export async function runDoctor(opts: { profile?: string | null }): Promise<DoctorReport> {
  const account = opts.profile ?? "(default)";

  // Config must resolve before anything else; surface that as the single failing check.
  let creds: ReturnType<typeof resolveCreds>;
  try {
    creds = resolveCreds(opts.profile ?? null);
  } catch (e) {
    return {
      account,
      ok: false,
      checks: [{ name: "config", target: "config", ok: false, detail: toMessage(e) }],
    };
  }
  const uipath = getUipath();

  const checks = await Promise.all([
    ...ENVS.map((env) =>
      probe(`copilot ${env} login`, creds[env].be, async () => {
        const client = makeClient(creds[env].be, env);
        await login(client, creds[env].email, creds[env].password);
        return "login OK";
      }),
    ),
    ...ENVS.map((env) =>
      probe(`uipath ${env} folder`, uipath.orchestratorUrl, async () => {
        const folder = resolveFolder(env);
        const jobs = await listRecentJobs(undefined, 1, folder);
        const authMode = uipath.oauth ? "oauth" : "bearer";
        return `reachable via ${authMode} (folder '${folder ?? "(default)"}', ${jobs.length} recent job${
          jobs.length === 1 ? "" : "s"
        } visible)`;
      }),
    ),
  ]);

  return { account, ok: checks.every((c) => c.ok), checks };
}
