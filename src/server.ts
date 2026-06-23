#!/usr/bin/env node

// MCP server: EHR Copilot operations (order cloning + Orchestrator execution analysis).
// Thin wiring layer — tool logic lives in:
//   - config.ts      single-file config (copilot creds + uipath args) + validation
//   - harness.ts     lazy-loaded legacy .planning clone/queue logic
//   - analyze.ts     analyze_order_execution orchestration
// Tools:
//   - clone_order              mirror prod order(s) into pre-prod (clone-only by default; submit opt-in)
//   - find_clone_candidates    list recent prod orders that are actually cloneable
//   - delete_preprod_order     delete pre-prod order(s)
//   - build_queue_item         build a UiPath AddQueueItem request from an order (build-only)
//   - analyze_order_execution  trace an order to its UiPath Orchestrator job(s) and diagnose the run (read-only)
//   - diff_settings            diff an account's settings between prod and pre-prod (read-only)
//   - list_setting_sections    list the settings sections/groups diff_settings can compare (read-only, no network)
//   - sync_settings            push settings prod -> pre-prod to reconcile drift (STUB — not implemented)
//   - get_order                fetch a single order's normalized detail by uid (read-only)
//   - doctor                   probe the BE + UiPath connections and report what's reachable (read-only)
//
// See the copilot + copilot-order-mirror skills for the full flow and quirks.

import { realpathSync } from "node:fs";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import { analyzeOrderExecution } from "./analyze.js";
import { resolveCreds } from "./config.js";
import { login, makeClient, ORDER_MODE, verify } from "./copilot-client.js";
import { runDoctor } from "./doctor.js";
import { type MirrorResult, mirrorOne } from "./mirror.js";
import { listQueue, pullQueueItem } from "./queue.js";
import { buildQueueItem } from "./queue-item.js";
import {
  ORDER_LIFECYCLE,
  PORTALS,
  QUEUE_ITEM_SCHEMA,
  RESULT_CONTRACT,
  SAFETY_RULES,
  UIPATH_FOLDERS,
} from "./reference.js";
import { diffSettings, listSettingSections, syncSettings } from "./settings.js";
import { findStuckOrders } from "./sweep.js";
import {
  fetchJobLogs,
  fetchJobVideoUrl,
  jobDeepLink,
  listRecentJobs,
  resolveFolder,
} from "./uipath.js";
import { envelopeRows, stringProp } from "./util.js";

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
const err = (message: string) => ({
  isError: true,
  content: [{ type: "text" as const, text: JSON.stringify({ error: message }, null, 2) }],
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

export const server = new McpServer({ name: "copilot", version: "1.4.0" });

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
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account; omit for default"),
      submit: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "If true, also fire /submit in pre-prod (advances to inProgress). Requires explicit user authorization.",
        ),
    },
  },
  async ({ uids, profile, submit }) => {
    try {
      const creds = resolveCreds(profile ?? null);
      const results: MirrorResult[] = [];
      const errors: { prodUid: string; error: string }[] = [];
      for (const uid of uids) {
        try {
          results.push(await mirrorOne(uid, { submit: !!submit, creds }));
        } catch (e) {
          errors.push({ prodUid: uid, error: toMessage(e) });
        }
      }
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
      return err(toMessage(e));
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
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account; omit for default"),
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
      return err(toMessage(e));
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
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account; omit for default"),
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
        results.push({ uid, status: r.status, ok: r.status < 400, msg });
      }
      return ok({
        profile: profile ?? "(default)",
        deleted: results.filter((r) => r.ok).length,
        results,
      });
    } catch (e) {
      return err(toMessage(e));
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
        .optional()
        .default("pre_prod")
        .describe("Which env the order lives in; drives fetch + token + serverURL"),
      profile: z
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account; omit for default"),
    },
  },
  async ({ orderUid, env, profile }) => {
    try {
      const out = await buildQueueItem(orderUid, {
        profile: profile ?? null,
        env: env ?? "pre_prod",
      });
      return ok(out);
    } catch (e) {
      return err(toMessage(e));
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
        .optional()
        .default("prod")
        .describe(
          "Which UiPath folder/env the job ran in: prod='Authorization', pre_prod='Authorization Dev Clone'",
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
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile for enrichOrderState; omit for default"),
    },
  },
  async ({
    orderUid,
    env,
    folder,
    since,
    top,
    includeLogs,
    includeVideo,
    enrichOrderState,
    profile,
  }) => {
    try {
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
      return ok(out);
    } catch (e) {
      return err(toMessage(e));
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
        .describe("Folder/env override; defaults from the URL fid, else pre_prod"),
      folderId: z
        .string()
        .optional()
        .describe("Numeric folder id (OrganizationUnitId) override when no url is given"),
    },
  },
  async ({ url, txnId, env, folderId }) => {
    try {
      const args = url
        ? ({ source: "url", url } as const)
        : ({
            source: "txn",
            txnId: txnId ?? 0,
            env: env ?? "pre_prod",
            folderId: folderId ?? "",
          } as const);
      return ok(await pullQueueItem(args));
    } catch (e) {
      return err(toMessage(e));
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
        .optional()
        .default("prod")
        .describe("Folder/env (queueName ids are prod; use queueDefId for pre_prod)"),
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
      return err(toMessage(e));
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
        .optional()
        .default("prod")
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone'"),
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
      return err(toMessage(e));
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
        .optional()
        .default("prod")
        .describe("prod='Authorization', pre_prod='Authorization Dev Clone'"),
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
      return err(toMessage(e));
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
      env: z.enum(["prod", "pre_prod"]).optional().default("prod").describe("Which env to scan"),
      profile: z
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account; omit for default"),
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
      return err(toMessage(e));
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
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account; omit for default"),
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
      return err(toMessage(e));
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
      "List the settings sections diff_settings can compare: each section's key, label, " +
      "top-level group, kind (object/list), and whether it is 'derived' (crawled from order " +
      "types — heavier). Use this to discover what to pass to diff_settings' groups/sections " +
      "params. READ-ONLY, no network (reads the static catalog). Returns {groups, sections:[...]}.",
    inputSchema: {
      group: z
        .string()
        .optional()
        .describe("Only list sections in this top-level group (e.g. 'orders')"),
      emr: z
        .string()
        .optional()
        .describe("Include the opt-in emr-details section for this EMR type"),
    },
  },
  ({ group, emr }) => {
    try {
      return ok(listSettingSections({ ...(group ? { group } : {}), ...(emr ? { emr } : {}) }));
    } catch (e) {
      return err(toMessage(e));
    }
  },
);

// ---- sync_settings (STUB) ------------------------------------------------
server.registerTool(
  "sync_settings",
  {
    title: "Sync settings prod -> pre-prod (NOT IMPLEMENTED)",
    annotations: {
      readOnlyHint: false, // intended to write settings to pre-prod
      destructiveHint: true, // overwrites existing pre-prod settings
      idempotentHint: true, // applying the same prod settings twice converges
      openWorldHint: true,
    },
    description:
      "STUB — NOT IMPLEMENTED. The write-side counterpart to diff_settings: it will push " +
      "selected settings sections from PROD to PRE-PROD to reconcile drift (pre-prod only, " +
      "dry-run by default). Calling it currently returns a not-implemented error. Use " +
      "diff_settings to inspect drift and apply changes manually for now.",
    inputSchema: {
      profile: z
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account; omit for default"),
      groups: z.array(z.string()).optional().describe("Top-level groups to sync (e.g. ['orders'])"),
      sections: z.array(z.string()).optional().describe("Exact section keys to sync"),
      emr: z.string().optional().describe("EMR type to include emrDetailsSettings"),
      dryRun: z
        .boolean()
        .optional()
        .default(true)
        .describe("Preview writes without applying (will default true once implemented)"),
    },
  },
  async ({ profile, groups, sections, emr, dryRun }) => {
    try {
      await syncSettings({
        profile: profile ?? null,
        ...(groups ? { groups } : {}),
        ...(sections ? { sections } : {}),
        ...(emr ? { emr } : {}),
        dryRun: dryRun ?? true,
      });
      return ok({ status: "ok" }); // unreachable while stubbed
    } catch (e) {
      return err(toMessage(e));
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
      "progress note is present. READ-ONLY. Returns {orderUid, env, ...detail} or an error if the " +
      "order is not found in that env.",
    inputSchema: {
      orderUid: z.string().min(8).describe("Order UID to fetch"),
      env: z
        .enum(["prod", "pre_prod"])
        .optional()
        .default("prod")
        .describe("Which env the order lives in"),
      profile: z
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account; omit for default"),
    },
  },
  async ({ orderUid, env, profile }) => {
    try {
      const creds = resolveCreds(profile ?? null)[env];
      const client = makeClient(creds.be);
      await login(client, creds.email, creds.password);
      const detail = await verify(client, orderUid);
      if (!detail) return err(`order ${orderUid} not found in ${env}`);
      return ok({ orderUid, env, ...detail });
    } catch (e) {
      return err(toMessage(e));
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
        .enum(["ossm", "kafri"])
        .optional()
        .describe("Credential profile / account to check; omit for default"),
    },
  },
  async ({ profile }) => {
    try {
      return ok(await runDoctor({ profile: profile ?? null }));
    } catch (e) {
      return err(toMessage(e));
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
