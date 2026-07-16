// Curated, hand-maintained reference data shared by the MCP resources and the
// queue/sweep tools. This is the STATIC half of the "hybrid" model: stable facts
// (portal registry, UiPath folder ids, schemas, contracts, safety rules) are
// bundled here rather than fetched live, while anything that changes per-run
// (queue items, jobs, orders) is a tool.
//
// Sources (keep in sync when the sibling repo changes):
//   - ../RPAPlaywright/optum/docs/uipath-production-queues.md   (queue ids, accounts, folders)
//   - ../RPAPlaywright/optum/CLAUDE.md                          (portal families, build artifacts)
//   - ../RPAPlaywright/optum/.planning/pull_queue_item_by_id.py (QUEUE_TO_PORTAL aliases)
//   - ../RPAPlaywright/optum/packages/core/src/schema.ts        (SpecificContent schema)
//   - ../RPAPlaywright/optum/.claude/skills/submissions-contract (result.json contract, dry-run gate)

import type { Env } from "../config/config.js";

// ---- UiPath folders (per env) --------------------------------------------

export interface UiPathFolder {
  name: string;
  organizationUnitId: string; // numeric folder id (fid / X-UIPATH-OrganizationUnitId)
  folderKey: string; // GUID FolderKey
  realBeCalls: boolean; // prod fires real BE notifications; dev clone dry-runs them
}

export const UIPATH_FOLDERS: Record<Env, UiPathFolder> = {
  prod: {
    name: "Authorization",
    organizationUnitId: "231517",
    folderKey: "9ecc9792-b3f1-4c3b-bae9-51b6f31384a5",
    realBeCalls: true,
  },
  pre_prod: {
    name: "Authorization Dev Clone",
    organizationUnitId: "434039",
    folderKey: "db6a5e70-7d8b-4113-afbc-a425eb81f015",
    realBeCalls: false,
  },
};

// ---- Portal registry ------------------------------------------------------

export type PortalFamily =
  | "Availity 278"
  | "Availity AuthAI"
  | "Aerial"
  | "QuickCap"
  | "Medpoint"
  | "Medhok"
  | "EZ-NET"
  | "Cedar Gate"
  | "Standalone";

// Total shape — no nullable/optional fields. Sentinels carry "unknown": "" for
// strings, 0 for queue ids. Callers test those sentinels instead of null.
export interface PortalEntry {
  key: string; // canonical queue name (uppercase), e.g. "PMG"
  aliases: readonly string[]; // other names that map to this portal (uppercase)
  portalDir: string; // directory under the optum repo
  buildArtifact: string; // <name>-auth.cjs ("" = no Playwright build yet)
  family: PortalFamily;
  account: string; // KAFRI | OSSM | SCLC | "" (unknown/empty)
  submitQueueDefId: number; // 0 = unknown
  syncQueueDefId: number; // 0 = unknown
  volume: string; // prod volume note from the queue doc ("" = unknown)
}

// One row per portal. submit/sync queue ids + accounts from uipath-production-queues.md;
// families + build artifacts from CLAUDE.md; aliases from the QUEUE_TO_PORTAL map.
export const PORTALS: PortalEntry[] = [
  {
    key: "PMG",
    aliases: [],
    portalDir: "PMG Through Aerial Auth Submit with Playwright",
    buildArtifact: "pmg-auth.cjs",
    family: "Aerial",
    account: "KAFRI",
    submitQueueDefId: 38592,
    syncQueueDefId: 39491,
    volume: "1,019 (highest)",
  },
  {
    key: "OPTUM CARE NETWORK",
    aliases: ["OPTUM"],
    portalDir: "Regal Auth Submit with Playwright",
    buildArtifact: "",
    family: "Standalone",
    account: "OSSM",
    submitQueueDefId: 118705,
    syncQueueDefId: 115325,
    volume: "821",
  },
  {
    key: "SPMG",
    aliases: ["SDPMG"],
    portalDir: "SDPMG Through Cedar Gate Auth Submit with Playwright",
    buildArtifact: "sdpmg-auth.cjs",
    family: "Cedar Gate",
    account: "KAFRI",
    submitQueueDefId: 43446,
    syncQueueDefId: 40372,
    volume: "212",
  },
  {
    key: "REGAL",
    aliases: [],
    portalDir: "Regal Auth Submit with Playwright",
    buildArtifact: "regal-auth.cjs",
    family: "Standalone",
    account: "OSSM",
    submitQueueDefId: 118708,
    syncQueueDefId: 113961,
    volume: "206",
  },
  {
    key: "IHP",
    aliases: ["CCI", "CCIPA"],
    portalDir: "CCI-IHP Through Medpoint Auth Submit with Playwright",
    buildArtifact: "cci-ihp-auth.cjs",
    family: "Medpoint",
    account: "KAFRI",
    submitQueueDefId: 43449,
    syncQueueDefId: 95893,
    volume: "139",
  },
  {
    key: "PCA",
    aliases: [],
    portalDir: "PCA Through Aerial Auth Submit with Playwright",
    buildArtifact: "pca-auth.cjs",
    family: "Aerial",
    account: "KAFRI",
    submitQueueDefId: 43448,
    syncQueueDefId: 50071,
    volume: "104",
  },
  {
    key: "SDP",
    aliases: ["SAN DIEGO"],
    portalDir: "San Diego Through QuickCap Auth Submit with Playwright",
    buildArtifact: "sandiego-auth.cjs",
    family: "QuickCap",
    account: "KAFRI",
    submitQueueDefId: 43445,
    syncQueueDefId: 62812,
    volume: "98",
  },
  {
    key: "MOLINA",
    aliases: [],
    portalDir: "Molina Through Availity Auth Submit with Playwright",
    buildArtifact: "molina-auth.cjs",
    family: "Availity AuthAI",
    account: "KAFRI",
    submitQueueDefId: 78843,
    syncQueueDefId: 78795,
    volume: "active",
  },
  {
    key: "BSC",
    aliases: [],
    portalDir: "BSC Through Medhok Auth Submit with Playwright",
    buildArtifact: "bsc-auth.cjs",
    family: "Medhok",
    account: "KAFRI",
    submitQueueDefId: 78203,
    syncQueueDefId: 81420,
    volume: "75",
  },
  {
    key: "CHG",
    aliases: [],
    portalDir: "CHG Auth Submit with playwright",
    buildArtifact: "chg-auth.cjs",
    family: "Standalone",
    account: "KAFRI",
    submitQueueDefId: 35658,
    syncQueueDefId: 40360,
    volume: "active",
  },
  {
    key: "RIOS",
    aliases: [],
    portalDir: "Rios Through QuickCap Auth Submit with Playwright",
    buildArtifact: "rios-auth.cjs",
    family: "QuickCap",
    account: "KAFRI",
    submitQueueDefId: 0,
    syncQueueDefId: 0,
    volume: "",
  },
  {
    key: "AETNA",
    aliases: [],
    portalDir: "Aetna Through Availity Auth Submit with Playwright",
    buildArtifact: "aetna-auth.cjs",
    family: "Availity 278",
    account: "",
    submitQueueDefId: 107164,
    syncQueueDefId: 120205,
    volume: "0",
  },
  {
    key: "ANTHEM",
    aliases: [],
    portalDir: "Anthem Blue Cross Through Availity Auth Submit with Playwright",
    buildArtifact: "anthem-auth.cjs",
    family: "Availity 278",
    account: "",
    submitQueueDefId: 102365,
    syncQueueDefId: 120202,
    volume: "0",
  },
  {
    key: "HEALTHNET",
    aliases: ["HEALTH NET"],
    portalDir: "HealthNet Through Availity Auth Submit with Playwright",
    buildArtifact: "healthnet-auth.cjs",
    family: "Availity 278",
    account: "",
    submitQueueDefId: 0,
    syncQueueDefId: 0,
    volume: "",
  },
  {
    key: "HUMANA",
    aliases: [],
    portalDir: "Humana Through Availity Auth Submit with Playwright",
    buildArtifact: "humana-auth.cjs",
    family: "Availity AuthAI",
    account: "",
    submitQueueDefId: 0,
    syncQueueDefId: 122378,
    volume: "",
  },
  {
    key: "CHMP",
    aliases: [],
    portalDir: "CHMP Through Availity Auth Submit with Playwright",
    buildArtifact: "chmp-auth.cjs",
    family: "Availity AuthAI",
    account: "",
    submitQueueDefId: 0,
    syncQueueDefId: 0,
    volume: "",
  },
  {
    key: "SCAN",
    aliases: [],
    portalDir: "Scan Health Plan Through Availity Auth Submit with Playwright",
    buildArtifact: "scan-auth.cjs",
    family: "Availity AuthAI",
    account: "",
    submitQueueDefId: 0,
    syncQueueDefId: 0,
    volume: "",
  },
  {
    key: "CENTRAL",
    aliases: [],
    portalDir: "Central Healthcare Through Availity Auth Submit with Playwright",
    buildArtifact: "central-auth.cjs",
    family: "Availity AuthAI",
    account: "",
    submitQueueDefId: 0,
    syncQueueDefId: 0,
    volume: "",
  },
  {
    key: "CIGNA",
    aliases: [],
    portalDir: "Cigna Auth Submit with Playwright",
    buildArtifact: "cigna-auth.cjs",
    family: "Standalone",
    account: "",
    submitQueueDefId: 106485,
    syncQueueDefId: 0,
    volume: "0",
  },
  {
    key: "IEHP",
    aliases: [],
    portalDir: "IEHP Auth Submit with Playwright",
    buildArtifact: "iehp-auth.cjs",
    family: "Standalone",
    account: "",
    submitQueueDefId: 121163,
    syncQueueDefId: 0,
    volume: "0",
  },
  {
    key: "ALIGNMENT",
    aliases: ["ALHC"],
    portalDir: "Alignment Auth Submit with Playwright",
    buildArtifact: "alignment-auth.cjs",
    family: "Standalone",
    account: "",
    submitQueueDefId: 52747,
    syncQueueDefId: 81423,
    volume: "0",
  },
  {
    key: "IHH",
    aliases: [],
    portalDir: "IHH Through EZ-NET Auth Submit with Playwright",
    buildArtifact: "ihh-auth.cjs",
    family: "EZ-NET",
    account: "",
    submitQueueDefId: 43447,
    syncQueueDefId: 42055,
    volume: "0",
  },
  {
    key: "CALOPTIMA",
    aliases: [],
    portalDir: "CalOptima Auth Submit with Playwright",
    buildArtifact: "caloptima-auth.cjs",
    family: "Standalone",
    account: "",
    submitQueueDefId: 0,
    syncQueueDefId: 0,
    volume: "",
  },
  {
    key: "CAREFIRST",
    aliases: [],
    portalDir: "CareFirst Auth Submit with Playwright",
    buildArtifact: "carefirst-auth.cjs",
    family: "Standalone",
    account: "",
    submitQueueDefId: 0,
    syncQueueDefId: 0,
    volume: "",
  },
  {
    key: "UHC",
    aliases: [],
    portalDir: "UHC Auth Submit with Playwright",
    buildArtifact: "uhc-auth.cjs",
    family: "Standalone",
    account: "",
    submitQueueDefId: 0,
    syncQueueDefId: 0,
    volume: "",
  },
];

// Strip the trailing " auth (sync|submit) queue" suffix from a QueueDefinitions.Name
// (e.g. "SCAN auth submit queue" -> "SCAN", "MOLINA auth sync queue" -> "MOLINA").
const QUEUE_NAME_SUFFIX_RE = /\s+auth\s+(?:sync|submit)\s+queue\s*$/i;

export function normalizeQueueName(raw: string): string {
  return raw.replace(QUEUE_NAME_SUFFIX_RE, "").trim();
}

// A portal-lookup result. `matched` discriminates a hit from a miss, so callers
// never deal with null — an unmatched lookup carries empty/zero sentinels.
export interface ResolvedPortal extends PortalEntry {
  matched: boolean;
}

const NO_PORTAL: PortalEntry = {
  key: "",
  aliases: [],
  portalDir: "",
  buildArtifact: "",
  family: "Standalone",
  account: "",
  submitQueueDefId: 0,
  syncQueueDefId: 0,
  volume: "",
};

// Resolve a portal from a queue name (canonical or alias, case-insensitive, with or
// without the "auth ... queue" suffix). Always returns a total ResolvedPortal —
// check `.matched` to tell a hit from a miss.
export function resolvePortal(queueName: string): ResolvedPortal {
  const key = normalizeQueueName(queueName).toUpperCase();
  const hit = key ? PORTALS.find((p) => p.key === key || p.aliases.includes(key)) : undefined;
  return hit ? { ...hit, matched: true } : { ...NO_PORTAL, matched: false };
}

// Resolve a portal from a QueueDefinitionId (submit or sync). Same total contract.
export function resolvePortalByDefId(id: number): ResolvedPortal {
  const hit = id
    ? PORTALS.find((p) => p.submitQueueDefId === id || p.syncQueueDefId === id)
    : undefined;
  return hit ? { ...hit, matched: true } : { ...NO_PORTAL, matched: false };
}

// Numeric OrganizationUnitId for an env (the fid the QueueItems endpoints expect).
export const folderIdFor = (env: Env): string => UIPATH_FOLDERS[env].organizationUnitId;

// Folder name (X-UIPATH-FolderPath) for an env.
export const folderNameFor = (env: Env): string => UIPATH_FOLDERS[env].name;

// Map a numeric folder id (fid / OrganizationUnitId) back to an env. Defaults to
// pre_prod (the dry-run-safe folder) when the id isn't one we know.
export function envForFolderId(folderId: string): Env {
  for (const env of Object.keys(UIPATH_FOLDERS) as Env[]) {
    if (UIPATH_FOLDERS[env].organizationUnitId === folderId) return env;
  }
  return "pre_prod";
}

// Folder name for a numeric folder id ("" if unknown — header is then omitted).
export function folderNameForId(folderId: string): string {
  for (const env of Object.keys(UIPATH_FOLDERS) as Env[]) {
    if (UIPATH_FOLDERS[env].organizationUnitId === folderId) return UIPATH_FOLDERS[env].name;
  }
  return "";
}

// ---- SpecificContent (queue-item) schema ---------------------------------
// Ported from packages/core/src/schema.ts (baseConfigSchema). Documentation only —
// portals extend this with portal-specific fields.

export const QUEUE_ITEM_SCHEMA = {
  description:
    "Base SpecificContent fields shared across all portal auth-submit queue items. " +
    "Portal-specific schemas extend this. IsApproved is forced false on every test pull.",
  groups: {
    member: [
      "MemberID*",
      "MemberFirstName",
      "MemberLastName",
      "MemberFullName",
      "MemberDOB",
      "MemberPhone",
      "MemberFax",
    ],
    provider: [
      "ProviderFirstName",
      "ProviderLastName",
      "ProviderFullName",
      "ProviderNPI*",
      "ProviderPhone",
      "ProviderFax",
      "ProviderMail",
      "ProviderID",
    ],
    referredTo: ["ReferredToNPI*", "ReferredToPhone", "ReferredToAddress"],
    service: [
      "Specialty*",
      "Facility",
      "Location",
      "Diagnoses* (JSON array string)",
      "ServiceCode* (JSON array string)",
      "DescriptionOfService (JSON array string)",
      "Units (JSON array string)",
    ],
    flags: ["IsApproved (boolean, always false in tests)", "IsUrgent (boolean)"],
    metadata: [
      "orderUid",
      "placeOfService",
      "appointmentDate",
      "OfficeComments",
      "clinicalDocPath",
      "retryCount",
    ],
    uipath: ["callbackContext", "token", "serverURL", "queueUrl", "automationId", "account"],
  },
  notes: "* = required. Diagnoses/ServiceCode/Units are JSON-array strings (e.g. '[\"I77.9\"]').",
} as const;

// ---- result.json output contract -----------------------------------------
// Ported from the submissions-contract skill. The contract every portal .cjs writes.

export const RESULT_CONTRACT = {
  description:
    "Output contract every portal automation writes (result.json), read by UiPath to " +
    "decide retry / business-exception / success.",
  result: ["SUBMITTED", "NOT_SUBMITTED", "FAILURE"],
  fields: {
    result: "REQUIRED — SUBMITTED | NOT_SUBMITTED | FAILURE",
    screenshotPath: "REQUIRED — path to final screenshot",
    hasInvalidData: "OPTIONAL — true only for data-validation failures",
    reason: "OPTIONAL — e.g. 'MemberId', 'Facility' (with hasInvalidData)",
    invalidCodes: "OPTIONAL — bad ICD/CPT codes, or [] for member/facility",
    referenceNumber: "OPTIONAL — portal auth number (SUBMITTED only)",
    postSubmit: "OPTIONAL — post-submit page diagnostics (see postSubmit shape)",
    error: "OPTIONAL — only on FAILURE; cleared if a fallback upgrades to SUBMITTED",
  },
  postSubmit: {
    url: "post-submit page URL",
    title: "page title",
    visibleTextPrefix: "leading visible text",
    htmlPath: "saved HTML path",
    screenshotPath: "saved screenshot path",
    approval: "OPTIONAL — APPROVED | PENDING | UNKNOWN",
    approvalSource: "OPTIONAL — url | text",
    approvalEvidence: "OPTIONAL — matched evidence string",
  },
} as const;

// ---- Copilot order lifecycle ---------------------------------------------
// Documents the statuses the mirror walks through and the error codes it tolerates.

export const ORDER_LIFECYCLE = {
  statuses: {
    drafted: "fresh empty draft (POST /api/v1/orders)",
    new: "draft with some fields set",
    incomplete: "missing facility/type/orderNames — stuck; not cloneable",
    pending: "pre-forReview working state",
    forReview: "ready-to-submit milestone (after /process + note upload)",
    inProgress: "submitted (POST /submit) — picked up for orchestration",
  },
  errorCodes: {
    E6001:
      "status must be in drafted/new/incomplete/pending — i.e. already at forReview (treated as success by the mirror)",
  },
  notes:
    "A prod order clones to forReview only if it has referredFacility + orderType + orderNames; otherwise it sticks at 'incomplete'.",
} as const;

// ---- Safety rules ---------------------------------------------------------
// The invariants that keep test/dev activity from touching prod.

export const SAFETY_RULES = {
  rules: [
    "IsApproved is ALWAYS false on pulled/test queue items — never submit a real auth from a test run.",
    "Never persist PHI (member names, DOBs, IDs, phones) — redact to placeholders in filenames/docs.",
    "Never commit credentials — bearer/passwords stay in local config, read at runtime.",
    "Dry-run gate: the 'Authorization Dev Clone' folder (434039) skips all BE notifications; only 'Authorization' (231517) fires real BE calls.",
    "This MCP never writes to prod: queue-item POSTs/deletes and job starts are pre_prod-only (dev clone 434039, schema-enforced) and it never repins releases.",
    "Every posted queue item passes the test-safety guard: IsApproved forced false, serverURL/queueUrl pinned to the configured pre-prod values, <TO-FILL> placeholders rejected.",
  ],
} as const;

// ---- Config setup guide ----------------------------------------------------
// Everything an agent needs to write a working config from scratch when the
// server starts without one. Mirrors copilot-mcp.config.example.json + config.ts schema —
// keep the three in sync. Placeholders only; never put real values here.

export const CONFIG_GUIDE = {
  howItLoads: [
    "The server reads ONE JSON config file, checked in this order: the file at $COPILOT_MCP_CONFIG; else copilot-mcp.config.json in the server's working directory (falling back to the older config.local.json name if that's what exists); else the legacy split files in $COPILOT_MCP_LOCAL_DIR (order-copy-credentials.json + uipath-config.json).",
    "The config is live: every tool call cheaply checks the file's mtime and re-reads + re-validates it when it changed, so editing the file takes effect on the next call — no server restart needed, ever, not just before the first successful load. The server sends an MCP logging notification (notifications/message) each time it picks up a change.",
    "Validation errors name the exact field and path; a missing file lists all three lookup options. If an already-loaded config is edited into something invalid, calls fail with that same validation error (fail closed) rather than silently keeping the old values.",
  ],
  steps: [
    "1. Ask the user for the values marked <ASK-USER> below — credentials and tokens cannot be guessed and must never be invented.",
    "2. Write the JSON file (template below) somewhere private and gitignored, e.g. ~/.config/copilot-mcp/config.json or copilot-mcp.config.json next to the server.",
    "3. If not using the default copilot-mcp.config.json location, set COPILOT_MCP_CONFIG to the file's absolute path in the MCP host's server config (the `env` block of this server's entry).",
    "4. Call the doctor tool (profile=<one of copilot.profiles>) to verify: it logs into the Copilot BE for both envs and probes both UiPath folders.",
  ],
  template: {
    copilot: {
      profiles: {
        "<account-name>": {
          prod: {
            be: "https://prod-be-batch.ehrcopilotbe.com",
            email: "<ASK-USER: prod physician login email>",
            password: "<ASK-USER: prod password>",
            fe: "https://copilot.apexmedical.ai",
          },
          pre_prod: {
            be: "https://pre-prod-be-batch.ehrcopilotbe.com",
            email: "<ASK-USER: pre-prod physician login email>",
            password: "<ASK-USER: pre-prod password>",
            fe: "https://pre-prod-copilot.apexmedicalai.com",
          },
        },
      },
    },
    uipath: {
      orchestratorUrl: "https://cloud.uipath.com/<org>/<tenant>/orchestrator_",
      bearer: "<ASK-USER: UiPath PAT with OR.Jobs/OR.Queues read scope> (optional if oauth is set)",
      oauth: {
        clientId:
          "<ASK-USER: UiPath external application client id> (optional block; alternative/fallback pair to bearer)",
        clientSecret: "<ASK-USER: UiPath external application client secret>",
        tokenUrl:
          "https://cloud.uipath.com/<org>/identity_/connect/token (optional, guessed if omitted)",
      },
      folderPathByEnv: { prod: "Authorization", pre_prod: "Authorization Dev Clone" },
    },
  },
  fields: {
    "copilot.profiles":
      "Named credential sets ('profiles') — every Copilot tool takes a required `profile` arg that must match a key here. Each profile needs BOTH prod and pre_prod creds.",
    "copilot.prod / copilot.pre_prod":
      "Optional top-level fallback creds used only when a caller could pass no profile; profiles are the normal path.",
    "uipath.orchestratorUrl":
      "Tenant Orchestrator base — https://cloud.uipath.com/{org}/{tenant}/orchestrator_",
    "uipath.bearer":
      "Single global UiPath PAT/token; UiPath tools take env but no profile. Optional if uipath.oauth is set — still used as the runtime fallback if the OAuth token request fails, so it's fine (and recommended) to keep both configured.",
    "uipath.oauth":
      "Optional: OAuth client-credentials alternative to bearer. Tried first when present; requires clientId + clientSecret. At least one of bearer/oauth is required.",
    "uipath.oauth.tokenUrl":
      "Identity-server token endpoint. Optional — defaults to https://cloud.uipath.com/{org}/identity_/connect/token derived from orchestratorUrl; set explicitly if that guess is wrong (e.g. non-standard/on-prem identity server).",
    "uipath.oauth.scope": "Optional OAuth scope string (e.g. 'OR.Jobs OR.Queues OR.Execution').",
    "uipath.folderPathByEnv":
      "Folder per env; defaults exist for the Authorization folders if omitted.",
    "uipath.folderIdByEnv":
      "Optional numeric OrganizationUnitId per env (defaults: prod 231517 / pre_prod 434039).",
    "uipath.queueUrl / uipath.addQueueItemPath / uipath.serverUrlByEnv":
      "Only needed for build_queue_item / add_queue_item; the safety guard pins posted items to these pre-prod values.",
    overrides: "Optional per-prodUid clone remaps (cross-env reference uids) for clone_order.",
    feedback:
      "Optional on-failure GitHub-issue feedback; on by default (feedback.enabled=false disables). When a tool fails in a way that looks like a bug in this server, the error payload carries reportIssue.url — a prefilled GitHub new-issue link (tool name + error + server version only, no args/credentials) targeting feedback.repositoryUrl (default: this server's repo). Surface or open it so bugs get reported.",
  },
  rules: [
    "NEVER invent credentials, tokens, or emails — every <ASK-USER> value must come from the user.",
    "The config holds secrets: keep it out of version control and never echo passwords/bearer back in chat or logs.",
    "Prod creds grant READ access paths only in practice here: this server's write tools are hard-wired to pre-prod / the dev clone and refuse prod at both the schema and domain layer.",
  ],
} as const;
