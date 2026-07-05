#!/usr/bin/env node

// MCP server: EHR Copilot operations (order cloning + Orchestrator execution analysis).
// Thin wiring layer — src/ is organized by domain (config/, copilot/, uipath/, mcp/, shared/);
// tool logic lives in per-domain modules, e.g.:
//   - config/config.ts     single-file config (copilot creds + uipath args) + validation
//   - copilot/analyze.ts   analyze_order_execution orchestration
//   - uipath/faults.ts     build_faulted_job_issue (faulted job -> GitHub issue payload)
// Tools:
//   - clone_order              mirror prod order(s) into pre-prod (clone-only by default; submit opt-in)
//   - find_clone_candidates    list recent prod orders that are actually cloneable
//   - delete_preprod_order     delete pre-prod order(s)
//   - build_queue_item         build a UiPath AddQueueItem request from an order (build-only)
//   - analyze_order_execution  trace an order to its UiPath Orchestrator job(s) and diagnose the run (read-only)
//   - build_faulted_job_issue  build a GitHub issue payload for a faulted UiPath job (read-only; posts nothing)
//   - diff_settings            diff an account's settings between prod and pre-prod (read-only)
//   - list_setting_sections    list the settings sections/groups diff_settings can compare (read-only, no network)
//   - sync_settings            additively add prod-only settings into pre-prod (specialties domain; dry-run default)
//   - get_order                fetch a single order's normalized detail by uid (read-only)
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
import { resolveCreds } from "./config/config.js";
import { analyzeOrderExecution } from "./copilot/analyze.js";
import {
  fetchOrder,
  login,
  loginToken,
  makeClient,
  normalizeOrder,
  ORDER_MODE,
  verify,
} from "./copilot/copilot-client.js";
import { runDoctor } from "./copilot/doctor.js";
import { type MirrorResult, mirrorOne } from "./copilot/mirror.js";
import { orderDocuments } from "./copilot/order-docs.js";
import { diffSettings, listSettingSections, syncSettings } from "./copilot/settings.js";
import { findStuckOrders } from "./copilot/sweep.js";
import { toolError } from "./mcp/feedback.js";
import { mcpLog, registerLogging, reportProgress } from "./mcp/notify.js";
import { registerPrompts } from "./mcp/prompts.js";
import {
  ORDER_LIFECYCLE,
  PORTALS,
  type PortalEntry,
  QUEUE_ITEM_SCHEMA,
  RESULT_CONTRACT,
  SAFETY_RULES,
  UIPATH_FOLDERS,
} from "./mcp/reference.js";
import { envelopeRows, stringProp } from "./shared/util.js";
import { buildFaultedJobIssue } from "./uipath/faults.js";
import { listQueue, pullQueueItem } from "./uipath/queue.js";
import { buildQueueItem } from "./uipath/queue-item.js";
import {
  fetchJobLogs,
  fetchJobVideoUrl,
  jobDeepLink,
  listRecentJobs,
  resolveFolder,
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

// Minimal shape of an order row from /api/v1/orders/filter (only fields we read).
interface OrderRow {
  orderUid?: string;
  status?: string;
  referredFacility?: { referredFacilityUid?: string; name?: string };
  orderType?: { typeUid?: string; name?: string };
  orderNames?: unknown[];
  insurance?: { name?: string };
  appointmentDate?: unknown;
  ICDCodes?: unknown[];
  CPTCodes?: unknown[];
}
const ordersOf = (data: unknown): OrderRow[] => envelopeRows(data) as OrderRow[];

// A prod order clones to forReview only if it has facility + type + orderNames.
// Returns the candidate summary, or null if it would get stuck 'incomplete'.
const cloneCandidate = (o: OrderRow): Record<string, unknown> | null => {
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
const VERSION = "1.7.4";

export const server = new McpServer(
  { name: "copilot", version: VERSION },
  // `logging` must be declared explicitly for notifications/message + logging/setLevel
  // to be advertised; tools/resources/prompts/completions are auto-negotiated by McpServer.
  { capabilities: { logging: {} } },
);

// Wire the logging/setLevel handler and register the workflow prompts. Tool/resource
// registration follows below in this module.
registerLogging(server);
registerPrompts(server);

// ---- clone_order ---------------------------------------------------------
server.registerTool(
  "clone_order",
  {
    title: "Clone Copilot order(s) prod -> pre-prod",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // creates fresh pre-prod orders; never deletes/overwrites
      idempotentHint: false, // each call mints new order(s)
      openWorldHint: true,
    },
    description:
      "Mirror one or more PROD EHR Copilot orders into the PRE-PROD tenant as fresh orders. " +
      "Clone-only by default (stops at forReview / ready-to-submit). Set submit=true ONLY when the " +
      "user explicitly authorizes submitting in pre-prod. Patient name/DOB do NOT carry across envs " +
      "(pre-prod substitutes its own EMR patient); insurance/memberId/ICD/CPT/facility/POS DO carry. " +
      "Source orders must have facility+type+orderNames (use find_clone_candidates first) or they get " +
      "stuck in 'incomplete'. Returns per-order {prodUid, newUid, verify:{status,...}}.",
    inputSchema: {
      uids: z.array(z.string().min(8)).min(1).describe("Prod orderUid(s) to clone"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      submit: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "If true, also fire /submit in pre-prod (advances to inProgress). Requires explicit user authorization.",
        ),
    },
  },
  async ({ uids, profile, submit }, extra) => {
    try {
      const creds = resolveCreds(profile ?? null);
      const results: MirrorResult[] = [];
      const errors: { prodUid: string; error: string }[] = [];
      mcpLog(server, "info", `cloning ${uids.length} order(s)`, { submit: !!submit });
      for (let i = 0; i < uids.length; i++) {
        const uid = uids[i] as string;
        reportProgress(extra, i, uids.length, `cloning ${uid}`);
        try {
          const r = await mirrorOne(uid, { submit: !!submit, creds });
          results.push(r);
          mcpLog(server, "info", `cloned ${uid} -> ${r.newUid}`, { status: r.verify?.status });
        } catch (e) {
          const error = toMessage(e);
          errors.push({ prodUid: uid, error });
          mcpLog(server, "error", `clone failed for ${uid}`, { error });
        }
      }
      reportProgress(extra, uids.length, uids.length, "done");
      return ok({
        profile: profile ?? "(default)",
        submit: !!submit,
        cloned: results.length,
        failed: errors.length,
        results: [
          ...results.map((r) => ({
            prodUid: r.prodUid,
            preprodUid: r.newUid,
            status: r.verify?.status,
            submitted: r.submitted,
            memberId: r.verify?.memberId,
            icds: r.verify?.icds,
            cpts: r.verify?.cpts,
          })),
          ...errors,
        ],
      });
    } catch (e) {
      return toolError("clone_order", e, VERSION);
    }
  },
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
      "pick good uids before calling clone_order.",
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
      const prod = makeClient(creds.prod.be);
      await login(prod, creds.prod.email, creds.prod.password);
      const candidates: Record<string, unknown>[] = [];
      let scanned = 0;
      for (let page = 0; page < scanPages && candidates.length < limit; page++) {
        const r = await prod.req("POST", "/api/v1/orders/filter", {
          json: {
            pageSize: 50,
            pageNumber: page,
            type: "Outbound Referral",
            orderMode: ORDER_MODE,
          },
        });
        const rows = ordersOf(r.data);
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
      const pre = makeClient(creds.pre_prod.be);
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
      "(payload + ready-to-run curl) from its details. BUILD ONLY — never POSTs to UiPath; run the " +
      "returned curl yourself. IsApproved is always false. Returns {payload, curl, notes, meta}.",
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
      "job State, output result, error/warning robot logs, failure-language heuristics, a video link, " +
      "and a deep link into Orchestrator. READ-ONLY — never writes to UiPath or the BE. Correlates via " +
      "the job's OutputArguments (handles both the flat out_* schema and the transactionItem schema); " +
      "token/callbackContext are stripped from the returned output. Jobs live in a per-env UiPath folder " +
      "(env='prod' -> 'Authorization', env='pre_prod' -> 'Authorization Dev Clone'); pass env (default prod) " +
      "or an explicit folder. If Orchestrator rejects the OutputArguments filter, it falls back to scanning " +
      "the `top` most-recent jobs — pass `since` for prod. Optionally enrich with the order's current BE " +
      "status (same env). Returns {orderUid, env, folder, matched, jobCount, summary:{latestState,verdict,reasons}, jobs:[...]}.",
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
        .describe("Fetch robot logs (up to 200) per matched job"),
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
          const client = makeClient(envCreds.be);
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
      "IsApproved is ALWAYS forced false so a test run can never submit a real auth. If no env is " +
      "given it is derived from the URL's fid (else defaults to pre_prod). Returns " +
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
      "filtered by status. READ-ONLY, PHI-light projection (id/status/reference + member id/name). " +
      "Returns {env, queueName, queueDefinitionId, count, items[]}.",
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
      "correlation. READ-ONLY. Returns {env, folder, count, jobs:[{id,key,state,creationTime,deepLink}]}.",
    inputSchema: {
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      folder: z.string().optional().describe("Explicit folder path override (wins over env)"),
      since: z.string().optional().describe("ISO date lower bound on CreationTime"),
      top: z.number().int().min(1).max(500).optional().default(50).describe("Max jobs to return"),
    },
  },
  async ({ env, folder, since, top }) => {
    try {
      const resolved = resolveFolder(env, folder);
      const jobs = await listRecentJobs(since, top, resolved);
      return ok({
        env,
        folder: resolved,
        count: jobs.length,
        jobs: jobs.map((j) => ({
          id: j.Id,
          key: j.Key,
          state: j.State,
          creationTime: j.CreationTime,
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
      "Fetch robot execution logs (oldest first, capped 200) for a single job by its GUID Key, " +
      "optionally with the video-recording URL. READ-ONLY. Returns {jobKey, folder, logs[], videoUrl?}.",
    inputSchema: {
      jobKey: z
        .string()
        .min(1)
        .describe("The job's GUID Key (from analyze_order_execution / list_jobs)"),
      env: z
        .enum(["prod", "pre_prod"])
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone' (required)"),
      folder: z.string().optional().describe("Explicit folder path override (wins over env)"),
      includeVideo: z
        .boolean()
        .optional()
        .default(false)
        .describe("Also fetch the job's video-recording URL (extra round-trip)"),
    },
  },
  async ({ jobKey, env, folder, includeVideo }) => {
    try {
      const resolved = resolveFolder(env, folder);
      const logs = await fetchJobLogs(jobKey, resolved);
      const out: Record<string, unknown> = { jobKey, folder: resolved, logs };
      if (includeVideo) out["videoUrl"] = await fetchJobVideoUrl(jobKey, resolved);
      return ok(out);
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
      "Unchanged sections are omitted unless includeUnchanged=true. Scope with groups (top-level, " +
      "e.g. ['orders']) and/or sections (exact keys) — combined as AND; use list_setting_sections " +
      "to discover valid groups/keys. Returns " +
      "{account, prodBase, preProdBase, sectionsCompared, sectionsWithDiffs, sections:[...]}.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      groups: z
        .array(z.string())
        .optional()
        .describe("Top-level groups to diff (e.g. ['orders','providers']); omit for all"),
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
  async ({ profile, groups, sections, emr, includeUnchanged }) => {
    try {
      return ok(
        await diffSettings({
          profile: profile ?? null,
          ...(groups ? { groups } : {}),
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
    title: "List settings sections diff_settings can compare",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false, // pure catalog read — no external service
    },
    description:
      "List the settings sections diff_settings/sync_settings can scope to: each section's key, " +
      "label, top-level group, kind (object/list), whether it is 'derived' (crawled from order " +
      "types — heavier), and (for list sections) its matchKey — the field items are matched/scoped " +
      "by. Use this to drive FINE-GRAINED scoping: pass exact section keys to diff_settings' " +
      "`sections` param instead of the broader `groups`. Narrow this listing with `group` and/or " +
      "`sections`. READ-ONLY, no network (reads the static catalog). Returns {groups, sections:[...]}.",
    inputSchema: {
      group: z
        .string()
        .optional()
        .describe("Only list sections in this top-level group (e.g. 'orders')"),
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
  ({ group, sections, emr }) => {
    try {
      return ok(
        listSettingSections({
          ...(group ? { group } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
        }),
      );
    } catch (e) {
      return toolError("list_setting_sections", e, VERSION);
    }
  },
);

// ---- sync_settings -------------------------------------------------------
server.registerTool(
  "sync_settings",
  {
    title: "Additively add prod-only settings into pre-prod",
    annotations: {
      readOnlyHint: false, // creates settings in pre-prod (when dryRun=false)
      destructiveHint: false, // ADDITIVE ONLY — never overwrites or deletes existing pre-prod settings
      idempotentHint: true, // re-running once names match is a no-op
      openWorldHint: true,
    },
    description:
      "Write-side counterpart to diff_settings: ADDITIVELY copy settings that exist in PROD but " +
      "are MISSING in PRE-PROD into pre-prod. Additive only — never overwrites or deletes existing " +
      "pre-prod settings. PRE-PROD ONLY. Dry-run by default (returns the planned create/merge " +
      "actions without writing; pass dryRun:false to apply). Currently covers the outbound " +
      "order-type SPECIALTIES domain (sections specialties / referred-providers / referred-" +
      "facilities): creates prod-only specialties and merges prod-only facilities/providers into " +
      "specialties that already exist in pre-prod, remapping payer references prod->pre. Other " +
      "sections have no write mapping yet and are reported under skippedSections.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      groups: z.array(z.string()).optional().describe("Top-level groups to sync (e.g. ['orders'])"),
      sections: z.array(z.string()).optional().describe("Exact section keys to sync"),
      emr: z.string().optional().describe("EMR type to include emrDetailsSettings"),
      dryRun: z
        .boolean()
        .optional()
        .default(true)
        .describe("Preview the additions without writing them (default true)"),
    },
  },
  async ({ profile, groups, sections, emr, dryRun }) => {
    try {
      const out = await syncSettings({
        profile: profile ?? null,
        ...(groups ? { groups } : {}),
        ...(sections ? { sections } : {}),
        ...(emr ? { emr } : {}),
        dryRun: dryRun ?? true,
        // Audit trail: surface every live write to the client log.
        onWrite: (message, data) => mcpLog(server, "warning", message, data),
      });
      return ok(out);
    } catch (e) {
      return toolError("sync_settings", e, VERSION);
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
      const client = makeClient(creds.be);
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
      const client = makeClient(creds.be);
      const token = await loginToken(client, creds.email, creds.password);
      return ok({ env, profile, token });
    } catch (e) {
      return toolError("get_login_token", e, VERSION);
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
