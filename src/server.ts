#!/usr/bin/env node

// MCP server: EHR Copilot operations (order cloning + Orchestrator execution analysis).
// Thin wiring layer — src/ is organized by domain (config/, copilot/, uipath/, mcp/, shared/);
// tool logic lives in per-domain modules, e.g.:
//   - config/config.ts     single-file config (copilot creds + uipath args) + validation
//   - copilot/analyze.ts   analyze_order_execution orchestration
//   - uipath/faults.ts     build_faulted_job_issue (faulted job -> GitHub issue payload)
// Tools:
//   - find_clone_candidates    list recent prod orders that are actually cloneable
//   - delete_preprod_order     delete pre-prod order(s)
//   - create_preprod_order     mint a fresh pre-prod order from an explicit spec (stops at forReview, never submits)
//   - submit_preprod_order     submit a pre-prod order sitting at forReview (explicit, separate write)
//   - build_queue_item         build a UiPath AddQueueItem request from an order (build-only)
//   - analyze_order_execution  trace an order to its UiPath Orchestrator job(s) and diagnose the run (read-only)
//   - build_faulted_job_issue  build a GitHub issue payload for a faulted UiPath job (read-only; posts nothing)
//   - list_queues              list queue definitions in a folder (read-only; discovers dev-clone queue ids)
//   - list_processes           list releases/processes in a folder (read-only; releaseKey + pin verification)
//   - list_triggers            list triggers in a folder (read-only; verify queue trigger -> release wiring)
//   - get_job                  fetch one job's state/output by GUID Key, or several via jobKeys (read-only)
//   - add_queue_item           POST one item to a dev-clone queue (pre_prod-only; test-safety guarded)
//   - delete_queue_item        delete a 'New' queue item from the dev clone (pre_prod-only; fetch-first)
//   - start_job                start job(s) for a dev-clone release (pre_prod-only)
//   - diff_settings            diff an account's settings between prod and pre-prod (read-only)
//   - list_setting_sections    list the settings sections/tags the settings tools can scope to (read-only, no network)
//   - get_settings             fetch one env's settings sections (read-only)
//   - plan_settings_sync       plan the additive prod -> pre-prod sync actions (read-only)
//   - apply_settings_sync      execute selected planned sync actions against pre-prod (additive only)
//   - get_order                fetch a single order's normalized detail by uid (read-only)
//   - search_orders            filter orders by location/insurance/referredTo/type/auth/date range (read-only)
//   - get_order_category_stats per-folder order counts for a given filter (read-only)
//   - get_login_token          log into the Copilot BE for a profile/env and return the session JWT (read-only)
//   - doctor                   probe the BE + UiPath connections and report what's reachable (read-only)
//
// See the copilot + copilot-order-mirror skills for the full flow and quirks.

import { realpathSync } from "node:fs";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { McpServer, ResourceTemplate } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import { configStatus, getFeedbackConfig, onConfigReload, resolveCreds } from "./config/config.js";
import { analyzeOrderExecution } from "./copilot/analyze.js";
import {
  assertPreProdClient,
  type BeOrder,
  fetchOrder,
  filterOrders,
  login,
  loginToken,
  makeClient,
  normalizeOrder,
  ORDER_MODE,
  submitOrder,
  toMDY,
  verify,
} from "./copilot/copilot-client.js";
import { runDoctor } from "./copilot/doctor.js";
import { type MintSpec, mintPreprodOrder } from "./copilot/mirror.js";
import { orderDocuments } from "./copilot/order-docs.js";
import { getOrderCategoryStats, searchOrders } from "./copilot/order-search.js";
import { normalizeOutput } from "./copilot/output-schema.js";
import {
  applySettingsSync,
  diffSettings,
  getSettings,
  listSettingSections,
  planSettingsSyncOp,
} from "./copilot/settings/index.js";
import { findStuckOrders } from "./copilot/sweep.js";
import { formatMcpIssue, toolError } from "./mcp/feedback.js";
import { mcpLog, registerLogging, reportProgress } from "./mcp/notify.js";
import { registerPrompts } from "./mcp/prompts.js";
import {
  CONFIG_GUIDE,
  ORDER_LIFECYCLE,
  PORTALS,
  type PortalEntry,
  QUEUE_ITEM_SCHEMA,
  RESULT_CONTRACT,
  SAFETY_RULES,
  UIPATH_FOLDERS,
} from "./mcp/reference.js";
import { msBetween, stringProp } from "./shared/util.js";
import { addQueueItem, deleteQueueItem, startJob } from "./uipath/actions.js";
import { buildFaultedJobIssue } from "./uipath/faults.js";
import { digestLogs, extractFault, truncate } from "./uipath/log-digest.js";
import { listQueue, pullQueueItem } from "./uipath/queue.js";
import { buildQueueItem } from "./uipath/queue-item.js";
import {
  fetchJobLogs,
  fetchJobLogsForKeys,
  fetchJobsForKeys,
  fetchJobVideoUrl,
  type JobLog,
  type JobLogFilter,
  type JobLogResult,
  jobDeepLink,
  listQueueDefinitions,
  listRecentJobs,
  listReleases,
  listTriggers,
  resolveFolder,
  scopeForEnv,
  type UiPathJob,
} from "./uipath/uipath.js";

// CRITICAL: MCP stdio uses STDOUT for the JSON-RPC protocol, so any stray stdout
// write corrupts it. We therefore never let console.log/info/warn reach stdout:
//   - progress (console.log/info) is silent by default and only surfaces on stderr
//     when verbose is on (LOG_LEVEL=debug|info|warn|trace, or COPILOT_MCP_DEBUG set);
//   - warnings (console.warn) and errors (native console.error) always go to stderr.
const toStderr = (...a: unknown[]) => process.stderr.write(`${a.map(String).join(" ")}\n`);
const VERBOSE =
  !!process.env["COPILOT_MCP_DEBUG"] ||
  ["debug", "info", "warn", "trace"].includes((process.env["LOG_LEVEL"] ?? "").toLowerCase());
const swallow = () => {};
console.log = VERBOSE ? toStderr : swallow;
console.info = console.log;
console.warn = toStderr;

const ok = (obj: unknown) => ({
  content: [{ type: "text" as const, text: JSON.stringify(obj, null, 2) }],
});
const toMessage = (e: unknown): string => (e instanceof Error ? e.message : String(e));

// A prod order clones to forReview only if it has facility + type + orderNames.
// Returns the candidate summary, or null if it would get stuck 'incomplete'.
const cloneCandidate = (o: BeOrder): Record<string, unknown> | null => {
  const cloneable =
    !!o.referredFacility?.referredFacilityUid &&
    !!o.orderType?.typeUid &&
    (o.orderNames ?? []).length > 0;
  if (!cloneable) return null;
  return {
    orderUid: o.orderUid,
    status: o.status,
    type: o.orderType?.name,
    facility: o.referredFacility?.name,
    insurance: o.insurance?.name,
    hasAppointmentDate: !!o.appointmentDate,
    icds: (o.ICDCodes ?? []).length,
    cpts: (o.CPTCodes ?? []).length,
  };
};

// Single source of truth for the server version: advertised to clients and embedded
// in the prefilled GitHub-issue URL on unexpected failures (see feedback.ts). Keep in
// sync with package.json on release.
const VERSION = "1.21.0";

// Initialize-time guidance for the connected agent. Instructions are static per
// session, so probe the config once at startup: an unconfigured server announces
// the self-setup path (config-guide resource -> write file -> doctor) instead of
// letting every tool fail cryptically. Config loading itself stays lazy and is
// retried on each call, so setup completes without a restart.
function buildInstructions(): string {
  const base =
    "EHR Copilot order operations + UiPath Orchestrator execution analysis. " +
    "Every Copilot tool requires an explicit env (prod | pre_prod) and a profile from the config — never assume either.\n\n" +
    "PROD IS READ-ONLY BY DESIGN. This server cannot mutate prod: every write path — order minting/deletes, " +
    "settings sync, queue-item posts/deletes, job starts — targets pre-prod / the UiPath dev-clone folder only, " +
    "enforced at the tool schema AND asserted again in the domain layer. Posted queue items additionally pass a " +
    "safety guard (IsApproved forced false, callback URLs pinned to pre-prod). Reading prod is normal and safe; " +
    "never work around a pre-prod-only refusal — it is the design, not a bug.\n\n" +
    "FEEDBACK: when a tool fails for a reason that looks like a bug in this server (not bad input, config, or an " +
    "upstream HTTP error), the error payload includes a `reportIssue` block with a prefilled GitHub new-issue URL " +
    "(carries only the tool name, error message, and server version — no args or credentials). Surface that link " +
    "to the user or open the issue directly so the bug gets reported; expected/user-actionable failures carry no such block. " +
    "The user can also ask to file a bug or general feedback about this server at any time — no failure required: " +
    "use build_mcp_issue (or the send-mcp-feedback prompt) to compose the issue, then post it via the host's GitHub tooling or hand over the prefilled URL.\n\n" +
    "Stable facts (portals, folders, schemas, safety rules, config guide) are resources under copilot://.";
  const cfg = configStatus();
  if (cfg.ok) return base;
  return (
    `${base}\n\nNO CONFIG IS LOADED — every tool will fail until one is provided (${cfg.error ?? "no config found"}).\n` +
    "To set this server up: read the copilot://reference/config-guide resource, ask the user for the credential/token values (never invent them), " +
    "write the JSON config file it describes, then call the doctor tool to verify connectivity. " +
    "The config is re-read on the next call — no restart needed."
  );
}

export const server = new McpServer(
  { name: "copilot", version: VERSION },
  // `logging` must be declared explicitly for notifications/message + logging/setLevel
  // to be advertised; tools/resources/prompts/completions are auto-negotiated by McpServer.
  { capabilities: { logging: {} }, instructions: buildInstructions() },
);

// Wire the logging/setLevel handler and register the workflow prompts. Tool/resource
// registration follows below in this module.
registerLogging(server);
registerPrompts(server);

// Config is live-reloaded (see config.ts) — tell the client via a logging
// notification whenever an edit on disk is picked up.
onConfigReload(({ source }) =>
  mcpLog(server, "info", `Configuration reloaded from ${source}`, { source }),
);

// ---- find_clone_candidates ----------------------------------------------
server.registerTool(
  "find_clone_candidates",
  {
    title: "Find cloneable prod orders",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Scan the most recent PROD orders and return only the ones that will actually clone to forReview — " +
      "i.e. they have a referredFacility, orderType, and orderNames. Orders missing those (common for " +
      "recent PCP-notes / order-only entries) get stuck 'incomplete' and are filtered out. Use this to " +
      "pick good uids before cloning via the clone-and-verify-order prompt.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      limit: z
        .number()
        .int()
        .min(1)
        .max(50)
        .optional()
        .default(10)
        .describe("Max candidates to return"),
      scanPages: z
        .number()
        .int()
        .min(1)
        .max(20)
        .optional()
        .default(8)
        .describe("Pages of 50 recent orders to scan"),
    },
  },
  async ({ profile, limit, scanPages }) => {
    try {
      const creds = resolveCreds(profile ?? null);
      const prod = makeClient(creds.prod.be, "prod");
      await login(prod, creds.prod.email, creds.prod.password);
      const candidates: Record<string, unknown>[] = [];
      let scanned = 0;
      for (let page = 0; page < scanPages && candidates.length < limit; page++) {
        const { rows } = await filterOrders(prod, {
          pageSize: 50,
          pageNumber: page,
          type: "Outbound Referral",
          orderMode: ORDER_MODE,
        });
        if (!rows.length) break;
        for (const o of rows) {
          scanned++;
          const c = cloneCandidate(o);
          if (c) candidates.push(c);
          if (candidates.length >= limit) break;
        }
      }
      return ok({ profile: profile ?? "(default)", scanned, found: candidates.length, candidates });
    } catch (e) {
      return toolError("find_clone_candidates", e, VERSION);
    }
  },
);

// ---- delete_preprod_order ------------------------------------------------
server.registerTool(
  "delete_preprod_order",
  {
    title: "Delete pre-prod order(s)",
    annotations: {
      readOnlyHint: false,
      destructiveHint: true, // deletes orders
      idempotentHint: true, // re-deleting an already-gone uid is a no-op
      openWorldHint: true,
    },
    description:
      "Delete one or more orders from the PRE-PROD tenant via DELETE /api/v1/orders/{uid}. " +
      "Use to clean up junk or incomplete clones. PRE-PROD ONLY — never targets prod.",
    inputSchema: {
      uids: z.array(z.string().min(8)).min(1).describe("Pre-prod orderUid(s) to delete"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
    },
  },
  async ({ uids, profile }) => {
    try {
      const creds = resolveCreds(profile ?? null);
      const pre = makeClient(creds.pre_prod.be, "pre_prod");
      assertPreProdClient(pre, "delete_preprod_order");
      await login(pre, creds.pre_prod.email, creds.pre_prod.password);
      const results = [];
      for (const uid of uids) {
        const r = await pre.req("DELETE", `/api/v1/orders/${uid}`);
        const msg = stringProp(r.data, "msg") ?? r.text.slice(0, 80);
        const okDelete = r.status < 400;
        // Audit trail: surface every destructive delete to the client log.
        mcpLog(server, "warning", `deleted pre-prod order ${uid}`, {
          status: r.status,
          ok: okDelete,
        });
        results.push({ uid, status: r.status, ok: okDelete, msg });
      }
      return ok({
        profile: profile ?? "(default)",
        deleted: results.filter((r) => r.ok).length,
        results,
      });
    } catch (e) {
      return toolError("delete_preprod_order", e, VERSION);
    }
  },
);

// ---- create_preprod_order --------------------------------------------------
server.registerTool(
  "create_preprod_order",
  {
    title: "Mint a pre-prod order from an explicit spec",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // creates a new order; existing state untouched
      idempotentHint: false, // each call mints a new order
      openWorldHint: true,
    },
    description:
      "Create a fresh order in the PRE-PROD tenant from explicit data (to clone an existing prod " +
      "order instead, use the clone-and-verify-order prompt, which reads the prod order via " +
      "get_order and calls this tool with the derived fields) and drive it to forReview. NEVER " +
      "submits — hand-minted test orders stop at forReview by design; use submit_preprod_order " +
      "separately, with explicit user authorization, to advance one. The sequence quirks (order " +
      "names reset speciality/provider; placeOfService applied last, post-forReview; /process " +
      "retry loop) are handled internally. Reference uids (typeUid, orderNamesUids, specialityUid, " +
      "facility/provider uids) are the PRE-PROD tenant's own — discover them via get_settings " +
      "(orders/specialties tags); never reuse prod uids. The result composes with " +
      "build_queue_item -> add_queue_item for dev-clone robot tests. If the order doesn't reach " +
      "forReview, the result's processMessage explains why (e.g. a missing reference) — check " +
      "get_settings/diff_settings for the implicated section. Returns " +
      "{profile, newUid, verify, processMessage}.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      patientName: z.string().min(1).describe("Patient name, 'Last, First' preferred"),
      patientBirthDate: z.string().min(6).describe("Patient DOB (MM/DD/YYYY or ISO)"),
      patientPhoneNumber: z
        .string()
        .optional()
        .describe("Patient phone (normalized; dummy used when omitted)"),
      insuranceName: z
        .string()
        .min(1)
        .describe("Insurance name — must be activated on the pre-prod tenant"),
      insuranceMemberId: z.string().optional().describe("Member id"),
      typeUid: z.string().min(8).describe("PRE-PROD order type uid (see get_settings)"),
      orderNamesUids: z
        .array(z.string().min(8))
        .min(1)
        .describe("PRE-PROD order name uid(s) — auto-seed CPTs; set BEFORE speciality/provider"),
      specialityUid: z.string().min(8).optional().describe("PRE-PROD speciality uid"),
      referredFacilityUid: z.string().min(8).optional().describe("PRE-PROD referred facility uid"),
      referredProviderUid: z
        .string()
        .min(8)
        .optional()
        .describe("PRE-PROD referred provider uid (referral orders)"),
      location: z.string().optional().describe("Clinic location string"),
      appointmentDate: z.string().optional().describe("Appointment date (MM/DD/YYYY or ISO)"),
      icdCodes: z
        .array(z.object({ code: z.string().min(1), description: z.string().optional() }))
        .optional()
        .describe("ICD codes to set after the note upload"),
      retro: z.boolean().optional().default(false).describe("Retro flag"),
      placeOfService: z
        .string()
        .optional()
        .describe("Place of service — applied LAST, post-forReview"),
    },
  },
  async (a) => {
    try {
      const creds = resolveCreds(a.profile);
      const pre = makeClient(creds.pre_prod.be, "pre_prod");
      await login(pre, creds.pre_prod.email, creds.pre_prod.password);
      const spec: MintSpec = {
        patientName: a.patientName,
        patientBirthDate: toMDY(a.patientBirthDate) ?? "",
        patientPhoneNumber: a.patientPhoneNumber ?? "",
        insuranceName: a.insuranceName,
        insuranceMemberId: a.insuranceMemberId ?? "",
        location: a.location ?? "",
        typeUid: a.typeUid,
        specialityUid: a.specialityUid ?? null,
        referredFacilityUid: a.referredFacilityUid ?? "",
        referredProviderUid: a.referredProviderUid ?? "",
        clinicProviderUid: "", // left to the FE default (no cross-account mapping here)
        orderNamesUids: a.orderNamesUids,
        cptCodes: [], // order names auto-seed CPTs
        uploadAuth: false,
        uploadFax: false,
        retro: a.retro,
        authorization: null,
        appointmentDate: a.appointmentDate ? (toMDY(a.appointmentDate) ?? "") : "",
        icdCodes: (a.icdCodes ?? []).map((i) => ({
          code: i.code,
          description: i.description ?? "",
        })),
        placeOfService: a.placeOfService ?? "",
      };
      const result = await mintPreprodOrder(pre, spec);
      // Audit trail: surface every pre-prod order mint to the client log.
      mcpLog(server, "warning", `minted pre-prod order ${result.newUid}`, {
        profile: a.profile,
        status: result.verify?.status,
      });
      return ok({ profile: a.profile, ...result });
    } catch (e) {
      return toolError("create_preprod_order", e, VERSION);
    }
  },
);

// ---- submit_preprod_order --------------------------------------------------
server.registerTool(
  "submit_preprod_order",
  {
    title: "Submit a pre-prod order sitting at forReview",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // advances the order's own state; nothing else is touched
      idempotentHint: false, // a second call re-submits/fails against an already-submitted order
      openWorldHint: true,
    },
    description:
      "Submit a PRE-PROD order that is sitting at forReview, advancing it to inProgress. " +
      "PRE-PROD ONLY. Requires EXPLICIT user authorization before calling — this is a real, " +
      "deliberate write, separate from create_preprod_order (which never submits). Returns " +
      "{profile, orderUid, submitted:true, verify}.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      orderUid: z.string().min(8).describe("PRE-PROD orderUid to submit (must be at forReview)"),
    },
  },
  async ({ profile, orderUid }) => {
    try {
      const creds = resolveCreds(profile);
      const pre = makeClient(creds.pre_prod.be, "pre_prod");
      await login(pre, creds.pre_prod.email, creds.pre_prod.password);
      const result = await submitOrder(pre, orderUid);
      mcpLog(server, "warning", `submitted pre-prod order ${orderUid}`, {
        profile,
        status: result?.status,
      });
      return ok({ profile, orderUid, submitted: true, verify: result });
    } catch (e) {
      return toolError("submit_preprod_order", e, VERSION);
    }
  },
);

// ---- build_queue_item ----------------------------------------------------
server.registerTool(
  "build_queue_item",
  {
    title: "Build UiPath AddQueueItem request from an order",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    }, // BUILD ONLY — fetches an order, never POSTs
    description:
      "Fetch an EHR Copilot order (prod or pre-prod) and build the UiPath AddQueueItem request " +
      "payload from its details. BUILD ONLY — never POSTs to UiPath; submit it yourself using " +
      "meta.postUrl and your own Orchestrator bearer token (see notes). IsApproved is always " +
      "false. Returns {payload, notes, meta}.",
    inputSchema: {
      orderUid: z.string().min(8).describe("Order UID to fetch (must exist in the chosen env)"),
      env: z
        .enum(["prod", "pre_prod"])
        .describe("Which env the order lives in; drives fetch + token + serverURL (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
    },
  },
  async ({ orderUid, env, profile }) => {
    try {
      const out = await buildQueueItem(orderUid, {
        profile: profile ?? null,
        env,
      });
      return ok(out);
    } catch (e) {
      return toolError("build_queue_item", e, VERSION);
    }
  },
);

// ---- analyze_order_execution ---------------------------------------------
server.registerTool(
  "analyze_order_execution",
  {
    title: "Analyze a Copilot order's UiPath Orchestrator execution",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Given a Copilot orderUid, find the matching UiPath Orchestrator job(s) and diagnose the run: " +
      "job State + verdict, computed durations and retry gaps, a structured fault (headline error, " +
      "stable signature, exception type), failure-language heuristics, a condensed log digest " +
      "(per-level counts, collapsed failure lines, stall gaps), a video link, and a deep link into " +
      "Orchestrator. The digest is deliberately concise — benign lines are omitted and messages " +
      "truncated. IF THE DIGEST IS NOT ENOUGH to explain the failure, call get_job_logs with the " +
      "job's `key` for the complete raw logs (filters: minLevel/contains/onlyFailures/tail). " +
      "READ-ONLY — never writes to UiPath or the BE. Correlates via " +
      "the job's OutputArguments (handles both the flat out_* schema and the transactionItem schema); " +
      "token/callbackContext are stripped from the returned output. Jobs live in a per-env UiPath folder " +
      "(env='prod' -> 'Authorization', env='pre_prod' -> 'Authorization Dev Clone'); pass env (required) " +
      "or an explicit folder. If Orchestrator rejects the OutputArguments filter, it falls back to scanning " +
      "the `top` most-recent jobs — pass `since` for prod. Optionally enrich with the order's current BE " +
      "status (same env). Returns {orderUid, env, folder, matched, jobCount, summary:{latestState,verdict,reasons}, " +
      "jobs:[{key, state, verdict, durationMs, gapSincePreviousJobMs, fault, logDigest, ...}]}.",
    inputSchema: {
      orderUid: z.string().min(8).describe("Copilot orderUid to trace"),
      env: z
        .enum(["prod", "pre_prod"])
        .describe(
          "Which UiPath folder/env the job ran in: prod='Authorization', pre_prod='Authorization Dev Clone' (required)",
        ),
      folder: z
        .string()
        .optional()
        .describe("Explicit UiPath folder path override (wins over env)"),
      since: z
        .string()
        .optional()
        .describe(
          "ISO date lower bound (only jobs created after this). Recommended for env=prod, and important if the OutputArguments filter is rejected and the tool falls back to scanning recent jobs",
        ),
      top: z
        .number()
        .int()
        .min(1)
        .max(500)
        .optional()
        .default(50)
        .describe("Max jobs to consider (also the fallback recent-scan window)"),
      includeLogs: z
        .boolean()
        .optional()
        .default(true)
        .describe(
          "Fetch robot logs (up to 500) per matched job and return them as a condensed " +
            "failure-focused digest (level counts, collapsed failure lines, stall gaps) — " +
            "use get_job_logs for the complete raw logs",
        ),
      includeVideo: z
        .boolean()
        .optional()
        .default(false)
        .describe("Fetch the job's video recording URL (extra round-trip; off by default)"),
      enrichOrderState: z
        .boolean()
        .optional()
        .default(false)
        .describe("Also fetch the order's current BE status (same env; needs Copilot creds)"),
      profile: z
        .string()
        .min(1)
        .describe(
          "Credential profile / account name from config (used for enrichOrderState) (required)",
        ),
    },
  },
  async (
    { orderUid, env, folder, since, top, includeLogs, includeVideo, enrichOrderState, profile },
    extra,
  ) => {
    try {
      mcpLog(server, "debug", `analyzing order ${orderUid}`, { env });
      reportProgress(extra, 0, 2, "tracing UiPath job");
      const out: Record<string, unknown> = {
        ...(await analyzeOrderExecution(orderUid, {
          env,
          folder,
          since,
          top,
          includeLogs,
          includeVideo,
        })),
      };
      reportProgress(extra, 1, 2, enrichOrderState ? "enriching order state" : "done");
      if (enrichOrderState) {
        try {
          const envCreds = resolveCreds(profile ?? null)[env];
          const client = makeClient(envCreds.be, env);
          await login(client, envCreds.email, envCreds.password);
          out["currentOrderState"] = await verify(client, orderUid);
        } catch (e) {
          out["currentOrderState"] = { error: toMessage(e) };
        }
      }
      reportProgress(extra, 2, 2, "done");
      return ok(out);
    } catch (e) {
      return toolError("analyze_order_execution", e, VERSION);
    }
  },
);

// ---- Resources (static reference data) -----------------------------------
// Read-only, bundled knowledge ported from ../RPAPlaywright/optum (see reference.ts).
// Live data (queues/jobs/orders) is exposed via tools below, not resources.
const jsonResource = (
  name: string,
  uri: string,
  title: string,
  description: string,
  data: unknown,
): void => {
  server.registerResource(
    name,
    uri,
    { title, description, mimeType: "application/json" },
    async (u) => ({
      contents: [
        { uri: u.href, mimeType: "application/json", text: JSON.stringify(data, null, 2) },
      ],
    }),
  );
};

jsonResource(
  "portals",
  "copilot://reference/portals",
  "Portal registry",
  "Auth-submit portals: queue name -> queue def ids, account, platform family, build artifact, volume.",
  PORTALS,
);
jsonResource(
  "uipath-folders",
  "copilot://reference/uipath-folders",
  "UiPath folders by env",
  "Per-env UiPath folder name, OrganizationUnitId, FolderKey, and whether it fires real BE calls.",
  UIPATH_FOLDERS,
);
jsonResource(
  "queue-item-schema",
  "copilot://schema/queue-item",
  "Queue item (SpecificContent) schema",
  "Base SpecificContent fields shared across portal auth-submit queue items.",
  QUEUE_ITEM_SCHEMA,
);
jsonResource(
  "result-contract",
  "copilot://schema/result-contract",
  "result.json output contract",
  "The result.json contract every portal automation writes and UiPath reads.",
  RESULT_CONTRACT,
);
jsonResource(
  "order-lifecycle",
  "copilot://reference/order-lifecycle",
  "Copilot order lifecycle",
  "Order statuses (drafted -> forReview -> inProgress) and tolerated error codes.",
  ORDER_LIFECYCLE,
);
jsonResource(
  "safety-rules",
  "copilot://reference/safety-rules",
  "Safety rules",
  "Invariants that keep test/dev activity from touching prod (IsApproved=false, dry-run gate, ...).",
  SAFETY_RULES,
);
jsonResource(
  "config-guide",
  "copilot://reference/config-guide",
  "Config setup guide",
  "How to configure this server from scratch: load order, JSON template with <ASK-USER> placeholders, field docs, and verification steps (doctor).",
  CONFIG_GUIDE,
);

// ---- Resource template: per-portal lookup --------------------------------
// Parameterized companion to the static `portals` resource: read one portal by
// queue name or alias. Demonstrates an MCP resource template + variable completion.
const findPortal = (name: string): PortalEntry | undefined => {
  const n = name.trim().toUpperCase();
  return PORTALS.find((p) => p.key === n || p.aliases.includes(n));
};
server.registerResource(
  "portal",
  new ResourceTemplate("copilot://reference/portal/{name}", {
    list: async () => ({
      resources: PORTALS.map((p) => ({
        name: p.key,
        uri: `copilot://reference/portal/${encodeURIComponent(p.key)}`,
        mimeType: "application/json",
        description: `${p.family} portal for account ${p.account || "(unknown)"}`,
      })),
    }),
    complete: {
      name: async (value) => {
        const v = value.trim().toUpperCase();
        return PORTALS.map((p) => p.key).filter((k) => k.startsWith(v));
      },
    },
  }),
  {
    title: "Portal by queue name",
    description:
      "Look up a single portal (queue ids, account, family, build artifact) by its queue name or alias.",
    mimeType: "application/json",
  },
  async (uri, { name }) => {
    const key = Array.isArray(name) ? name[0] : name;
    const portal = key ? findPortal(key) : undefined;
    const text = portal
      ? JSON.stringify(portal, null, 2)
      : JSON.stringify({ error: `unknown portal: ${key ?? ""}` }, null, 2);
    return { contents: [{ uri: uri.href, mimeType: "application/json", text }] };
  },
);

// ---- pull_queue_item -----------------------------------------------------
server.registerTool(
  "pull_queue_item",
  {
    title: "Pull a UiPath queue item as a test payload",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch a single UiPath queue item (by Orchestrator URL or transaction id) and return its " +
      "SpecificContent as a ready-to-run test payload, mapped to its portal. READ-ONLY. " +
      "IsApproved is ALWAYS forced false so a test run can never submit a real auth. env is " +
      "derived from the URL's fid when a url is given; pulling by txnId requires env. Returns " +
      "{item, env, queueName, portal, specificContent, suggestedFilename}.",
    inputSchema: {
      url: z
        .string()
        .url()
        .optional()
        .describe("Orchestrator queue-item URL (copy from the UI; fid + txn id are parsed out)"),
      txnId: z
        .number()
        .int()
        .positive()
        .optional()
        .describe("Transaction/item id (use instead of url)"),
      env: z
        .enum(["prod", "pre_prod"])
        .optional()
        .describe("Folder/env; derived from the URL fid when a url is given, otherwise required"),
      folderId: z
        .string()
        .optional()
        .describe("Numeric folder id (OrganizationUnitId) override when no url is given"),
    },
  },
  async ({ url, txnId, env, folderId }) => {
    try {
      if (!url && !env)
        throw new Error("env is required when pulling by txnId (no url to derive it from)");
      const args = url
        ? ({ source: "url", url } as const)
        : ({
            source: "txn",
            txnId: txnId ?? 0,
            env: env as "prod" | "pre_prod",
            folderId: folderId ?? "",
          } as const);
      return ok(await pullQueueItem(args));
    } catch (e) {
      return toolError("pull_queue_item", e, VERSION);
    }
  },
);

// ---- list_queue_items ----------------------------------------------------
server.registerTool(
  "list_queue_items",
  {
    title: "Browse a UiPath queue",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List items in a portal's UiPath queue for triage, newest first. Resolve by queueName " +
      "(prod queue ids only — pass queueDefId for dev) or an explicit queueDefId, optionally " +
      "filtered by status. READ-ONLY, PHI-light projection (id/status/reference/robotName + member " +
      "id/name). Returns {env, queueName, queueDefinitionId, count, items[]}.",
    inputSchema: {
      queueName: z
        .string()
        .optional()
        .describe("Portal/queue name, e.g. 'PMG', 'MOLINA' (resolves to its prod queue def id)"),
      queueDefId: z
        .number()
        .int()
        .positive()
        .optional()
        .describe("Explicit QueueDefinitionId (required for dev-clone queues)"),
      queueKind: z
        .enum(["submit", "sync"])
        .optional()
        .describe("Which queue for the named portal (default submit)"),
      env: z
        .enum(["prod", "pre_prod"])
        .describe("Folder/env (queueName ids are prod; use queueDefId for pre_prod) (required)"),
      status: z
        .enum(["New", "InProgress", "Failed", "Successful", "Retried", "Abandoned", "Deleted"])
        .optional()
        .describe("Optional UiPath queue-item status filter"),
      top: z.number().int().min(1).max(200).optional().default(50).describe("Max items to return"),
    },
  },
  async ({ queueName, queueDefId, queueKind, env, status, top }) => {
    try {
      return ok(
        await listQueue({
          queueName: queueName ?? "",
          queueDefId: queueDefId ?? 0,
          queueKind: queueKind ?? "submit",
          env,
          status: status ?? "",
          top,
        }),
      );
    } catch (e) {
      return toolError("list_queue_items", e, VERSION);
    }
  },
);

// ---- list_jobs -----------------------------------------------------------
server.registerTool(
  "list_jobs",
  {
    title: "List recent UiPath Orchestrator jobs",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the most recent Orchestrator jobs in a folder (newest first), without order " +
      "correlation. READ-ONLY. To diagnose a job from the list, call get_job with " +
      "includeLogDigest=true (condensed failure digest + fault signature) — get_job also takes a " +
      "jobKeys array to diagnose several jobs from this list in one call. Returns {env, folder, count, " +
      "jobs:[{id,key,state,processName,processVersion,robotName,creationTime,endTime,durationMs,deepLink}]}.",
    inputSchema: {
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      folder: z.string().optional().describe("Explicit folder path override (wins over env)"),
      since: z.string().optional().describe("ISO date lower bound on CreationTime"),
      top: z.number().int().min(1).max(500).optional().default(50).describe("Max jobs to return"),
      processName: z
        .string()
        .optional()
        .describe("Substring filter on the process (ReleaseName), e.g. 'OPTUM'"),
    },
  },
  async ({ env, folder, since, top, processName }) => {
    try {
      const resolved = resolveFolder(env, folder);
      const jobs = await listRecentJobs(since, top, resolved, processName);
      return ok({
        env,
        folder: resolved,
        count: jobs.length,
        jobs: jobs.map((j) => ({
          id: j.Id,
          key: j.Key,
          state: j.State,
          processName: j.ReleaseName,
          processVersion: j.ProcessVersion ?? "",
          robotName: j.RobotName ?? "",
          creationTime: j.CreationTime,
          endTime: j.EndTime,
          durationMs: msBetween(j.CreationTime, j.EndTime),
          deepLink: j.Key ? jobDeepLink(j.Key) : "",
        })),
      });
    } catch (e) {
      return toolError("list_jobs", e, VERSION);
    }
  },
);

// ---- get_job_logs --------------------------------------------------------
server.registerTool(
  "get_job_logs",
  {
    title: "Get a UiPath job's robot logs",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch robot execution logs (oldest first, capped 500 per job) for a job by its GUID Key, " +
      "optionally with the video-recording URL. Pass jobKeys (up to 25) instead of jobKey to fetch " +
      "logs for several jobs in ONE call — RobotLogs has no server-side way to combine JobKeys " +
      "(confirmed: OData 'in'/'or' across JobKey silently misbehave), so this still issues one " +
      "HTTP call per job under bounded concurrency, but collapses the MCP round-trips: use this " +
      "instead of looping get_job_logs once per job. All row filters are opt-in and apply to every " +
      "job in the batch — by default the full (capped) log set is returned, but each Message is " +
      "truncated to 400 chars (pass fullMessages=true for the untruncated text, e.g. to read a " +
      "complete stack trace). READ-ONLY. " +
      "Returns {jobKey,folder,logs[],returned,totalMatching?,truncated,videoUrl?} for a single " +
      "jobKey, or {env,folder,count,jobs:[{jobKey,logs[],returned,totalMatching?,truncated," +
      "videoUrl?}]} for jobKeys.",
    inputSchema: {
      jobKey: z
        .string()
        .min(1)
        .optional()
        .describe(
          "The job's GUID Key (from analyze_order_execution / list_jobs). Provide this OR " +
            "jobKeys, not both",
        ),
      jobKeys: z
        .array(z.string().min(1))
        .min(1)
        .max(25)
        .optional()
        .describe(
          "Batch: fetch logs for several jobs in one call. Provide this OR jobKey, not both",
        ),
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      folder: z.string().optional().describe("Explicit folder path override (wins over env)"),
      includeVideo: z
        .boolean()
        .optional()
        .default(false)
        .describe("Also fetch the job's video-recording URL (extra round-trip)"),
      minLevel: z
        .enum(["warn", "error"])
        .optional()
        .describe(
          "Only logs at or above this level (warn=Warn/Error/Fatal, error=Error/Fatal). " +
            "Filtered server-side. Default: all levels",
        ),
      contains: z
        .string()
        .min(1)
        .optional()
        .describe("Only logs whose Message contains this substring (server-side, case-sensitive)"),
      onlyFailures: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Semantic failure filter: keep only logs at error/fatal level OR whose message " +
            "mentions failure indicators (exception, failed, timeout, 'unable to', denied, …) " +
            "regardless of level. Catches failures logged at Info",
        ),
      tail: z
        .number()
        .int()
        .positive()
        .max(500)
        .optional()
        .describe("Only the last N matching logs (still returned oldest first)"),
      fullMessages: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Return full untruncated Message text (default: truncated to 400 chars, matching " +
            "analyze_order_execution's log digest). Use when you need a complete stack trace.",
        ),
    },
  },
  async ({
    jobKey,
    jobKeys,
    env,
    folder,
    includeVideo,
    minLevel,
    contains,
    onlyFailures,
    tail,
    fullMessages,
  }) => {
    try {
      if (!jobKey && !jobKeys) throw new Error("provide either jobKey or jobKeys");
      if (jobKey && jobKeys) throw new Error("provide only one of jobKey or jobKeys");
      const resolved = resolveFolder(env, folder);
      const filter: JobLogFilter = {
        onlyFailures,
        ...(minLevel !== undefined ? { minLevel } : {}),
        ...(contains !== undefined ? { contains } : {}),
        ...(tail !== undefined ? { tail } : {}),
      };
      const keys = jobKeys ?? [jobKey ?? ""];
      const byKey = await fetchJobLogsForKeys(keys, resolved, filter);
      const jobs = await Promise.all(
        keys.map(async (key) => {
          const { logs, totalMatching }: JobLogResult = byKey[key] ?? {
            logs: [],
            totalMatching: null,
          };
          const entry: Record<string, unknown> = {
            jobKey: key,
            logs: fullMessages ? logs : logs.map((l) => ({ ...l, Message: truncate(l.Message) })),
            returned: logs.length,
            // truncated = the 500-row fetch window didn't cover every matching row
            // (a requested `tail` shorter than the total is not truncation).
            truncated: totalMatching !== null && totalMatching > 500,
          };
          if (totalMatching !== null) entry["totalMatching"] = totalMatching;
          if (includeVideo) entry["videoUrl"] = await fetchJobVideoUrl(key, resolved);
          return entry;
        }),
      );
      if (jobKeys) return ok({ env, folder: resolved, count: jobs.length, jobs });
      return ok({ ...jobs[0], folder: resolved }); // single-jobKey call keeps the original flat shape
    } catch (e) {
      return toolError("get_job_logs", e, VERSION);
    }
  },
);

// ---- build_faulted_job_issue ---------------------------------------------
server.registerTool(
  "build_faulted_job_issue",
  {
    title: "Build a GitHub issue payload for a faulted UiPath job",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Read a faulted UiPath job (by its GUID Key) and its robot logs, and BUILD a ready-to-post " +
      "GitHub issue payload for the RPA repo. READ-ONLY: this does NOT post to GitHub — it returns " +
      "{repo,title,body,labels,faultSignature,searchQuery,recurrenceComment,found}. The caller (the " +
      "report-faulted-uipath-jobs prompt) hands this to the host's GitHub MCP server: search with " +
      "`searchQuery`, then add `recurrenceComment` to the matching open issue, or create_issue with " +
      "title/body/labels. `faultSignature` is the normalized error (stable across reruns) used to dedupe.",
    inputSchema: {
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      jobKey: z.string().min(1).describe("The faulted job's GUID Key (from list_jobs)"),
      folder: z.string().optional().describe("Explicit folder path override (wins over env)"),
      repo: z
        .string()
        .optional()
        .describe("Target repo owner/name (default Apex-Medical-AI-Inc/RPAPlaywright)"),
      labels: z.array(z.string()).optional().describe("Issue labels (default uipath-fault, bug)"),
    },
  },
  async ({ env, jobKey, folder, repo, labels }) => {
    try {
      return ok(await buildFaultedJobIssue(env, jobKey, { folder, repo, labels }));
    } catch (e) {
      return toolError("build_faulted_job_issue", e, VERSION);
    }
  },
);

// ---- list_queues -----------------------------------------------------------
server.registerTool(
  "list_queues",
  {
    title: "List UiPath queue definitions in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the queue definitions (the queues themselves) in an env's Orchestrator folder, " +
      "sorted by name. READ-ONLY. Use this to discover a queueDefId for list_queue_items / " +
      "the exact queue Name for add_queue_item — dev-clone (pre_prod) queue ids differ from " +
      "the prod ids in the static portal registry. Queue CREATION is deliberately not a tool " +
      "(rare one-time setup — do it in the Orchestrator UI). Returns " +
      "{env, count, queues:[{id,name,description,creationTime}]}.",
    inputSchema: {
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      nameContains: z.string().optional().describe("Substring filter on the queue name"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max queues"),
    },
  },
  async ({ env, nameContains, top }) => {
    try {
      const queues = await listQueueDefinitions(scopeForEnv(env), nameContains ?? "", top);
      return ok({ env, count: queues.length, queues });
    } catch (e) {
      return toolError("list_queues", e, VERSION);
    }
  },
);

// ---- list_processes --------------------------------------------------------
server.registerTool(
  "list_processes",
  {
    title: "List UiPath releases (processes) in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the releases ('processes' in the Orchestrator UI) in an env's folder, sorted by " +
      "name. READ-ONLY. `key` is the ReleaseKey that start_job takes; `processVersion` is the " +
      "pinned package version — a job resolves it at START, so verify the pin here right before " +
      "starting (repinning is manual in the Orchestrator UI; this MCP never repins). Returns " +
      "{env, count, processes:[{id,key,name,processKey,processVersion,isLatestVersion}]}.",
    inputSchema: {
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      nameContains: z.string().optional().describe("Substring filter on the release name"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max releases"),
    },
  },
  async ({ env, nameContains, top }) => {
    try {
      const processes = await listReleases(scopeForEnv(env), nameContains ?? "", top);
      return ok({ env, count: processes.length, processes });
    } catch (e) {
      return toolError("list_processes", e, VERSION);
    }
  },
);

// ---- list_triggers ---------------------------------------------------------
server.registerTool(
  "list_triggers",
  {
    title: "List UiPath triggers (queue + time) in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the triggers (ProcessSchedules) in an env's folder. READ-ONLY. Queue triggers carry " +
      "a queueDefinitionId + the releaseId/releaseName they start — use this to verify a queue's " +
      "trigger points at YOUR dev release before enqueueing (cross-check releaseId against " +
      "list_processes). Time triggers have queueDefinitionId 0. Returns {env, count, " +
      "triggers:[{id,name,enabled,releaseId,releaseKey,releaseName,queueDefinitionId," +
      "queueDefinitionName,itemsActivationThreshold,maxJobsForActivation}]}.",
    inputSchema: {
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      queueDefinitionId: z
        .number()
        .int()
        .positive()
        .optional()
        .describe("Only triggers watching this queue"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max triggers"),
    },
  },
  async ({ env, queueDefinitionId, top }) => {
    try {
      let triggers = await listTriggers(scopeForEnv(env), top);
      if (queueDefinitionId) {
        triggers = triggers.filter((t) => t.queueDefinitionId === queueDefinitionId);
      }
      return ok({ env, count: triggers.length, triggers });
    } catch (e) {
      return toolError("list_triggers", e, VERSION);
    }
  },
);

// Shape one job's detail (output parsed, optional fault + log digest) — shared by
// get_job's single-jobKey and batched jobKeys paths so they stay in sync.
function shapeJobDetail(
  job: UiPathJob,
  logs: JobLog[],
  includeLogDigest: boolean,
): Record<string, unknown> {
  let output: unknown = null;
  if (job.OutputArguments) {
    try {
      const parsed = JSON.parse(job.OutputArguments) as Record<string, unknown>;
      output = Object.fromEntries(normalizeOutput(parsed).fields);
    } catch {
      output = job.OutputArguments.slice(0, 500);
    }
  }
  return {
    id: job.Id,
    key: job.Key,
    state: job.State,
    processName: job.ReleaseName,
    processVersion: job.ProcessVersion ?? "",
    robotName: job.RobotName ?? "",
    creationTime: job.CreationTime,
    endTime: job.EndTime,
    durationMs: msBetween(job.CreationTime, job.EndTime),
    deepLink: job.Key ? jobDeepLink(job.Key) : "",
    output,
    ...(includeLogDigest
      ? {
          fault: job.State === "Successful" ? null : extractFault(job, logs),
          logDigest: digestLogs(logs),
        }
      : {}),
  };
}

// ---- get_job ---------------------------------------------------------------
server.registerTool(
  "get_job",
  {
    title: "Get a single UiPath job's state by Key",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch one Orchestrator job by its GUID Key — the light poll target after start_job, and " +
      "the job-first diagnostic when you have a Key but no orderUid " +
      "(list_jobs is an unkeyed scan; analyze_order_execution is order-correlated and heavy). " +
      "Pass jobKeys (up to 25) instead of jobKey to diagnose several jobs — e.g. every job list_jobs " +
      "just returned — in ONE call instead of looping get_job per key. READ-ONLY. `output` is the " +
      "job's parsed OutputArguments with token/callbackContext stripped. With includeLogDigest=true " +
      "it also returns a structured fault (headline error, stable signature, exception type) and a " +
      "condensed failure-focused log digest — if the digest is not enough, call get_job_logs " +
      "(also batchable via jobKeys) for the complete raw logs. " +
      "Returns {env, folder, found, job:{id,key,state,processName,processVersion,robotName," +
      "creationTime,endTime,durationMs,deepLink,output,fault?,logDigest?}} for a single jobKey, or " +
      "{env, folder, count, jobs:[{key,found,job?}]} for jobKeys.",
    inputSchema: {
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      jobKey: z
        .string()
        .min(8)
        .optional()
        .describe(
          "The job's GUID Key (from start_job / list_jobs). Provide this OR jobKeys, not both",
        ),
      jobKeys: z
        .array(z.string().min(8))
        .min(1)
        .max(25)
        .optional()
        .describe("Batch: diagnose several jobs in one call. Provide this OR jobKey, not both"),
      folder: z.string().optional().describe("Explicit folder path override (wins over env)"),
      includeLogDigest: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Also fetch each job's robot logs (extra HTTP call per job) and return a condensed " +
            "failure digest + structured fault (signature, exception type) — " +
            "use get_job_logs for the complete raw logs",
        ),
    },
  },
  async ({ env, jobKey, jobKeys, folder, includeLogDigest }) => {
    try {
      if (!jobKey && !jobKeys) throw new Error("provide either jobKey or jobKeys");
      if (jobKey && jobKeys) throw new Error("provide only one of jobKey or jobKeys");
      const resolved = resolveFolder(env, folder);
      const keys = jobKeys ?? [jobKey ?? ""];
      const byKey = await fetchJobsForKeys(keys, resolved);
      const results = await Promise.all(
        keys.map(async (key) => {
          const job = byKey[key] ?? null;
          if (!job) return { key, found: false };
          const logs = includeLogDigest ? await fetchJobLogs(key, resolved) : [];
          return { key, found: true, job: shapeJobDetail(job, logs, includeLogDigest) };
        }),
      );
      if (jobKeys) return ok({ env, folder: resolved, count: results.length, jobs: results });
      const only = results[0];
      if (!only?.found) return ok({ env, folder: resolved, found: false });
      return ok({ env, folder: resolved, found: true, job: only.job });
    } catch (e) {
      return toolError("get_job", e, VERSION);
    }
  },
);

// ---- add_queue_item --------------------------------------------------------
server.registerTool(
  "add_queue_item",
  {
    title: "Post a queue item to a dev-clone queue",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // additive — creates an item, never overwrites
      idempotentHint: false, // each call mints a new item
      openWorldHint: true,
    },
    description:
      "POST one item to a UiPath queue in the DEV-CLONE folder (pre_prod ONLY — the schema " +
      "rejects prod). Every payload passes the test-safety guard first: IsApproved is forced " +
      "false; a non-empty serverURL/queueUrl must match the config's pre-prod values; " +
      "<TO-FILL> placeholders are rejected. To post a build_queue_item result, pass " +
      "payload.itemData.Name as queueName, payload.itemData.Reference as reference and " +
      "payload.itemData.SpecificContent as specificContent. When enqueueing many items, pace " +
      "~1/s. Returns {env, queueName, reference, itemId, status, forced}.",
    inputSchema: {
      env: z
        .literal("pre_prod")
        .describe(
          "UiPath writes are pre_prod-only (folder 434039 'Authorization Dev Clone'); " +
            "'prod' is rejected by the schema (required)",
        ),
      queueName: z
        .string()
        .min(1)
        .describe("Exact queue Name in the dev-clone folder (see list_queues)"),
      reference: z
        .string()
        .min(1)
        .describe("Queue-item Reference, e.g. '<PORTAL>-DEVTEST-<n>-<orderUid8>'"),
      specificContent: z
        .record(z.unknown())
        .describe("The item's SpecificContent (fixture record or build_queue_item output)"),
      priority: z.enum(["Low", "Normal", "High"]).optional().default("Normal"),
    },
  },
  async ({ env, queueName, reference, specificContent, priority }) => {
    try {
      const result = await addQueueItem({ env, queueName, reference, priority, specificContent });
      // Audit trail: surface every Orchestrator write to the client log.
      mcpLog(server, "warning", `posted queue item to pre-prod queue '${queueName}'`, {
        reference,
        itemId: result.itemId,
        forced: result.forced,
      });
      return ok(result);
    } catch (e) {
      return toolError("add_queue_item", e, VERSION);
    }
  },
);

// ---- delete_queue_item -----------------------------------------------------
server.registerTool(
  "delete_queue_item",
  {
    title: "Delete a New queue item from the dev clone",
    annotations: {
      readOnlyHint: false,
      destructiveHint: true, // deletes an item
      idempotentHint: true, // re-deleting fails safe (status is no longer New)
      openWorldHint: true,
    },
    description:
      "Delete one queue item from the DEV-CLONE folder (pre_prod ONLY — the schema rejects " +
      "prod). Fetch-first: refuses unless the item's current Status is 'New', so InProgress/" +
      "Successful/Failed history is never destroyed. Use to clear stale test items before " +
      "re-enqueueing after a re-mint. Returns {env, itemId, deleted, previousStatus, reference}.",
    inputSchema: {
      env: z
        .literal("pre_prod")
        .describe(
          "UiPath writes are pre_prod-only (folder 434039 'Authorization Dev Clone'); " +
            "'prod' is rejected by the schema (required)",
        ),
      itemId: z.number().int().positive().describe("Queue item id (from list_queue_items)"),
    },
  },
  async ({ env, itemId }) => {
    try {
      const result = await deleteQueueItem(itemId, env);
      mcpLog(server, "warning", `deleted pre-prod queue item ${itemId}`, {
        reference: result.reference,
      });
      return ok(result);
    } catch (e) {
      return toolError("delete_queue_item", e, VERSION);
    }
  },
);

// ---- start_job -------------------------------------------------------------
server.registerTool(
  "start_job",
  {
    title: "Start a UiPath job on the dev clone",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // starts a run; existing state untouched
      idempotentHint: false, // each call starts new job(s)
      openWorldHint: true,
    },
    description:
      "Start job(s) for a release in the DEV-CLONE folder (pre_prod ONLY — the schema rejects " +
      "prod). Get the releaseKey from list_processes. PIN RACE: a job resolves its package " +
      "version at START, not creation — if others are publishing, verify the release's " +
      "processVersion via list_processes right before starting (repinning is manual in the " +
      "Orchestrator UI; this MCP never repins). Poll the result with get_job. Returns " +
      "{env, jobs:[{id,key,state,releaseName,deepLink}]}.",
    inputSchema: {
      env: z
        .literal("pre_prod")
        .describe(
          "UiPath writes are pre_prod-only (folder 434039 'Authorization Dev Clone'); " +
            "'prod' is rejected by the schema (required)",
        ),
      releaseKey: z.string().min(8).describe("Release GUID Key (from list_processes)"),
      inputArguments: z
        .record(z.unknown())
        .optional()
        .default({})
        .describe("Job input arguments (omit for none)"),
      jobsCount: z.number().int().min(1).max(5).optional().default(1).describe("Jobs to start"),
    },
  },
  async ({ env, releaseKey, inputArguments, jobsCount }) => {
    try {
      const result = await startJob({ env, releaseKey, inputArguments, jobsCount });
      mcpLog(server, "warning", `started ${result.jobs.length} dev-clone job(s)`, {
        releaseKey,
        keys: result.jobs.map((j) => j.key),
      });
      return ok(result);
    } catch (e) {
      return toolError("start_job", e, VERSION);
    }
  },
);

// ---- find_stuck_orders ---------------------------------------------------
server.registerTool(
  "find_stuck_orders",
  {
    title: "Find stuck Copilot orders",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Scan recent orders in an env and flag the ones sitting in a non-terminal ('stuck') status " +
      "(default inProgress/incomplete/pending). Optionally correlate each to its UiPath job(s) for a " +
      "coarse verdict (no-job / job-faulted / job-running / job-successful-order-stuck). READ-ONLY. " +
      "Returns {env, scanned, statuses, found, stuck:[{orderUid,status,ageHours,uipath?}]}.",
    inputSchema: {
      env: z.enum(["prod", "pre_prod"]).describe("Which env to scan (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      scanPages: z
        .number()
        .int()
        .min(1)
        .max(20)
        .optional()
        .default(8)
        .describe("Pages of 50 recent orders to scan"),
      statuses: z
        .array(z.string())
        .optional()
        .describe("Override the 'stuck' status set (default inProgress/incomplete/pending)"),
      olderThanHours: z
        .number()
        .min(0)
        .optional()
        .describe(
          "Only flag orders at least this many hours old (needs a parseable date on the row)",
        ),
      crossCheckUipath: z
        .boolean()
        .optional()
        .default(false)
        .describe("Correlate each stuck order to its UiPath job(s) for a verdict"),
      since: z.string().optional().describe("ISO lower bound for the UiPath job search"),
      top: z
        .number()
        .int()
        .min(1)
        .max(500)
        .optional()
        .default(50)
        .describe("Max jobs to consider per order in the cross-check"),
    },
  },
  async ({ env, profile, scanPages, statuses, olderThanHours, crossCheckUipath, since, top }) => {
    try {
      return ok(
        await findStuckOrders({
          env,
          profile: profile ?? null,
          scanPages,
          statuses,
          olderThanHours,
          crossCheckUipath,
          since,
          top,
        }),
      );
    } catch (e) {
      return toolError("find_stuck_orders", e, VERSION);
    }
  },
);

// ---- diff_settings -------------------------------------------------------
server.registerTool(
  "diff_settings",
  {
    title: "Diff Copilot settings prod vs pre-prod",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Compare an account's EHR Copilot settings between its PROD and PRE-PROD tenants. " +
      "Logs into both envs, fetches every settings section (file manager, outbound orders + types, " +
      "locations/groups/regions, payers, document routing/reviewing rules, eligibility, rendering " +
      "providers, and — crawled from each order type's specialities — specialties + referred " +
      "providers/facilities), and returns a normalized diff. READ-ONLY — never writes to either env. " +
      "Env-specific noise (UIDs, timestamps, dummy emails, CDN hosts) is stripped, and list " +
      "sections are matched by a semantic key (name) rather than UID, so only real drift shows. " +
      "Unchanged sections are omitted unless includeUnchanged=true. Scope with tags " +
      "(e.g. ['orders']) and/or sections (exact keys) — combined as AND; use list_setting_sections " +
      "to discover valid tags/keys. For a single env's raw values use get_settings; to " +
      "reconcile drift use plan_settings_sync then apply_settings_sync. Returns " +
      "{account, prodBase, preProdBase, sectionsCompared, sectionsWithDiffs, sections:[...]}.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      tags: z
        .array(z.string())
        .optional()
        .describe("Tags to diff (e.g. ['orders','providers']); omit for all"),
      sections: z
        .array(z.string())
        .optional()
        .describe("Subset of section keys to diff (e.g. ['orders-outbound']); omit for all"),
      emr: z
        .string()
        .optional()
        .describe("EMR type (e.g. 'NEXTGEN') to also diff emrDetailsSettings; account-specific"),
      includeUnchanged: z
        .boolean()
        .optional()
        .default(false)
        .describe("Include sections that are identical across envs (default: only differences)"),
    },
  },
  async ({ profile, tags, sections, emr, includeUnchanged }) => {
    try {
      return ok(
        await diffSettings({
          profile: profile ?? null,
          ...(tags ? { tags } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
          includeUnchanged,
        }),
      );
    } catch (e) {
      return toolError("diff_settings", e, VERSION);
    }
  },
);

// ---- list_setting_sections ----------------------------------------------
server.registerTool(
  "list_setting_sections",
  {
    title: "List the settings sections the settings tools can scope to",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false, // pure catalog read — no external service
    },
    description:
      "List the settings sections diff_settings / get_settings / plan_settings_sync can scope to: " +
      "each section's key, label, tags, kind (object/list), whether it is 'derived' " +
      "(crawled from order types — heavier), and (for list sections) its matchKey — the field items " +
      "are matched/scoped by. Use this to drive FINE-GRAINED scoping: pass exact section keys via " +
      "the `sections` param instead of the broader `tags`. Narrow this listing with `tag` and/or " +
      "`sections`. READ-ONLY, no network (reads the static catalog). Returns {tags, sections:[...]}.",
    inputSchema: {
      tag: z.string().optional().describe("Only list sections carrying this tag (e.g. 'orders')"),
      sections: z
        .array(z.string())
        .optional()
        .describe("Only list these exact section keys (e.g. ['orders-outbound','locations'])"),
      emr: z
        .string()
        .optional()
        .describe("Include the opt-in emr-details section for this EMR type"),
    },
  },
  ({ tag, sections, emr }) => {
    try {
      return ok(
        listSettingSections({
          ...(tag ? { tag } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
        }),
      );
    } catch (e) {
      return toolError("list_setting_sections", e, VERSION);
    }
  },
);

// ---- get_settings ---------------------------------------------------------
server.registerTool(
  "get_settings",
  {
    title: "Get one env's Copilot settings",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch an account's EHR Copilot settings from ONE env (prod or pre_prod) — the single-env " +
      "counterpart to diff_settings. Logs into that env only and returns each selected section's " +
      "payload, stripped of env-specific noise (UIDs, timestamps, per-section ignore fields) by " +
      "default (list sections also report a row count). READ-ONLY — never writes. Scope with " +
      "tags and/or sections (exact keys) — combined as AND; use list_setting_sections " +
      "to discover them; crawled sections (specialties / referred-* / orders) are heavier. Pass " +
      "normalized=false for the raw payload with real UIDs/timestamps visible (e.g. to copy a UID " +
      "for another call). Returns " +
      "{account, env, base, sectionsFetched, sections:[{key,label,tags,kind,count?,data|error}]}.",
    inputSchema: {
      env: z.enum(["prod", "pre_prod"]).describe("Which env to read (required — never assumed)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      tags: z
        .array(z.string())
        .optional()
        .describe("Tags to fetch (e.g. ['orders']); omit for all"),
      sections: z
        .array(z.string())
        .optional()
        .describe("Exact section keys to fetch; omit for all"),
      emr: z
        .string()
        .optional()
        .describe("EMR type (e.g. 'NEXTGEN') to include emrDetailsSettings"),
      normalized: z
        .boolean()
        .optional()
        .default(true)
        .describe(
          "Strip UID/timestamp noise like diff_settings does (default true). Pass false for " +
            "raw payloads with real UIDs/timestamps visible (e.g. to copy a UID for another call).",
        ),
    },
  },
  async ({ env, profile, tags, sections, emr, normalized }) => {
    try {
      return ok(
        await getSettings({
          env,
          profile: profile ?? null,
          ...(tags ? { tags } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
          normalized,
        }),
      );
    } catch (e) {
      return toolError("get_settings", e, VERSION);
    }
  },
);

// ---- plan_settings_sync ----------------------------------------------------
server.registerTool(
  "plan_settings_sync",
  {
    title: "Plan an additive prod -> pre-prod settings sync",
    annotations: {
      readOnlyHint: true, // planning only — apply_settings_sync executes
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Compute — WITHOUT WRITING — the additive actions that would copy settings existing in PROD " +
      "but missing in PRE-PROD into pre-prod. READ-ONLY planning step; apply_settings_sync " +
      "executes a reviewed selection. Covers two domains: (1) the outbound order-type SPECIALTIES " +
      "domain (sections specialties / referred-providers / referred-facilities) — creates prod-only " +
      "specialties and merges prod-only facilities/providers into specialties that already exist in " +
      "pre-prod; and (2) the ORDERS domain (section orders) — creates prod-only orders under " +
      "matching order types (create-only). All references (payers, facilities, auth/referral " +
      "sub-categories) are remapped prod->pre by NAME; unmatched ones are dropped with a warning on " +
      "the action. Each action carries a stable id ('section:op:typeName:itemName') to pass to " +
      "apply_settings_sync after user review. Request bodies are summarized, not included " +
      "(includeBodies=true to inspect them — scope with sections first). Sections with no verified " +
      "write endpoint are reported under skippedSections. Also runs a READ-ONLY payer-link audit " +
      "(specialties domain only): a facility/provider that already exists (matched by name) in " +
      "both envs is never touched by the additive sync above, so payer-link drift on it — payers " +
      "linked in pre-prod but not prod, prod but not pre-prod, or a payerUid that resolves to no " +
      "payer at all — was previously invisible. Matched by payer NAME, never by payerUid (payer " +
      "uids are per-env and never expected to match). Findings are reported only, never turned " +
      "into a write action. Returns {account, prodBase, preProdBase, actionCount, " +
      "actions:[{id,op,itemKind,typeName,itemName,method,path,summary,warnings?}], skipped, " +
      "skippedSections, payerLinkFindings:[{typeName,specialityName,itemKind,itemName," +
      "extraInPreProd,missingInPreProd,orphanedProdPayerUids,orphanedPreProdPayerUids}]}.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      tags: z.array(z.string()).optional().describe("Tags to plan for (e.g. ['orders'])"),
      sections: z.array(z.string()).optional().describe("Exact section keys to plan for"),
      emr: z.string().optional().describe("EMR type to include emrDetailsSettings"),
      includeBodies: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Include full request bodies per action (large; for spot-checking a scoped plan)",
        ),
    },
  },
  async ({ profile, tags, sections, emr, includeBodies }) => {
    try {
      return ok(
        await planSettingsSyncOp({
          profile: profile ?? null,
          ...(tags ? { tags } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
          includeBodies,
        }),
      );
    } catch (e) {
      return toolError("plan_settings_sync", e, VERSION);
    }
  },
);

// ---- apply_settings_sync ---------------------------------------------------
server.registerTool(
  "apply_settings_sync",
  {
    title: "Apply planned additive settings sync actions to pre-prod",
    annotations: {
      readOnlyHint: false, // writes to pre-prod
      destructiveHint: false, // ADDITIVE ONLY — never overwrites or deletes existing pre-prod settings
      idempotentHint: true, // re-applying once names match is a no-op (nothing left to plan)
      openWorldHint: true,
    },
    description:
      "Execute additive prod -> pre-prod settings sync actions. PRE-PROD ONLY; additive only — " +
      "never overwrites or deletes existing pre-prod settings. SAFETY: it never accepts request " +
      "bodies — it RE-PLANS server-side (same computation and scoping args as plan_settings_sync; " +
      "use the SAME tags/sections scope) and executes only the planned actions you select: pass " +
      "actionIds (from plan_settings_sync, after reviewing with the user) OR all=true to apply " +
      "every planned action — exactly one of the two is required; calls with neither (or both) are " +
      "rejected. If pre-prod changed since planning, a stale id matches nothing and is reported " +
      "under unmatchedIds instead of executing. References are remapped prod->pre by name; " +
      "unmatched refs are dropped with a warning. Every write is logged to the client (audit " +
      "trail). Returns {account, prodBase, preProdBase, plannedCount, executed:[{id,op,itemKind," +
      "typeName,itemName,method,path,status,ok,warnings?}], notSelected, unmatchedIds, skipped, " +
      "skippedSections}.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      tags: z
        .array(z.string())
        .optional()
        .describe("Tags to re-plan (must match the plan call's scope)"),
      sections: z
        .array(z.string())
        .optional()
        .describe("Exact section keys to re-plan (must match the plan call's scope)"),
      emr: z.string().optional().describe("EMR type to include emrDetailsSettings"),
      actionIds: z
        .array(z.string().min(1))
        .optional()
        .describe("Ids from plan_settings_sync to execute (reviewed with the user)"),
      all: z
        .boolean()
        .optional()
        .describe("Explicitly apply EVERY planned action (mutually exclusive with actionIds)"),
    },
  },
  async ({ profile, tags, sections, emr, actionIds, all }) => {
    try {
      return ok(
        await applySettingsSync({
          profile: profile ?? null,
          ...(tags ? { tags } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
          ...(actionIds ? { actionIds } : {}),
          ...(all !== undefined ? { all } : {}),
          // Audit trail: surface every live write to the client log.
          onWrite: (message, data) => mcpLog(server, "warning", message, data),
        }),
      );
    } catch (e) {
      return toolError("apply_settings_sync", e, VERSION);
    }
  },
);

// ---- get_order -----------------------------------------------------------
server.registerTool(
  "get_order",
  {
    title: "Get a Copilot order's normalized detail",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch a single EHR Copilot order by uid in a chosen env and return its normalized " +
      "detail: status/submissionStatus, patient, insurance + memberId, order type, facility, " +
      "place of service, requiredAuthorization, appointment date, ICD/CPT codes, and whether a " +
      "progress note is present. Also returns `documents`: clickable CDN links that actually exist " +
      "for the order — the auth-screenshot PDF (when hasAuthScreenshot) and the medical-authorization " +
      "summary PDF(s) (from /medicalAuthorizations). The CDN is CloudFront signed-cookie protected, so " +
      "these links open only in an authenticated Copilot browser session; they are not fetched here. " +
      "READ-ONLY. Returns {orderUid, env, ...detail, documents?} or an error if the order is not found.",
    inputSchema: {
      orderUid: z.string().min(8).describe("Order UID to fetch"),
      env: z.enum(["prod", "pre_prod"]).describe("Which env the order lives in (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
    },
  },
  async ({ orderUid, env, profile }) => {
    try {
      const creds = resolveCreds(profile ?? null)[env];
      const client = makeClient(creds.be, env);
      await login(client, creds.email, creds.password);
      const order = await fetchOrder(client, orderUid);
      const detail = normalizeOrder(order);
      const documents = await orderDocuments(client, order, orderUid, profile);
      return ok({ orderUid, env, ...detail, ...(documents.length > 0 ? { documents } : {}) });
    } catch (e) {
      return toolError("get_order", e, VERSION);
    }
  },
);

// ---- search_orders --------------------------------------------------------
const orderFilterDimensionsSchema = {
  type: z
    .string()
    .optional()
    .describe("Order category, e.g. 'Outbound Referral' (default) or 'PCP Notes'"),
  locations: z.array(z.string()).optional().describe("Clinic location names to filter to"),
  insurances: z.array(z.string()).optional().describe("Insurance/payer names to filter to"),
  referredTo: z
    .array(z.string())
    .optional()
    .describe("referredFacilityUid/referredProviderUid values to filter to"),
  orderType: z
    .array(z.string())
    .optional()
    .describe(
      "Order type names, e.g. 'Consultation Referral', 'Follow-up Visit', 'Imaging', 'Injections', " +
        "'Medical Supplies', 'Other', 'PCP Notes'",
    ),
  authRequired: z
    .array(z.enum(["Yes", "No", "Skip"]))
    .optional()
    .describe("Authorization Required filter (UI strings)"),
  authStatus: z
    .array(
      z.enum([
        "denied",
        "approved",
        "expired",
        "cancelled",
        "closed",
        "denied by Service Provider",
      ]),
    )
    .optional()
    .describe("Authorization Status filter"),
  uploadStatusAuth: z
    .array(z.enum(["not_started", "completed", "failed", "in_progress"]))
    .optional()
    .describe("Upload Auth Status filter"),
  uploadStatusFax: z
    .array(z.enum(["not_started", "completed", "failed", "in_progress"]))
    .optional()
    .describe("Upload Fax Status filter"),
  hasAuthScreenshot: z
    .boolean()
    .optional()
    .describe("true = Pending Final Approval, false = Pending Review & Submission"),
  sendFax: z.boolean().optional().describe("Send Fax filter"),
  missingNotes: z.boolean().optional().describe("Missing Provider Notes filter"),
  mrn: z.string().optional().describe("Free-text MRN search"),
  search: z.string().optional().describe("Free-text patient/order search"),
  fromDate: z.string().optional().describe("Creation Date range start, MM-DD-YYYY"),
  toDate: z.string().optional().describe("Creation Date range end, MM-DD-YYYY"),
  orderDateFrom: z.string().optional().describe("Order Date range start, MM-DD-YYYY"),
  orderDateTo: z.string().optional().describe("Order Date range end, MM-DD-YYYY"),
  appointmentDateFrom: z.string().optional().describe("Appointment Date range start, MM-DD-YYYY"),
  appointmentDateTo: z.string().optional().describe("Appointment Date range end, MM-DD-YYYY"),
  submissionDateFrom: z.string().optional().describe("Submission Date range start, MM-DD-YYYY"),
  submissionDateTo: z.string().optional().describe("Submission Date range end, MM-DD-YYYY"),
  notificationDateFrom: z.string().optional().describe("Notification Date range start, MM-DD-YYYY"),
  notificationDateTo: z.string().optional().describe("Notification Date range end, MM-DD-YYYY"),
};

server.registerTool(
  "search_orders",
  {
    title: "Search Copilot orders by dimension",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Filter an account's Copilot orders by any combination of location, insurance, referred-to, " +
      "order type, auth/upload status, MRN, free-text search, or any of 5 date ranges, via " +
      "POST /orders/filter. READ-ONLY. Returns slim, non-PHI rows (orderUid, status, dates, insurance, " +
      "speciality, referredTo{name,npi,external}, auth/upload status — no patient data). Defaults to " +
      "type='Outbound Referral', pageSize=100, pageNumber=1 (1-based). Use get_order for a single " +
      "order's full detail including patient info.",
    inputSchema: {
      env: z.enum(["prod", "pre_prod"]).describe("Which env to search (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      pageSize: z.number().int().min(1).max(100).optional().describe("Rows per page (default 100)"),
      pageNumber: z.number().int().min(1).optional().describe("1-based page number (default 1)"),
      sort: z
        .string()
        .regex(
          /^(creationDate|orderDate|submissionDate|appointmentDate|notificationDate):(asc|desc)$/,
        )
        .optional()
        .describe("e.g. 'creationDate:desc'"),
      ...orderFilterDimensionsSchema,
    },
  },
  async (args) => {
    try {
      return ok(await searchOrders({ ...args, profile: args.profile ?? null }));
    } catch (e) {
      return toolError("search_orders", e, VERSION);
    }
  },
);

// ---- get_order_category_stats ----------------------------------------------
server.registerTool(
  "get_order_category_stats",
  {
    title: "Get Copilot order counts per category folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Cheap per-folder order counts (For Review, PCP Notes, Processing, Archived, with their " +
      "sub-buckets) for a given filter, via POST /orders/category/stats — same filter dimensions as " +
      "search_orders, no pagination. READ-ONLY. Response shape is passed through as returned by the BE.",
    inputSchema: {
      env: z.enum(["prod", "pre_prod"]).describe("Which env to query (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      ...orderFilterDimensionsSchema,
    },
  },
  async (args) => {
    try {
      return ok(await getOrderCategoryStats({ ...args, profile: args.profile ?? null }));
    } catch (e) {
      return toolError("get_order_category_stats", e, VERSION);
    }
  },
);

// ---- get_login_token -----------------------------------------------------
server.registerTool(
  "get_login_token",
  {
    title: "Get a Copilot BE login token",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Log into the EHR Copilot BE for a profile + env and return the session JWT " +
      "(the same token used as SpecificContent.token for UiPath callbacks). READ-ONLY — " +
      "authenticates only, never reads or writes order data. Returns {env, profile, token}.",
    inputSchema: {
      env: z.enum(["prod", "pre_prod"]).describe("Which env to log into (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
    },
  },
  async ({ env, profile }) => {
    try {
      const creds = resolveCreds(profile ?? null)[env];
      const client = makeClient(creds.be, env);
      const token = await loginToken(client, creds.email, creds.password);
      return ok({ env, profile, token });
    } catch (e) {
      return toolError("get_login_token", e, VERSION);
    }
  },
);

// ---- build_mcp_issue -------------------------------------------------------
server.registerTool(
  "build_mcp_issue",
  {
    title: "Build a GitHub issue for feedback on this MCP server",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false, // pure string building — touches no external service
    },
    description:
      "Compose a GitHub issue about THIS MCP server from a user's report — a bug OR general " +
      "feedback (ideas, friction, docs gaps); no tool failure required. BUILD ONLY — posts " +
      "nothing and holds no GitHub credentials: create the issue via the host's GitHub tooling " +
      "(GitHub MCP server / gh) from the returned title/body/labels, or give the user `url` (a " +
      "prefilled new-issue link; filing needs a GitHub account with access to the target repo). " +
      "Targets feedback.repositoryUrl (default: the copilot-mcp repo); works even with no config " +
      "loaded. Never put credentials or PHI in the details. Returns {repo, title, body, labels, url}.",
    inputSchema: {
      kind: z
        .enum(["bug", "feedback"])
        .describe("bug = something is wrong; feedback = idea / improvement / general comment"),
      title: z.string().min(8).describe("Short issue summary"),
      details: z
        .string()
        .min(20)
        .describe(
          "The report itself: what happened or what is wanted, expected vs actual, steps if relevant. No credentials or PHI.",
        ),
      tool: z
        .string()
        .optional()
        .describe("Related copilot-mcp tool name, if the report concerns one"),
    },
  },
  async ({ kind, title, details, tool }) => {
    try {
      let repositoryUrl: string | undefined;
      try {
        repositoryUrl = getFeedbackConfig().repositoryUrl;
      } catch {
        // no config loaded — feedback must still work; fall back to the default repo
      }
      return ok(
        formatMcpIssue({
          kind,
          title,
          details,
          version: VERSION,
          ...(tool ? { tool } : {}),
          ...(repositoryUrl ? { repositoryUrl } : {}),
        }),
      );
    } catch (e) {
      return toolError("build_mcp_issue", e, VERSION);
    }
  },
);

// ---- doctor --------------------------------------------------------------
server.registerTool(
  "doctor",
  {
    title: "Check the server's external API connections",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Probe the MCP server's connections to its external APIs and report what is reachable: " +
      "logs into the Copilot BE for PROD and PRE-PROD, and makes one cheap authenticated UiPath " +
      "Orchestrator call per env/folder. READ-ONLY. Use it to debug setup (creds, UiPath token, " +
      "folder access). Returns {account, ok, checks:[{name, target, ok, detail}]}.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config to check (required)"),
    },
  },
  async ({ profile }) => {
    try {
      return ok(await runDoctor({ profile: profile ?? null }));
    } catch (e) {
      return toolError("doctor", e, VERSION);
    }
  },
);

// Only open the stdio transport when this module is the process entrypoint —
// importing it (e.g. from the test suite) must not consume stdin. We compare the
// invoked script to this module, resolving symlinks so the `copilot-mcp` bin
// (a symlink into node_modules/.bin) still matches dist/server.js.
function isEntrypoint(): boolean {
  const argv1 = process.argv[1];
  if (!argv1) return false;
  try {
    return realpathSync(argv1) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
}

if (isEntrypoint()) {
  await server.connect(new StdioServerTransport());
  console.log("copilot MCP server ready (stdio)");
}
