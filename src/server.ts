#!/usr/bin/env node

// MCP server: EHR Copilot operations (order cloning + orchestrator execution analysis).
// Thin wiring layer — src/ is organized by domain (config/, copilot/, orchestrator/, uipath/, mcp/, shared/);
// tool logic lives in per-domain modules, e.g.:
//   - config/config.ts            single-file config (copilot creds + orchestrator args) + validation
//   - orchestrator/registry.ts    backendFor(orchestrator): the backend every orchestrator tool dispatches through
//   - orchestrator/queue-item.ts  build_queue_item's canonical order -> queue-item content
//   - uipath/backend.ts           the UiPath implementation of the orchestrator backend interface
// Tools (the orchestrator ones take an explicit orchestrator and reply { orchestrator, env, ... }):
//   - find_clone_candidates    list recent prod orders that are actually cloneable
//   - delete_preprod_order     delete pre-prod order(s)
//   - create_preprod_order     mint a fresh pre-prod order from an explicit spec (stops at forReview, never submits)
//   - submit_preprod_order     submit a pre-prod order sitting at forReview (explicit, separate write)
//   - build_queue_item         build an orchestrator queue-item request from an order (build-only)
//   - find_order_queue_items   find the queue item(s) + job ids an order flowed through, across every orchestrator (read-only)
//   - pull_queue_item          fetch one queue item as a ready-to-run test payload, IsApproved forced false (read-only)
//   - list_queue_items         browse a portal's queue for triage (read-only)
//   - list_jobs                list recent jobs in a folder (read-only)
//   - get_job_logs             fetch one job's logs, or several via jobIds (read-only)
//   - build_faulted_job_issue  build a GitHub issue payload for a faulted job (read-only; posts nothing)
//   - list_queues              list queues in a folder (read-only; discovers pre-prod queue ids)
//   - list_processes           list processes in a folder (read-only; processId + pin verification)
//   - list_triggers            list triggers in a folder (read-only; verify queue trigger -> process wiring)
//   - get_job                  fetch one job's state/output by id, or several via jobIds (read-only)
//   - get_job_artifacts        list or download an Apex job's artifacts (read-only; files land under ~/.cache/copilot-mcp/artifacts)
//   - add_queue_item           POST one item to a pre-prod queue (pre_prod-only; test-safety guarded)
//   - delete_queue_item        delete a queue item from the pre-prod folder (pre_prod-only)
//   - start_job                start job(s) for a pre-prod process (pre_prod-only)
//   - stop_job                 stop or kill a pre-prod job (pre_prod-only; fetch-first)
//   - find_stuck_orders        scan recent orders for non-terminal ('stuck') statuses, optionally cross-checked against every orchestrator (read-only)
//   - diff_settings            diff an account's settings between prod and pre-prod (read-only)
//   - list_setting_sections    list the settings sections/tags the settings tools can scope to (read-only, no network)
//   - get_settings             fetch one env's settings sections (read-only)
//   - plan_settings_sync       plan the additive prod -> pre-prod sync actions (read-only)
//   - apply_settings_sync      execute selected planned sync actions against pre-prod (additive only)
//   - get_order                fetch a single order's normalized detail by uid (read-only)
//   - search_orders            filter orders by location/insurance/referredTo/type/auth/date range (read-only)
//   - get_order_category_stats per-folder order counts for a given filter (read-only)
//   - get_login_token          log into the Copilot BE for a profile/env and return the session JWT (read-only)
//   - list_clinics             list the clinics the support account can switch into (read-only)
//   - build_mcp_issue          compose a GitHub issue for feedback/bugs on this server (posts nothing)
//   - doctor                   probe the BE + orchestrator (UiPath, Apex) connections and report what's reachable (read-only)
//
// See the copilot + copilot-order-mirror skills for the full flow and quirks.

import { realpathSync } from "node:fs";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { McpServer, ResourceTemplate } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import { downloadJobArtifacts, listJobArtifacts } from "./apex/artifacts.js";
import { configStatus, getFeedbackConfig, loadConfig, onConfigReload } from "./config/config.js";
import {
  assertPreProdClient,
  type BeOrder,
  fetchOrder,
  filterOrders,
  normalizeOrder,
  ORDER_MODE,
  submitOrder,
  toMDY,
} from "./copilot/copilot-client.js";
import { runDoctor } from "./copilot/doctor.js";
import { type MintSpec, mintPreprodOrder } from "./copilot/mirror.js";
import { orderDocuments } from "./copilot/order-docs.js";
import { getOrderCategoryStats, searchOrders } from "./copilot/order-search.js";
import { connect, listClinics } from "./copilot/session.js";
import {
  applySettingsSync,
  diffSettings,
  getSettings,
  listSettingSections,
  planSettingsSyncOp,
} from "./copilot/settings/index.js";
import { findStuckOrders } from "./copilot/sweep.js";
import { formatMcpIssue, toolError } from "./mcp/feedback.js";
import {
  mcpLog,
  registerLogging,
  reportProgress,
  stepProgress,
  withHeartbeat,
} from "./mcp/notify.js";
import { registerPrompts } from "./mcp/prompts.js";
import {
  ACCOUNT_AUTOMATION_IDS,
  CONFIG_GUIDE,
  ORDER_LIFECYCLE,
  PORTALS,
  type PortalEntry,
  QUEUE_ITEM_SCHEMA,
  RESULT_CONTRACT,
  SAFETY_RULES,
  UIPATH_FOLDERS,
} from "./mcp/reference.js";
import { findOrderAcross, selectBackends } from "./orchestrator/find-order.js";
import { jobEntriesEnvelope, resolveJobIds } from "./orchestrator/job-batch.js";
import { buildQueueItemContent } from "./orchestrator/queue-item.js";
import { backendFor, ENABLED_ORCHESTRATORS } from "./orchestrator/registry.js";
import { QUEUE_ITEM_STATUSES } from "./orchestrator/types.js";
import { stringProp } from "./shared/util.js";

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
const VERSION = "1.33.1";

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
  async ({ profile, limit, scanPages }, extra) => {
    try {
      const { client: prod } = await connect("prod", profile);
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
        reportProgress(extra, page + 1, scanPages, `page ${page + 1}: ${candidates.length} found`);
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
      const { client: pre } = await connect("pre_prod", profile);
      assertPreProdClient(pre, "delete_preprod_order");
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
  async (a, extra) => {
    try {
      const { client: pre } = await connect("pre_prod", a.profile);
      // Ported by mintPreprodOrder alongside its own console.log calls — surfaces
      // its ~15-step sequence (up to 6 /process retries at 5s apart) to the client
      // instead of leaving a multi-retry mint with no visible feedback at all.
      let step = 0;
      const onProgress = (message: string): void => {
        step++;
        reportProgress(extra, step, undefined, message);
      };
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
      const result = await mintPreprodOrder(pre, spec, onProgress);
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
      const { client: pre } = await connect("pre_prod", profile);
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

// ---- Orchestrator tools: shared argument schemas ---------------------------
// Every orchestrator tool takes an explicit env and orchestrator (never defaulted) and
// replies { orchestrator, env, ...result } so agents read one vocabulary across backends.
const orchestratorArg = z
  .enum(ENABLED_ORCHESTRATORS)
  .describe(`Which orchestrator: ${ENABLED_ORCHESTRATORS.join(" | ")} (required)`);
const envArg = z
  .enum(["prod", "pre_prod"])
  .describe(
    "Selects the orchestrator's folder: UiPath prod='Authorization', pre_prod='Authorization Dev Clone'; " +
      "Apex prod='Authorization', pre_prod='Authorization Test' (required)",
  );
const writeEnvArg = z
  .literal("pre_prod")
  .describe(
    "Orchestrator writes are pre_prod-only (UiPath dev clone 'Authorization Dev Clone', Apex " +
      "'Authorization Test'); 'prod' is rejected by the schema (required)",
  );

// ---- build_queue_item ----------------------------------------------------
server.registerTool(
  "build_queue_item",
  {
    title: "Build an orchestrator queue-item request from an order",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    }, // BUILD ONLY — fetches an order, never POSTs
    description:
      "Fetch an EHR Copilot order (prod or pre-prod) and build the queue-item request payload for " +
      "the chosen orchestrator from its details. BUILD ONLY — never POSTs to the orchestrator; " +
      "submit it yourself (for UiPath, to meta.postUrl with your own Orchestrator bearer token — see " +
      "notes; or hand payload.itemData's Name/Reference/SpecificContent to add_queue_item). " +
      "IsApproved is always false. The payload's wire format is the orchestrator's own: UiPath " +
      "carries the array fields and callbackContext as JSON strings, Apex keeps native types. " +
      "Returns {payload, notes, meta}.",
    inputSchema: {
      orderUid: z.string().min(8).describe("Order UID to fetch (must exist in the chosen env)"),
      env: z
        .enum(["prod", "pre_prod"])
        .describe("Which env the order lives in; drives fetch + token + serverURL (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
      orchestrator: orchestratorArg,
      automationId: z
        .string()
        .min(1)
        .optional()
        .describe(
          "The account's real automation id (from its orchestrator asset). Usually unnecessary — " +
            "when omitted, it's resolved from `profile` against the known account table; only " +
            "needed to override that or for an account not yet in the table (falls back to a " +
            "placeholder random UUID + warning note in that case).",
        ),
    },
  },
  async ({ orderUid, env, profile, orchestrator, automationId }, extra) => {
    try {
      const built = await withHeartbeat(
        extra,
        "building queue item",
        buildQueueItemContent(orderUid, { profile, env, automationId: automationId ?? null }),
      );
      const { payload, notes, meta } = backendFor(orchestrator).buildQueueItemPayload({
        env,
        queueName: built.queueName,
        orderUid,
        content: built.content,
        beBase: built.beBase,
      });
      return ok({ payload, notes: [...built.notes, ...notes], meta: { ...built.meta, ...meta } });
    } catch (e) {
      return toolError("build_queue_item", e, VERSION);
    }
  },
);

// ---- find_order_queue_items ------------------------------------------------
server.registerTool(
  "find_order_queue_items",
  {
    title: "Find the queue item(s) a Copilot order flowed through",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Given a Copilot orderUid, find the orchestrator queue item(s) it flowed through and the " +
      "job ids that ran them — every order flows through a queue item carrying the orderUid and " +
      "its job(s), so this reaches faulted/still-running consumer jobs an OutputArguments scan " +
      "can't. Omit `orchestrator` to search EVERY orchestrator (uipath | apex) in parallel: routing " +
      "is decided per portal outside this repo, so where the item actually sits is the truth. Each " +
      "orchestrator fails independently into `searchErrors` without losing the others' items. " +
      "`folder` is accepted only together with an explicit `orchestrator`. READ-ONLY. The item's " +
      "full specificContent is returned deliberately, in full (member/clinical fields visible) — " +
      "the point is divergence analysis: comparing what Copilot actually sent against the order " +
      "record from get_order. Only the JWT `token` is redacted (a live credential, opaque, useless " +
      "for divergence). Feed `jobIds[<orchestrator>]` into get_job's jobIds with THAT orchestrator " +
      "and includeLogDigest=true to diagnose every run and retry in one call per orchestrator " +
      "(Apex lists every attempt of a retried item). `count === 0` with no `searchErrors` means the " +
      "order never reached any searched orchestrator; with `searchErrors` it is inconclusive for " +
      "the failed ones. `jobIds[o]: []` with items present means queued but not yet picked up by a " +
      "worker. Env picks each orchestrator's folder (see copilot://reference/orchestrators). " +
      "`since` narrows the queue-item search (each orchestrator retries unbounded if nothing " +
      "matches) — pass the order's creationDate from get_order. Returns {orderUid, env, count, " +
      "jobIds:{<orchestrator>:string[]}, items:QueueItemCore[] (newest first), searched, " +
      "backends:{<orchestrator>:{folder,searchMode,scanned}}, notes? (prefixed '<orchestrator>: '), " +
      "searchErrors?:{<orchestrator>:string}}.",
    inputSchema: {
      orderUid: z.string().min(8).describe("Copilot orderUid to correlate"),
      env: envArg,
      orchestrator: orchestratorArg
        .optional()
        .describe(
          "Search only this orchestrator (uipath | apex); omit to search every orchestrator",
        ),
      folder: z
        .string()
        .optional()
        .describe(
          "Explicit folder override for the one orchestrator named by `orchestrator` (wins " +
            "over env); refused without `orchestrator`",
        ),
      since: z
        .string()
        .optional()
        .describe(
          "ISO lower bound on the queue item's creation time — pass the order's creationDate " +
            "from get_order; each orchestrator retries without the bound if nothing matches",
        ),
      top: z
        .number()
        .int()
        .min(1)
        .max(500)
        .optional()
        .default(50)
        .describe("Max queue items per orchestrator"),
      includeSpecificContent: z.boolean().optional().default(true),
    },
  },
  async ({ orderUid, env, orchestrator, folder, since, top, includeSpecificContent }, extra) => {
    try {
      const found = await withHeartbeat(
        extra,
        "Orchestrator",
        findOrderAcross(
          { env, orderUid, folder, since, top, includeSpecificContent },
          selectBackends(orchestrator, folder),
        ),
      );
      return ok({ orderUid, env, ...found });
    } catch (e) {
      return toolError("find_order_queue_items", e, VERSION);
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
  "account-automation-ids",
  "copilot://reference/account-automation-ids",
  "Account automation ids",
  "Real UiPath automation ids per account (build_queue_item's automationId arg), confirmed from a successful queue item's SpecificContent. Accounts with no successful job yet are absent, not guessed.",
  ACCOUNT_AUTOMATION_IDS,
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
    title: "Pull a queue item as a test payload",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch a single orchestrator queue item and return its specificContent as a ready-to-run " +
      "test payload, mapped to its portal. READ-ONLY. IsApproved is ALWAYS forced false so a test " +
      "run can never submit a real auth. UiPath: pass `url` (an Orchestrator queue-item URL — env " +
      "is derived from its fid) or `itemId` + `env` (+ optional `folderId`). Apex: pass `env` + " +
      "`queueId` + `itemId`. Returns {orchestrator, env, item:QueueItemCore (specificContent " +
      "present), portal:{matched,key,portalDir,buildArtifact,family}, isApprovedForced, " +
      "suggestedFilename, queueNameError?}.",
    inputSchema: {
      orchestrator: orchestratorArg,
      env: z
        .enum(["prod", "pre_prod"])
        .optional()
        .describe(
          "Folder/env; UiPath derives it from the URL's fid when a url is given, otherwise required",
        ),
      url: z
        .string()
        .url()
        .optional()
        .describe(
          "UiPath only: Orchestrator queue-item URL (copy from the UI; fid + item id are parsed out)",
        ),
      queueId: z
        .string()
        .min(1)
        .optional()
        .describe(
          "Apex: the queue's id (from list_queues); UiPath derives the queue from the item",
        ),
      itemId: z
        .string()
        .min(1)
        .optional()
        .describe("Queue item id (UiPath numeric id as a string | Apex uuid); use instead of url"),
      folderId: z
        .string()
        .optional()
        .describe("UiPath: numeric folder id (OrganizationUnitId) override when no url is given"),
    },
  },
  async ({ orchestrator, env, url, queueId, itemId, folderId }, extra) => {
    try {
      const result = await withHeartbeat(
        extra,
        "Orchestrator",
        backendFor(orchestrator).pullQueueItem({ env, url, queueId, itemId, folderId }),
      );
      return ok({ orchestrator, ...result });
    } catch (e) {
      return toolError("pull_queue_item", e, VERSION);
    }
  },
);

// ---- list_queue_items ----------------------------------------------------
server.registerTool(
  "list_queue_items",
  {
    title: "Browse a queue",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List items in a portal's queue for triage, newest first. `queue` is a portal key/alias " +
      "(e.g. 'PMG', 'MOLINA' — resolved to that portal's submit or sync queue) or the " +
      "orchestrator's own queue id (from list_queues; required for pre_prod UiPath queues, whose " +
      "ids differ from the static portal registry). Optionally filter by status. READ-ONLY, " +
      "PHI-light projection (specificContent is null; member id/name live under each item's " +
      "details). Returns {orchestrator, env, queueId, queueName, count, items:[{orchestrator," +
      "queueId,queueName,itemId,reference,orderUid,status,rawStatus,exceptionType,createdAt," +
      "endedAt,jobs,specificContent:null,details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      queue: z
        .string()
        .min(1)
        .describe("Portal key/alias, e.g. 'PMG', or the orchestrator's queue id (see list_queues)"),
      queueKind: z
        .enum(["submit", "sync"])
        .optional()
        .default("submit")
        .describe("Which queue for a portal key (default submit)"),
      status: z.enum(QUEUE_ITEM_STATUSES).optional().describe("Optional queue-item status filter"),
      top: z.number().int().min(1).max(200).optional().default(50).describe("Max items to return"),
    },
  },
  async ({ env, orchestrator, queue, queueKind, status, top }, extra) => {
    try {
      const result = await withHeartbeat(
        extra,
        "Orchestrator",
        backendFor(orchestrator).listQueueItems({ env, queue, queueKind, status, top }),
      );
      return ok({ orchestrator, env, count: result.items.length, ...result });
    } catch (e) {
      return toolError("list_queue_items", e, VERSION);
    }
  },
);

// ---- list_jobs -----------------------------------------------------------
server.registerTool(
  "list_jobs",
  {
    title: "List recent orchestrator jobs",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the most recent jobs in an env's folder (newest first), without order correlation. " +
      "READ-ONLY. To diagnose a job from the list, call get_job with includeLogDigest=true " +
      "(condensed failure digest + fault signature) — get_job also takes a jobIds array to " +
      "diagnose several jobs from this list in one call. Returns {orchestrator, env, folder, " +
      "count, jobs:[{orchestrator,id,state (dashboard label),rawState (the backend's own status " +
      "string),terminal,process:{id,name,version},queueItem:{queueId,itemId,reference}|null," +
      "createdAt,startedAt,endedAt,durationMs,attempt,retryOfJobId,fault,link (dashboard deep " +
      "link),details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      since: z.string().optional().describe("ISO date lower bound on creation time"),
      top: z.number().int().min(1).max(500).optional().default(50).describe("Max jobs to return"),
      processName: z
        .string()
        .optional()
        .describe("Substring filter on the process name, e.g. 'OPTUM'"),
    },
  },
  async ({ env, orchestrator, folder, since, top, processName }, extra) => {
    try {
      const result = await withHeartbeat(
        extra,
        "Orchestrator",
        backendFor(orchestrator).listJobs({ env, folder, since, top, processName }),
      );
      return ok({
        orchestrator,
        env,
        folder: result.folder,
        count: result.jobs.length,
        jobs: result.jobs,
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
    title: "Get a job's robot logs",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch execution logs (oldest first, capped 500 per job) for a job by its id, optionally " +
      "with the video recording. Pass jobIds (up to 25) instead of jobId to fetch logs for several " +
      "jobs in ONE call — use this instead of looping get_job_logs once per job. All row filters " +
      "are opt-in and apply to every job in the batch — by default the full (capped) log set is " +
      "returned, but each Message is truncated to 400 chars (pass fullMessages=true for the " +
      "untruncated text, e.g. to read a complete stack trace). READ-ONLY. A jobIds batch never " +
      "fails as a whole on one job's error — that job's entry carries error instead, the rest " +
      "still return. `video` is always best-effort: a failure there surfaces as videoError, never " +
      "blocks the logs; it is {kind:'url',url} when the orchestrator serves a link, or " +
      "{kind:'artifact',artifact,sizeBytes,fetchWith:'get_job_artifacts'} when the recording is a " +
      "job artifact to download. A job whose logs cannot be read carries logsUnavailable " +
      "{reason: running | never-started | artifact-policy, detail}. includeRawFields=true adds " +
      "each row's backend-specific structured fields as `raw` (UiPath: the parsed RawMessage — " +
      "transactionId, queueName, processingExceptionType/Reason, execution timings, any custom " +
      "Add-Log-Fields data, ~3x the response size on real jobs; Apex: the bot's NDJSON `fields`); " +
      "`raw` is null when a row has none. Returns {orchestrator, env, folder, jobId, logs:[{Level," +
      "Message,TimeStamp,raw?}], returned, totalMatching?, truncated, logsUnavailable?, video?, " +
      "videoError?, error?} for a single jobId, or {orchestrator, env, folder, count, jobs:[{jobId," +
      "logs,returned,totalMatching?,truncated,logsUnavailable?,error?,video?,videoError?}]} for jobIds.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      jobId: z
        .string()
        .min(1)
        .optional()
        .describe(
          "The job's id (from find_order_queue_items / list_jobs). Provide this OR jobIds, not both",
        ),
      jobIds: z
        .array(z.string().min(1))
        .min(1)
        .max(25)
        .optional()
        .describe(
          "Batch: fetch logs for several jobs in one call. Provide this OR jobId, not both",
        ),
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      includeVideo: z
        .boolean()
        .optional()
        .default(false)
        .describe("Also fetch the job's video recording (extra round-trip)"),
      minLevel: z
        .enum(["warn", "error"])
        .optional()
        .describe(
          "Only logs at or above this level (warn=Warn/Error/Fatal, error=Error/Fatal). " +
            "Default: all levels",
        ),
      contains: z
        .string()
        .min(1)
        .optional()
        .describe("Only logs whose Message contains this substring (case-sensitive)"),
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
            "get_job's log digest). Use when you need a complete stack trace.",
        ),
      includeRawFields: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Attach each row's backend-specific structured fields as `raw` — see the tool " +
            "description. Off by default: UiPath's parsed RawMessage is ~3x the response size.",
        ),
    },
  },
  async (
    {
      env,
      orchestrator,
      jobId,
      jobIds,
      folder,
      includeVideo,
      minLevel,
      contains,
      onlyFailures,
      tail,
      fullMessages,
      includeRawFields,
    },
    extra,
  ) => {
    try {
      const { ids, batch } = resolveJobIds(jobId, jobIds);
      const result = await backendFor(orchestrator).getJobLogs(
        {
          env,
          jobIds: ids,
          batch,
          folder,
          filter: { minLevel, contains, onlyFailures, tail },
          fullMessages,
          includeRawFields,
          includeVideo,
        },
        stepProgress(extra),
      );
      return ok(jobEntriesEnvelope(orchestrator, env, result.folder, batch, result.entries));
    } catch (e) {
      return toolError("get_job_logs", e, VERSION);
    }
  },
);

// ---- build_faulted_job_issue ---------------------------------------------
server.registerTool(
  "build_faulted_job_issue",
  {
    title: "Build a GitHub issue payload for a faulted job",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Read a faulted job (by its id) and its logs, and BUILD a ready-to-post GitHub issue " +
      "payload for the RPA repo. READ-ONLY: this does NOT post to GitHub — it returns " +
      "{orchestrator,env,repo,title,body,labels,faultSignature,searchQuery,recurrenceComment," +
      "found,logsError?}. The logs fetch is best-effort: a failure surfaces as logsError (issue " +
      "still built from the job alone) instead of failing the whole call. The caller (the " +
      "report-faulted-jobs prompt) hands this to the host's GitHub MCP server: search " +
      "with `searchQuery`, then add `recurrenceComment` to the matching open issue, or " +
      "create_issue with title/body/labels. `faultSignature` is the normalized error (stable " +
      "across reruns) used to dedupe.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      jobId: z.string().min(1).describe("The faulted job's id (from list_jobs)"),
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      repo: z
        .string()
        .optional()
        .describe("Target repo owner/name (default Apex-Medical-AI-Inc/RPAPlaywright)"),
      labels: z.array(z.string()).optional().describe("Issue labels (default uipath-fault, bug)"),
    },
  },
  async ({ env, orchestrator, jobId, folder, repo, labels }) => {
    try {
      const result = await backendFor(orchestrator).buildFaultedJobIssue({
        env,
        jobId,
        folder,
        repo,
        labels,
      });
      return ok({ orchestrator, env, ...result });
    } catch (e) {
      return toolError("build_faulted_job_issue", e, VERSION);
    }
  },
);

// ---- list_queues -----------------------------------------------------------
server.registerTool(
  "list_queues",
  {
    title: "List queues in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the queues (the queues themselves) in an env's folder, sorted by name. READ-ONLY. Use " +
      "this to discover a queue id for list_queue_items / pull_queue_item and the exact queue " +
      "name for add_queue_item — pre_prod queue ids differ from the prod ids in the static portal " +
      "registry. Queue CREATION is deliberately not a tool (rare one-time setup — do it in the " +
      "orchestrator UI). Returns {orchestrator, env, count, queues:[{orchestrator,id,name," +
      "processId,active,details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      nameContains: z.string().optional().describe("Substring filter on the queue name"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max queues"),
    },
  },
  async ({ env, orchestrator, folder, nameContains, top }) => {
    try {
      const queues = await backendFor(orchestrator).listQueues({ env, folder, nameContains, top });
      return ok({ orchestrator, env, count: queues.length, queues });
    } catch (e) {
      return toolError("list_queues", e, VERSION);
    }
  },
);

// ---- list_processes --------------------------------------------------------
server.registerTool(
  "list_processes",
  {
    title: "List processes in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the processes (UiPath releases / Apex workflows) in an env's folder, sorted by name. " +
      "READ-ONLY. `id` is what start_job takes as processId; `version` is the pinned package " +
      "version — a UiPath job resolves it at START, so verify the pin here right before starting " +
      "(repinning is manual in the orchestrator UI; this MCP never repins). Returns " +
      "{orchestrator, env, count, processes:[{orchestrator,id,name,package,version,active,details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      nameContains: z.string().optional().describe("Substring filter on the process name"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max processes"),
    },
  },
  async ({ env, orchestrator, folder, nameContains, top }) => {
    try {
      const processes = await backendFor(orchestrator).listProcesses({
        env,
        folder,
        nameContains,
        top,
      });
      return ok({ orchestrator, env, count: processes.length, processes });
    } catch (e) {
      return toolError("list_processes", e, VERSION);
    }
  },
);

// ---- list_triggers ---------------------------------------------------------
server.registerTool(
  "list_triggers",
  {
    title: "List triggers (queue + time) in a folder",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the triggers (queue + time schedules) in an env's folder. READ-ONLY. Queue triggers " +
      "carry a queueId + the processId they start — use this to verify a queue's trigger points " +
      "at YOUR pre-prod process before enqueueing (cross-check processId against list_processes). " +
      "Time triggers have queueId null and a cron. Returns {orchestrator, env, count, " +
      "triggers:[{orchestrator,id,name,kind:'queue'|'time',processId,queueId,enabled,cron," +
      "details}]}.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      queueId: z.string().min(1).optional().describe("Only triggers watching this queue"),
      top: z.number().int().min(1).max(200).optional().default(100).describe("Max triggers"),
    },
  },
  async ({ env, orchestrator, folder, queueId, top }) => {
    try {
      const triggers = await backendFor(orchestrator).listTriggers({ env, folder, queueId, top });
      return ok({ orchestrator, env, count: triggers.length, triggers });
    } catch (e) {
      return toolError("list_triggers", e, VERSION);
    }
  },
);

// ---- get_job ---------------------------------------------------------------
server.registerTool(
  "get_job",
  {
    title: "Get a single job's state by id",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "Fetch one job by its id — the light poll target after start_job, and the job-first " +
      "diagnostic when you have an id but no orderUid (list_jobs is an unkeyed scan; " +
      "find_order_queue_items is order-correlated). Pass jobIds (up to 25) instead of jobId to " +
      "diagnose several jobs — e.g. every job list_jobs just returned — in ONE call instead of " +
      "looping get_job per id. READ-ONLY. `job` is the normalized job (state = dashboard label, " +
      "rawState = the backend's own status, terminal, process, queueItem, timings, attempt, " +
      "retryOfJobId, fault, link, details — UiPath details carry the parsed output with " +
      "token/callbackContext stripped). With includeLogDigest=true it also returns the log digest " +
      "(failure-focused condensed logs + stalls) and a structured fault (headline error, stable " +
      "signature, class); UiPath adds transactionOutcome — the queue transaction's OWN " +
      "processingExceptionType/Reason and timing from the Transaction-Ended log line, more " +
      "reliable for exception classification than the free-text-derived class, null when the job " +
      "never reached one; Apex adds run {exitCode, outcome, errorClass, timedOut, cancelled}. " +
      "When a job has no readable log, logsUnavailable {reason: running | never-started | " +
      "artifact-policy, detail} says why. If the digest is not enough, call get_job_logs (also " +
      "batchable via jobIds) for the complete logs. A jobIds batch never fails as a whole on one " +
      "id's error — that entry carries error instead, the rest still return; a single jobId " +
      "fails the call normally, like any other error. includeLogDigest is always best-effort: a " +
      "failure fetching it surfaces as logDigestError, never blocks the job data. Returns " +
      "{orchestrator, env, folder, id, found, job?, logDigest?, logsUnavailable?, logDigestError?, " +
      "error?} for a single jobId, or {orchestrator, env, folder, count, jobs:[{id,found,job?," +
      "logDigest?,logsUnavailable?,logDigestError?,error?}]} for jobIds.",
    inputSchema: {
      env: envArg,
      orchestrator: orchestratorArg,
      jobId: z
        .string()
        .min(8)
        .optional()
        .describe("The job's id (from start_job / list_jobs). Provide this OR jobIds, not both"),
      jobIds: z
        .array(z.string().min(8))
        .min(1)
        .max(25)
        .optional()
        .describe("Batch: diagnose several jobs in one call. Provide this OR jobId, not both"),
      folder: z.string().optional().describe("Explicit folder name override (wins over env)"),
      includeLogDigest: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Also fetch each job's logs (extra call per job) and return a condensed failure " +
            "digest + structured fault — use get_job_logs for the complete logs",
        ),
    },
  },
  async ({ env, orchestrator, jobId, jobIds, folder, includeLogDigest }, extra) => {
    try {
      const { ids, batch } = resolveJobIds(jobId, jobIds);
      // With includeLogDigest a 25-id batch is 50 orchestrator calls — the backend counts
      // them off through stepProgress as each id finishes.
      const result = await backendFor(orchestrator).getJobs(
        { env, jobIds: ids, batch, folder, includeLogDigest },
        stepProgress(extra),
      );
      return ok(jobEntriesEnvelope(orchestrator, env, result.folder, batch, result.entries));
    } catch (e) {
      return toolError("get_job", e, VERSION);
    }
  },
);

// ---- get_job_artifacts -----------------------------------------------------
server.registerTool(
  "get_job_artifacts",
  {
    title: "List or download an Apex job's artifacts",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List an Apex job's artifacts ({filename, type, sizeBytes}: job.log, bot.log, debug.json, " +
      "result.json, screenshots, trace.zip, recording.mp4), or download chosen ones with `download` " +
      '(artifact filenames, or "all") to ~/.cache/copilot-mcp/artifacts/<jobId>/ and get back the ' +
      "local paths + sizes. Apex-only (UiPath has no artifacts) — no `orchestrator` argument. " +
      "READ-ONLY on Apex; only local files are written. PHI WARNING: screenshots, trace.zip, " +
      "recordings and logs can contain member data — they are written OUTSIDE any repo with " +
      "owner-only permissions; never commit, attach or paste them. An unknown filename fails with " +
      "the list of available names. For parsed, filterable logs use get_job_logs instead; use this " +
      "for trace.zip, screenshots, recordings and raw files. Returns {env,folder,artifacts[]} " +
      "when listing, {env,dir,files:[{filename,path,sizeBytes}]} when downloading.",
    inputSchema: {
      env: envArg,
      jobId: z.string().min(8).describe("The Apex job id (from get_job / find_order_queue_items)"),
      folder: z
        .string()
        .optional()
        .describe("Explicit folder name override for the listing (wins over env)"),
      download: z
        .union([z.literal("all"), z.array(z.string().min(1)).min(1).max(50)])
        .optional()
        .describe(
          'Artifact filenames to download (e.g. ["trace.zip"]) or "all". Omit to only list.',
        ),
    },
  },
  async ({ env, jobId, folder, download }, extra) => {
    try {
      if (!download) {
        const listing = await withHeartbeat(extra, "Apex", listJobArtifacts(env, jobId, folder));
        return ok({ env, ...listing });
      }
      const downloaded = await withHeartbeat(
        extra,
        "Apex",
        downloadJobArtifacts(env, jobId, download, undefined, folder),
      );
      return ok({ env, ...downloaded });
    } catch (e) {
      return toolError("get_job_artifacts", e, VERSION);
    }
  },
);

// ---- add_queue_item --------------------------------------------------------
server.registerTool(
  "add_queue_item",
  {
    title: "Post a queue item to a pre-prod queue",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // additive — creates an item, never overwrites
      idempotentHint: false, // each call mints a new item
      openWorldHint: true,
    },
    description:
      "POST one item to a queue in the PRE-PROD folder (pre_prod ONLY — the schema rejects prod). " +
      "Every payload passes the test-safety guard first: IsApproved is forced false; a non-empty " +
      "serverURL/queueUrl must match the config's pre-prod values; <TO-FILL> placeholders are " +
      "rejected. To post a build_queue_item result, pass payload.itemData.Name as queue, " +
      "payload.itemData.Reference as reference and payload.itemData.SpecificContent as " +
      "specificContent. priority applies to UiPath only (Apex uses the queue's default). When " +
      "enqueueing many items, pace ~1/s. Returns {orchestrator, env, queueId, queueName, " +
      "reference, itemId, status, forced}.",
    inputSchema: {
      env: writeEnvArg,
      orchestrator: orchestratorArg,
      queue: z
        .string()
        .min(1)
        .describe(
          "Queue in the pre-prod folder: UiPath exact queue Name; Apex queue name or id (see list_queues)",
        ),
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
  async ({ env, orchestrator, queue, reference, specificContent, priority }) => {
    try {
      const result = await backendFor(orchestrator).addQueueItem({
        env,
        queue,
        reference,
        priority,
        specificContent,
      });
      // Audit trail: surface every orchestrator write to the client log.
      mcpLog(server, "warning", `posted queue item to pre-prod queue '${queue}'`, {
        orchestrator,
        reference,
        itemId: result.itemId,
        forced: result.forced,
      });
      return ok({ orchestrator, ...result }); // result carries env
    } catch (e) {
      return toolError("add_queue_item", e, VERSION);
    }
  },
);

// ---- delete_queue_item -----------------------------------------------------
server.registerTool(
  "delete_queue_item",
  {
    title: "Delete a New queue item from the pre-prod folder",
    annotations: {
      readOnlyHint: false,
      destructiveHint: true, // deletes an item
      idempotentHint: true, // re-deleting fails safe (status is no longer New)
      openWorldHint: true,
    },
    description:
      "Delete one queue item from the PRE-PROD folder (pre_prod ONLY — the schema rejects prod). " +
      "UiPath is fetch-first: it refuses unless the item's current Status is 'New', so " +
      "InProgress/Successful/Failed history is never destroyed. Use to clear stale test items " +
      "before re-enqueueing after a re-mint. Returns {orchestrator, env, itemId, deleted, " +
      "previousStatus, reference}.",
    inputSchema: {
      env: writeEnvArg,
      orchestrator: orchestratorArg,
      itemId: z
        .string()
        .min(1)
        .describe("Queue item id (UiPath: a positive integer; from list_queue_items)"),
      queueId: z
        .string()
        .min(1)
        .optional()
        .describe("Queue id (required by Apex; ignored by UiPath)"),
    },
  },
  async ({ env, orchestrator, itemId, queueId }) => {
    try {
      const result = await backendFor(orchestrator).deleteQueueItem({ env, itemId, queueId });
      mcpLog(server, "warning", `deleted pre-prod queue item ${itemId}`, {
        orchestrator,
        reference: result.reference,
      });
      return ok({ orchestrator, ...result }); // result carries env
    } catch (e) {
      return toolError("delete_queue_item", e, VERSION);
    }
  },
);

// ---- start_job -------------------------------------------------------------
server.registerTool(
  "start_job",
  {
    title: "Start a job on the pre-prod folder",
    annotations: {
      readOnlyHint: false,
      destructiveHint: false, // starts a run; existing state untouched
      idempotentHint: false, // each call starts new job(s)
      openWorldHint: true,
    },
    description:
      "Start job(s) for a process in the PRE-PROD folder (pre_prod ONLY — the schema rejects " +
      "prod). Get the processId from list_processes. PIN RACE (UiPath): a job resolves its " +
      "package version at START, not creation — if others are publishing, verify the process's " +
      "version via list_processes right before starting (repinning is manual in the orchestrator " +
      "UI; this MCP never repins). Poll the result with get_job. Returns {orchestrator, env, " +
      "jobs:[{id,state,processName,link}]}.",
    inputSchema: {
      env: writeEnvArg,
      orchestrator: orchestratorArg,
      processId: z.string().min(8).describe("Process id (from list_processes)"),
      input: z
        .record(z.unknown())
        .optional()
        .default({})
        .describe("Job input arguments (omit for none)"),
      jobsCount: z.number().int().min(1).max(5).optional().default(1).describe("Jobs to start"),
      matchValue: z
        .string()
        .min(1)
        .optional()
        .describe("Apex only: the match value the job is dispatched under (required by Apex)"),
    },
  },
  async ({ env, orchestrator, processId, input, jobsCount, matchValue }) => {
    try {
      const result = await backendFor(orchestrator).startJob({
        env,
        processId,
        input,
        jobsCount,
        matchValue,
      });
      mcpLog(server, "warning", `started ${result.jobs.length} pre-prod job(s)`, {
        orchestrator,
        processId,
        ids: result.jobs.map((j) => j.id),
      });
      return ok({ orchestrator, ...result }); // result carries env
    } catch (e) {
      return toolError("start_job", e, VERSION);
    }
  },
);

// ---- stop_job ---------------------------------------------------------------
server.registerTool(
  "stop_job",
  {
    title: "Stop or kill a running job on the pre-prod folder",
    annotations: {
      readOnlyHint: false,
      destructiveHint: true, // aborts a run in progress
      idempotentHint: true, // fetch-first: refuses (no write) once already terminal
      openWorldHint: true,
    },
    description:
      "Stop (SoftStop, graceful) or Kill (immediate) a job in the PRE-PROD folder (pre_prod " +
      "ONLY — the schema rejects prod). Takes the job's id (from start_job / list_jobs / " +
      "get_job); the id is resolved inside the pre-prod folder first, so a prod job's id " +
      "resolves to nothing here. Fetch-first: refuses if the job is already in a terminal state. " +
      "Use to abort a hung or misbehaving pre-prod run. Returns {orchestrator, env, jobId, " +
      "strategy, stopped}.",
    inputSchema: {
      env: writeEnvArg,
      orchestrator: orchestratorArg,
      jobId: z.string().min(8).describe("Job id (from start_job / list_jobs / get_job)"),
      strategy: z
        .enum(["SoftStop", "Kill"])
        .optional()
        .default("SoftStop")
        .describe("SoftStop = graceful (default), Kill = immediate"),
    },
  },
  async ({ env, orchestrator, jobId, strategy }) => {
    try {
      const result = await backendFor(orchestrator).stopJob({ env, jobId, strategy });
      mcpLog(server, "warning", `stopped pre-prod job ${jobId} (${strategy})`, { orchestrator });
      return ok({ orchestrator, ...result }); // result carries env
    } catch (e) {
      return toolError("stop_job", e, VERSION);
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
      "(default inProgress/incomplete/pending). Optionally (`crossCheck`) correlate each to its queue " +
      "item(s) on the orchestrator(s) — omit `orchestrator` to check every orchestrator, since where " +
      "the item actually sits is the truth — for a coarse verdict: no-job / job-never-started / " +
      "job-faulted / job-running / queued-not-picked-up / job-successful-order-stuck / job-found " +
      "(or 'error: …' when the lookup failed, which is NOT 'no-job'). Verdicts come from the newest " +
      "queue item's status and latest attempt (PHI-safe — member data / tokens are never surfaced). " +
      "job-never-started = the latest attempt was reaped before any worker ran it (e.g. a missing " +
      "credential on Apex): a platform/config problem, not order data. READ-ONLY. Returns {env, " +
      "scanned, statuses, found, stuck:[{orderUid,status,ageHours,orchestrator?:{backend," +
      "verdict,queueItemCount,queueItemStatus?,exceptionType?}}]} where `backend` is the " +
      "orchestrator holding the newest queue item (null when none / lookup failed).",
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
      crossCheck: z
        .boolean()
        .optional()
        .default(false)
        .describe("Correlate each stuck order to its queue item(s)/job(s) for a verdict"),
      orchestrator: orchestratorArg
        .optional()
        .describe(
          "With crossCheck: check only this orchestrator (uipath | apex); omit to check every orchestrator",
        ),
      since: z
        .string()
        .optional()
        .describe(
          "Explicit ISO floor for the queue-item search. Each order's own creationDate " +
            "already tightens its search to that date (queue items can't predate the order), so " +
            "this is only needed to push the floor even later, or when crossCheck is used " +
            "without a BE-sourced creationDate",
        ),
      top: z
        .number()
        .int()
        .min(1)
        .max(500)
        .optional()
        .default(50)
        .describe("Max queue items to consider per order, per orchestrator, in the cross-check"),
    },
  },
  async (
    { env, profile, scanPages, statuses, olderThanHours, crossCheck, orchestrator, since, top },
    extra,
  ) => {
    try {
      return ok(
        await findStuckOrders({
          env,
          profile: profile ?? null,
          scanPages,
          statuses,
          olderThanHours,
          crossCheck,
          orchestrator,
          since,
          top,
          onProgress: stepProgress(extra),
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
  async ({ profile, tags, sections, emr, includeUnchanged }, extra) => {
    try {
      return ok(
        await diffSettings({
          profile: profile ?? null,
          ...(tags ? { tags } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
          includeUnchanged,
          onProgress: stepProgress(extra),
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
  async ({ env, profile, tags, sections, emr, normalized }, extra) => {
    try {
      return ok(
        await getSettings({
          env,
          profile: profile ?? null,
          ...(tags ? { tags } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
          normalized,
          onProgress: stepProgress(extra),
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
  async ({ profile, tags, sections, emr, includeBodies }, extra) => {
    try {
      return ok(
        await planSettingsSyncOp({
          profile: profile ?? null,
          ...(tags ? { tags } : {}),
          ...(sections ? { sections } : {}),
          ...(emr ? { emr } : {}),
          includeBodies,
          onProgress: stepProgress(extra),
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
  async ({ profile, tags, sections, emr, actionIds, all }, extra) => {
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
          onProgress: stepProgress(extra),
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
      const { client } = await connect(env, profile);
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
      "speciality, referredTo{name,npi,external}, auth/upload status — no patient data). insurance/" +
      "orderType are omitted from a row when the request already filtered to exactly one value for " +
      "that dimension (it would just repeat the filter back). Defaults to type='Outbound Referral', " +
      "pageSize=100, pageNumber=1 (1-based). Use get_order for a single order's full detail including " +
      "patient info.",
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
      "Return the Copilot BE session JWT for a profile + env. In support-account envs " +
      "that is the post-switch-clinic, clinic-scoped token with the activeClinicId it is " +
      "pinned to; otherwise it is the direct login token. A cached session is reused rather " +
      "than re-logging in. READ-ONLY — authenticates only, never reads or writes order data. " +
      "Returns {env, profile, mode, clinicUid, activeClinicId, token}.",
    inputSchema: {
      env: z.enum(["prod", "pre_prod"]).describe("Which env to log into (required)"),
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config (required)"),
    },
  },
  async ({ env, profile }, extra) => {
    try {
      const s = await withHeartbeat(extra, `${env} connect`, connect(env, profile));
      return ok({
        env,
        profile,
        mode: s.mode,
        clinicUid: s.clinicUid,
        activeClinicId: s.activeClinicId,
        token: s.token,
      });
    } catch (e) {
      return toolError("get_login_token", e, VERSION);
    }
  },
);

// ---- list_clinics --------------------------------------------------------
// The documented carve-out from the "every Copilot operation requires env + profile"
// invariant: this is the discovery tool that PRODUCES the profile -> clinicUid mapping,
// so requiring a profile would be circular.
server.registerTool(
  "list_clinics",
  {
    title: "List the clinics the support account can switch into",
    annotations: {
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: true,
    },
    description:
      "List the clinics the support account can switch into, with the config profile each one " +
      "is already mapped to (profile:null = unmapped). Use it to fill " +
      "copilot.profiles.<name>.<env>.clinicUid. Requires copilot.support.<env> (support-account " +
      "auth — today prod only); takes no profile, because it is what produces the profile -> " +
      "clinicUid mapping. READ-ONLY. Returns {env, clinics}.",
    inputSchema: {
      env: z.enum(["prod", "pre_prod"]).describe("Which env to list clinics for (required)"),
    },
  },
  async ({ env }, extra) => {
    try {
      const clinics = await withHeartbeat(extra, `${env} list clinics`, listClinics(env));
      const profiles = loadConfig().copilot.profiles ?? {};
      const profileByUid: Record<string, string> = {};
      for (const [name, entry] of Object.entries(profiles)) {
        const uid = entry[env]?.clinicUid;
        if (uid) profileByUid[uid] = name;
      }
      return ok({
        env,
        clinics: clinics
          .map((c) => ({ ...c, profile: profileByUid[c.clinicUid] ?? null }))
          .sort((a, b) => a.name.localeCompare(b.name)),
      });
    } catch (e) {
      return toolError("list_clinics", e, VERSION);
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
      "for PROD and PRE-PROD, an auth check (Copilot BE login, reusing a cached session), a " +
      "clinic-scope fingerprint (GET /orders/locations, the cheap proof a support-account " +
      "session landed on the intended clinic and not just that login succeeded), and one cheap " +
      "authenticated UiPath Orchestrator call per env/folder, and — when Apex is configured — " +
      "which config source supplied the Apex account, that Apex login works, and that each env's " +
      "folder resolves to an id (an unconfigured Apex is reported as optional, not a failure). " +
      "READ-ONLY. Use it to debug setup (creds, clinic scope, UiPath token, Apex login, folder " +
      "access). Returns {account, ok, " +
      "checks:[{name, target, ok, detail}]}.",
    inputSchema: {
      profile: z
        .string()
        .min(1)
        .describe("Credential profile / account name from config to check (required)"),
    },
  },
  async ({ profile }, extra) => {
    try {
      return ok(await runDoctor({ profile: profile ?? null, onProgress: stepProgress(extra) }));
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
