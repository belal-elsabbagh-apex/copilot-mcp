// Single-file, validated configuration for the copilot MCP server.
//
// One file holds BOTH the Copilot BE credentials and the UiPath Orchestrator
// args: { copilot: {...}, uipath: {...} }. The path is taken from the
// COPILOT_MCP_CONFIG env var, else `config.local.json` next to the package.
//
// Migration fallback: if no single-file config exists, this assembles the same
// shape from a split `order-copy-credentials.json` + `uipath-config.json` (+ optional
// `overrides.json`) found in COPILOT_MCP_LOCAL_DIR, so the legacy `.planning/.local`
// setup keeps working — point COPILOT_MCP_LOCAL_DIR at it (see config.example.json).

import { readFileSync } from "node:fs";
import { join } from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { z } from "zod";

// ---- Schemas -------------------------------------------------------------

const EnvCredsSchema = z
  .object({
    be: z.string().min(1, "be (batch BE base URL) is required"),
    email: z.string().min(1, "email is required"),
    password: z.string().min(1, "password is required"),
    fe: z.string().optional(),
  })
  .passthrough();

const CopilotSchema = z
  .object({
    prod: EnvCredsSchema.optional(),
    pre_prod: EnvCredsSchema.optional(),
    profiles: z.record(z.object({ prod: EnvCredsSchema, pre_prod: EnvCredsSchema })).optional(),
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
    bearer: z
      .string({ required_error: "bearer is required" })
      .min(1, "bearer (UiPath PAT/token) must not be empty"),
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
    noteBucket: z.string().min(1).optional(),
    queueUrl: z.string().min(1).optional(),
    addQueueItemPath: z.string().min(1).optional(),
    serverUrlByEnv: z
      .object({ prod: z.string().optional(), pre_prod: z.string().optional() })
      .partial()
      .optional(),
  })
  .passthrough();

// Optional per-prodUid clone overrides (cross-env reference-uid remaps for the mirror).
const OrderOverrideSchema = z
  .object({
    typeUid: z.string().optional(),
    referredFacilityUid: z.string().optional(),
    specialityUid: z.string().optional(),
    orderNamesUids: z.array(z.string()).optional(),
    clinicProviderUid: z.string().optional(),
    injectCPTs: z.boolean().optional(),
    placeOfService: z.string().optional(),
  })
  .passthrough();

const ConfigSchema = z.object({
  copilot: CopilotSchema,
  uipath: UipathSchema,
  overrides: z.record(OrderOverrideSchema).optional(),
});

export type Env = "prod" | "pre_prod";
export type EnvCreds = z.infer<typeof EnvCredsSchema>;
export type UipathConfig = z.infer<typeof UipathSchema>;
export type CopilotConfig = z.infer<typeof CopilotSchema>;
export type OrderOverride = z.infer<typeof OrderOverrideSchema>;
export type Config = z.infer<typeof ConfigSchema>;
export interface ResolvedCreds {
  prod: EnvCreds;
  pre_prod: EnvCreds;
}

// ---- Loading -------------------------------------------------------------

// Resolved lazily (per loadConfig call) so the env var is honored even if it is set
// after this module is first evaluated — notably under the test runner.
const configPath = (): string =>
  process.env["COPILOT_MCP_CONFIG"] ?? new URL("../config.local.json", import.meta.url).pathname;
// Split legacy config lives in COPILOT_MCP_LOCAL_DIR (falls back to a `.local` dir
// next to the package for in-tree/dev use).
const LOCAL_DIR = process.env["COPILOT_MCP_LOCAL_DIR"];
const localFile = (name: string): string =>
  LOCAL_DIR ? join(LOCAL_DIR, name) : fileURLToPath(new URL(`../.local/${name}`, import.meta.url));
const LEGACY_CREDS = localFile("order-copy-credentials.json");
const LEGACY_UIPATH = localFile("uipath-config.json");
const LEGACY_OVERRIDES = localFile("overrides.json");

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

// Assemble the single-file shape from the split legacy files. overrides.json is optional.
function loadLegacy(): unknown {
  const copilot = readJson(LEGACY_CREDS, "Copilot credentials");
  const uipath = readJson(LEGACY_UIPATH, "UiPath config");
  let overrides: unknown;
  try {
    overrides = readJson(LEGACY_OVERRIDES, "overrides");
  } catch (e) {
    if (!(e instanceof ConfigMissing)) throw e;
  }
  return overrides ? { copilot, uipath, overrides } : { copilot, uipath };
}

let _config: Config | undefined;

// Read + validate the config once. Tries the single file first, then the legacy
// pair. Validation errors name the exact field + path so misconfig is obvious.
export function loadConfig(): Config {
  if (_config) return _config;
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
        `copilot-mcp: ${cfgPath} not found; using ${source}. Migrate to a single config — see config.example.json.`,
      );
    } catch (e2) {
      if (e2 instanceof ConfigMissing) {
        throw new Error(
          `No config found. Set COPILOT_MCP_CONFIG to a config file (see config.example.json), or COPILOT_MCP_LOCAL_DIR to a dir with the split legacy files. Missing: ${e2.message}`,
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
  _config = result.data;
  return _config;
}

// Clear the memoized config. Intended for tests that swap COPILOT_MCP_CONFIG.
export function resetConfigCache(): void {
  _config = undefined;
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

export const getOverrides = (): Record<string, OrderOverride> => loadConfig().overrides ?? {};

// Resolve a credential pair: a named profile (copilot.profiles[name]) or the
// top-level prod/pre_prod pair. Throws a clear error if the requested set is absent.
export function resolveCreds(profile?: string | null): ResolvedCreds {
  const { copilot } = loadConfig();
  if (profile) {
    const p = copilot.profiles?.[profile];
    if (!p)
      throw new Error(
        `unknown profile '${profile}' (available: ${Object.keys(copilot.profiles ?? {}).join(", ") || "none"})`,
      );
    return p;
  }
  if (!(copilot.prod && copilot.pre_prod)) {
    throw new Error("copilot.prod and copilot.pre_prod are required when no profile is given");
  }
  return { prod: copilot.prod, pre_prod: copilot.pre_prod };
}
