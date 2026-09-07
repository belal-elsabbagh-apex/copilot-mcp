// Single-file, validated configuration for the copilot MCP server.
//
// One file holds BOTH the Copilot BE credentials and the UiPath Orchestrator
// args: { copilot: {...}, uipath: {...} }. The path is taken from the
// COPILOT_MCP_CONFIG env var, else `copilot-mcp.config.json` in the project root
// (cwd), else the older `config.local.json` name for existing setups.
//
// The config is live: loadConfig() cheaply stats the backing file(s) on every call
// and re-reads/re-validates only when they changed, so editing the file while the
// server is running takes effect on the next call — no restart needed. Subscribe via
// onConfigReload() to react to that (server.ts turns it into an MCP notification).
//
// Migration fallback: if no single-file config exists, this assembles the same
// shape from a split `order-copy-credentials.json` + `uipath-config.json` found in
// COPILOT_MCP_LOCAL_DIR, so the legacy `.planning/.local` setup keeps working —
// point COPILOT_MCP_LOCAL_DIR at it (see copilot-mcp.config.example.json).

import { existsSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { z } from "zod";

// ---- Schemas -------------------------------------------------------------

const DirectCredsSchema = z
  .object({
    be: z.string().min(1, "be (batch BE base URL) is required"),
    email: z.string().min(1, "email is required"),
    password: z.string().min(1, "password is required"),
    fe: z.string().optional(),
  })
  .passthrough();

// Support-account credentials for one env (prod today). Shared by every profile whose
// env entry carries a clinicUid.
const SupportCredsSchema = DirectCredsSchema;

// One env of one profile: EITHER a clinic pointer (support-account auth — credentials come
// from copilot.support[env]) OR a full per-account login (the pre-existing shape). `be`/`fe`
// stay allowed alongside clinicUid so a profile can override the support account's base URL.
const ProfileEnvSchema = z
  .object({
    clinicUid: z.string().uuid("clinicUid must be the clinic UUID from list_clinics").optional(),
    be: z.string().min(1).optional(),
    email: z.string().min(1).optional(),
    password: z.string().min(1).optional(),
    fe: z.string().optional(),
  })
  .passthrough()
  .refine(
    (e) => Boolean(e.clinicUid) || Boolean(e.be && e.email && e.password),
    "needs either clinicUid (support-account auth) or be+email+password (direct login)",
  );

const CopilotSchema = z
  .object({
    // Support account per env; only required for envs whose profiles use clinicUid.
    support: z
      .object({ prod: SupportCredsSchema.optional(), pre_prod: SupportCredsSchema.optional() })
      .optional(),
    prod: DirectCredsSchema.optional(),
    pre_prod: DirectCredsSchema.optional(),
    profiles: z
      .record(
        z.object({ prod: ProfileEnvSchema.optional(), pre_prod: ProfileEnvSchema.optional() }),
      )
      .optional(),
    // Persist sessions to disk (default true). false = in-memory only.
    sessionCache: z.boolean().optional(),
  })
  .passthrough();

const UipathOAuthSchema = z
  .object({
    clientId: z.string().min(1, "oauth.clientId is required"),
    clientSecret: z.string().min(1, "oauth.clientSecret is required"),
    // Falls back to a guess derived from orchestratorUrl (…/identity_/connect/token) when omitted.
    tokenUrl: z.string().url("oauth.tokenUrl must be a valid URL").optional(),
    scope: z.string().min(1).optional(),
  })
  .passthrough();

const UipathSchema = z
  .object({
    orchestratorUrl: z
      .string({ required_error: "orchestratorUrl is required" })
      .min(1, "orchestratorUrl must not be empty")
      .url("orchestratorUrl must be a valid URL")
      .refine(
        (u) => /\/\/cloud\.uipath\.com\/[^/]+\/[^/]+\/orchestrator_/.test(u),
        "orchestratorUrl must look like https://cloud.uipath.com/{org}/{tenant}/orchestrator_",
      ),
    // At least one of bearer/oauth is required — see the .refine() below. oauth is
    // tried first when present; bearer is also the runtime fallback if the OAuth
    // token request fails.
    bearer: z.string().min(1, "bearer (UiPath PAT/token) must not be empty").optional(),
    oauth: UipathOAuthSchema.optional(),
    folderPath: z.string().min(1).optional(),
    folderPathByEnv: z
      .object({ prod: z.string().min(1).optional(), pre_prod: z.string().min(1).optional() })
      .partial()
      .optional(),
    // Numeric UiPath folder ids (OrganizationUnitId) per env. Used by the queue
    // endpoints, which the tenant addresses by org-unit id rather than folder path.
    // Optional — defaults come from reference.ts (prod 231517 / pre_prod 434039).
    folderIdByEnv: z
      .object({ prod: z.string().min(1).optional(), pre_prod: z.string().min(1).optional() })
      .partial()
      .optional(),
    // Extra fields used ONLY by build_queue_item (optional for analyze-only setups):
    queueUrl: z.string().min(1).optional(),
    addQueueItemPath: z.string().min(1).optional(),
    serverUrlByEnv: z
      .object({ prod: z.string().optional(), pre_prod: z.string().optional() })
      .partial()
      .optional(),
  })
  .passthrough()
  .refine((cfg) => Boolean(cfg.bearer || cfg.oauth), {
    message: "uipath requires bearer, oauth, or both",
  });

// On-failure GitHub-issue suggestion (see feedback.ts). Optional; on by default.
const FeedbackSchema = z
  .object({
    enabled: z.boolean().optional(),
    repositoryUrl: z.string().url().optional(),
  })
  .passthrough();

const ConfigSchema = z.object({
  copilot: CopilotSchema,
  uipath: UipathSchema,
  feedback: FeedbackSchema.optional(),
});

export type Env = "prod" | "pre_prod";
export type EnvCreds = z.infer<typeof DirectCredsSchema>;
export type UipathConfig = z.infer<typeof UipathSchema>;
export type CopilotConfig = z.infer<typeof CopilotSchema>;
export type Config = z.infer<typeof ConfigSchema>;

// ---- Loading -------------------------------------------------------------

const DEFAULT_CONFIG_NAME = "copilot-mcp.config.json";
// Pre-rename default filename. Still honored (with a warning) so existing
// deployments relying on the cwd default keep working without a restart.
const OLD_DEFAULT_CONFIG_NAME = "config.local.json";
let warnedOldDefaultConfigName = false;

// Resolved lazily (per loadConfig call) so the env var is honored even if it is set
// after this module is first evaluated — notably under the test runner. When neither
// env var nor the new default filename is present, falls back to the old default
// filename for one release cycle.
const configPath = (): string => {
  const fromEnv = process.env["COPILOT_MCP_CONFIG"];
  if (fromEnv) return fromEnv;
  const preferred = join(process.cwd(), DEFAULT_CONFIG_NAME);
  if (existsSync(preferred)) return preferred;
  const legacy = join(process.cwd(), OLD_DEFAULT_CONFIG_NAME);
  if (existsSync(legacy)) {
    if (!warnedOldDefaultConfigName) {
      warnedOldDefaultConfigName = true;
      console.warn(
        `copilot-mcp: using legacy default config filename ${OLD_DEFAULT_CONFIG_NAME}; rename it to ${DEFAULT_CONFIG_NAME} (the new default) when convenient.`,
      );
    }
    return legacy;
  }
  return preferred;
};
// Split legacy config lives in COPILOT_MCP_LOCAL_DIR (falls back to a `.local` dir
// next to the package for in-tree/dev use).
const LOCAL_DIR = process.env["COPILOT_MCP_LOCAL_DIR"];
const localFile = (name: string): string =>
  LOCAL_DIR ? join(LOCAL_DIR, name) : fileURLToPath(new URL(`../.local/${name}`, import.meta.url));
const LEGACY_CREDS = localFile("order-copy-credentials.json");
const LEGACY_UIPATH = localFile("uipath-config.json");

const isMissing = (e: unknown): boolean =>
  e instanceof Error && "code" in e && (e as { code?: string }).code === "ENOENT";

function readJson(path: string | URL, label: string): unknown {
  let raw: string;
  try {
    raw = readFileSync(path, "utf8");
  } catch (e) {
    if (isMissing(e)) throw new ConfigMissing(`${label} not found at ${String(path)}`);
    throw new Error(`Could not read ${label} at ${String(path)}: ${(e as Error).message}`);
  }
  try {
    return JSON.parse(raw);
  } catch (e) {
    throw new Error(`${label} is not valid JSON (${String(path)}): ${(e as Error).message}`);
  }
}

class ConfigMissing extends Error {}

// Assemble the single-file shape from the split legacy files.
function loadLegacy(): unknown {
  const copilot = readJson(LEGACY_CREDS, "Copilot credentials");
  const uipath = readJson(LEGACY_UIPATH, "UiPath config");
  return { copilot, uipath };
}

let _config: Config | undefined;
let _statKey: string | undefined;

// mtime of whichever file(s) currently back the config, cheap to check (stat only,
// no read/parse) so loadConfig can compare against it on every call without the cost
// of re-reading JSON when nothing changed.
function tryMtime(path: string): number | undefined {
  try {
    return statSync(path).mtimeMs;
  } catch {
    return undefined;
  }
}

function computeStatKey(): string {
  const cfgPath = configPath();
  const single = tryMtime(cfgPath);
  if (single !== undefined) return `single:${cfgPath}:${single}`;
  const parts = [LEGACY_CREDS, LEGACY_UIPATH]
    .map((p) => `${p}:${tryMtime(p) ?? "missing"}`)
    .join("|");
  return `legacy:${parts}`;
}

type ConfigReloadListener = (info: { source: string }) => void;
const reloadListeners: ConfigReloadListener[] = [];

// Subscribe to successful config reloads (fired after the *first* load only on
// later changes, never on initial startup). Listeners must not throw; errors are
// swallowed so a bad listener can never break config loading. Returns an
// unsubscribe function.
export function onConfigReload(listener: ConfigReloadListener): () => void {
  reloadListeners.push(listener);
  return () => {
    const i = reloadListeners.indexOf(listener);
    if (i !== -1) reloadListeners.splice(i, 1);
  };
}

// Read + validate the config, live: every call cheaply stats the backing file(s)
// and only re-reads/re-validates when they changed, so editing the config while the
// server is running takes effect on the next call — no restart needed. Tries the
// single file first, then the legacy pair. Validation errors name the exact field +
// path so misconfig is obvious. If a previously-valid config is edited into
// something invalid, this throws (fails closed) rather than silently keeping the
// stale value — the next call retries the read.
export function loadConfig(): Config {
  const statKey = computeStatKey();
  if (_config && statKey === _statKey) return _config;

  const cfgPath = configPath();
  let parsed: unknown;
  let source: string;
  try {
    parsed = readJson(cfgPath, "config");
    source = cfgPath;
  } catch (e) {
    if (!(e instanceof ConfigMissing)) throw e;
    try {
      parsed = loadLegacy();
      source = `legacy split config (${LEGACY_CREDS} + ${LEGACY_UIPATH})`;
      console.warn(
        `copilot-mcp: ${cfgPath} not found; using ${source}. Migrate to a single config — see copilot-mcp.config.example.json.`,
      );
    } catch (e2) {
      if (e2 instanceof ConfigMissing) {
        throw new Error(
          `No config found. Set COPILOT_MCP_CONFIG to a JSON config file, or put a ${DEFAULT_CONFIG_NAME} in the working directory (${process.cwd()}), or set COPILOT_MCP_LOCAL_DIR to a dir with the split legacy files. Read the MCP resource copilot://reference/config-guide for the exact shape, field docs, and setup steps. Config edits are picked up automatically on the next call — no server restart needed. Verify with the doctor tool once written. Missing: ${e2.message}`,
        );
      }
      throw e2;
    }
  }
  const result = ConfigSchema.safeParse(parsed);
  if (!result.success) {
    const issues = result.error.issues
      .map((i) => `  - ${i.path.join(".") || "(root)"}: ${i.message}`)
      .join("\n");
    throw new Error(`Config from ${source} is invalid:\n${issues}`);
  }
  const isReload = _config !== undefined;
  _config = result.data;
  _statKey = statKey;
  if (isReload) {
    for (const listener of reloadListeners) {
      try {
        listener({ source });
      } catch {
        // a bad listener must never break config loading
      }
    }
  }
  return _config;
}

// Clear the memoized config. Intended for tests that swap COPILOT_MCP_CONFIG.
export function resetConfigCache(): void {
  _config = undefined;
  _statKey = undefined;
}

// Non-throwing config probe for startup guidance (server instructions / doctor).
// `ok:false` carries the same actionable message loadConfig would throw.
export function configStatus(): { ok: boolean; error?: string } {
  try {
    loadConfig();
    return { ok: true };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e) };
  }
}

export const getUipath = (): UipathConfig => loadConfig().uipath;

// Names of the credential profiles defined in the config (copilot.profiles). Used to
// drive prompt autocompletion; never throws (returns [] when the config is absent or
// has no profiles) so it is safe to call from tool/prompt wiring and completions.
export function listProfiles(): string[] {
  try {
    return Object.keys(loadConfig().copilot.profiles ?? {});
  } catch {
    return [];
  }
}

// On-failure feedback settings. Enabled by default; repositoryUrl is omitted unless
// configured (feedback.ts supplies the default repo). Reads through loadConfig, so it
// may throw if the config is absent — callers in the error path guard against that.
export function getFeedbackConfig(): { enabled: boolean; repositoryUrl?: string } {
  const fb = loadConfig().feedback;
  return {
    enabled: fb?.enabled ?? true,
    ...(fb?.repositoryUrl ? { repositoryUrl: fb.repositoryUrl } : {}),
  };
}

export type EnvAuth =
  | { mode: "support"; be: string; email: string; password: string; clinicUid: string }
  | { mode: "direct"; be: string; email: string; password: string };

// The support account for one env. Absent = this env has not adopted support accounts.
export function resolveSupport(env: Env): EnvCreds {
  const support = loadConfig().copilot.support?.[env];
  if (!support) {
    throw new Error(
      `copilot.support.${env} is required in the config for support-account (clinicUid) auth — add {be, email, password}`,
    );
  }
  return support;
}

// How to authenticate one profile in one env. A clinicUid on the env entry selects
// support-account auth (credentials from copilot.support[env]); otherwise the entry's own
// be/email/password are used, exactly as before support accounts existed. A profile carrying
// both wins with clinicUid — that is how an account is migrated env by env without deleting
// its old credentials.
export function resolveAuth(profile: string | null | undefined, env: Env): EnvAuth {
  const { copilot } = loadConfig();
  if (!profile) {
    if (!(copilot.prod && copilot.pre_prod)) {
      throw new Error("copilot.prod and copilot.pre_prod are required when no profile is given");
    }
    const t = copilot[env] as EnvCreds;
    return { mode: "direct", be: t.be, email: t.email, password: t.password };
  }
  const entry = copilot.profiles?.[profile];
  if (!entry) {
    throw new Error(
      `unknown profile '${profile}' (available: ${Object.keys(copilot.profiles ?? {}).join(", ") || "none"})`,
    );
  }
  const e = entry[env];
  if (!e) {
    throw new Error(
      `profile '${profile}' has no ${env} auth — add clinicUid (support account) or be/email/password (direct login)`,
    );
  }
  if (e.clinicUid) {
    const support = resolveSupport(env);
    return {
      mode: "support",
      be: e.be ?? support.be,
      email: support.email,
      password: support.password,
      clinicUid: e.clinicUid,
    };
  }
  return {
    mode: "direct",
    be: e.be as string,
    email: e.email as string,
    password: e.password as string,
  };
}

export const sessionCacheEnabled = (): boolean => loadConfig().copilot.sessionCache !== false;
