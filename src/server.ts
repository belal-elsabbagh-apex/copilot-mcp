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
//
// See the copilot + copilot-order-mirror skills for the full flow and quirks.

import process from "node:process";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import { analyzeOrderExecution } from "./analyze.js";
import { resolveCreds } from "./config.js";
import { login, makeClient, ORDER_MODE, verify } from "./copilot-client.js";
import { type MirrorResult, mirrorOne } from "./mirror.js";
import { buildQueueItem } from "./queue-item.js";
import { envelopeRows, stringProp } from "./util.js";

// CRITICAL: progress is logged via console.log. MCP stdio uses STDOUT for the
// JSON-RPC protocol, so any stray stdout corrupts it. Route all console.* to stderr.
console.log = (...a: unknown[]) => process.stderr.write(`${a.map(String).join(" ")}\n`);
console.info = console.log;
console.warn = console.log;

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

const server = new McpServer({ name: "copilot", version: "1.2.0" });

// ---- clone_order ---------------------------------------------------------
server.registerTool(
  "clone_order",
  {
    title: "Clone Copilot order(s) prod -> pre-prod",
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

await server.connect(new StdioServerTransport());
console.log("copilot MCP server ready (stdio)");
