// MCP prompts: user-invoked workflow templates that chain the existing tools.
// Each returns a single user message that walks the operator (or the model) through
// the tool sequence for a common Copilot ops task. Prompts add no new capability —
// they encode the playbooks that today live only in the skills/docs.

import { completable } from "@modelcontextprotocol/sdk/server/completable.js";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import { listProfiles } from "../config/config.js";

// Profiles are read live from the config so autocompletion reflects whatever the
// running server is configured with, not a hardcoded list.
const ENVS = ["prod", "pre_prod"];

const userText = (text: string) => ({
  messages: [{ role: "user" as const, content: { type: "text" as const, text } }],
});

export function registerPrompts(server: McpServer): void {
  // ---- diagnose-order ----------------------------------------------------
  server.registerPrompt(
    "diagnose-order",
    {
      title: "Diagnose a Copilot order",
      description:
        "Root-cause a failing or stuck order: fetch its detail, trace its UiPath job, and read the logs.",
      argsSchema: {
        orderUid: z.string().min(8).describe("The orderUid to diagnose"),
        env: completable(
          z.string().optional().describe("Env the order/job lives in (prod | pre_prod)"),
          () => ENVS,
        ),
      },
    },
    ({ orderUid, env }) => {
      const envArg = env ? `, env="${env}"` : "";
      const envReminder = env
        ? ""
        : " Both get_order and analyze_order_execution require an env (prod or pre_prod) — confirm with the user which env the order is in before calling them.";
      return userText(
        `Diagnose Copilot order ${orderUid}${env ? ` (env: ${env})` : ""}. Work through it step by step:\n` +
          `1. Call get_order with orderUid="${orderUid}"${envArg} to read its current normalized state.\n` +
          `2. Call analyze_order_execution with orderUid="${orderUid}"${envArg} (includeLogs=true) to find the matching UiPath job and its verdict.\n` +
          `3. If a job is found and you need more detail, call get_job_logs for that job.\n` +
          `Then summarize: what state is the order in, did the automation run, and what failed or is pending.${envReminder}`,
      );
    },
  );

  // ---- reconcile-settings ------------------------------------------------
  server.registerPrompt(
    "reconcile-settings",
    {
      title: "Reconcile account settings (prod -> pre-prod)",
      description:
        "Compare an account's settings between prod and pre-prod, then additively add the items prod has but pre-prod is missing.",
      argsSchema: {
        profile: completable(
          z.string().min(1).describe("Credential profile / account name from config (required)"),
          () => listProfiles(),
        ),
      },
    },
    ({ profile }) => {
      return userText(
        `Reconcile account settings between prod and pre-prod for profile="${profile}".\n` +
          `1. Call list_setting_sections to see which sections/groups can be compared.\n` +
          `2. Call diff_settings (profile="${profile}") for the relevant groups to find drift, focusing on each section's onlyInProd items (present in prod, missing in pre-prod).\n` +
          `3. Call plan_settings_sync (profile="${profile}", same groups/sections scope) to compute the additive actions. It is READ-ONLY and returns each action with a stable id, a one-line summary, and warnings about references dropped for having no pre-prod match by name.\n` +
          `4. Present the planned actions (id, op, type, item, warnings) to the user and ask which to apply. Call out warnings — dropped payer/facility/sub-category links mean the created item will be missing those references.\n` +
          `5. Only after the user explicitly approves, call apply_settings_sync (profile="${profile}", same groups/sections scope) with actionIds set to exactly the approved ids — or all=true only if the user explicitly approved everything. It re-plans server-side, writes ADDITIVELY to PRE-PROD only, and never overwrites or deletes. Report executed vs notSelected vs unmatchedIds (an unmatched id means state changed since planning — re-plan and re-review).\n` +
          `Tip: if applying both specialties and orders, apply the specialties/referred-* actions first, then RE-PLAN the orders section — order creates resolve facility references by name against current pre-prod state, so newly created facilities only resolve on a fresh plan.\n` +
          `Never apply without the user reviewing the plan first.`,
      );
    },
  );

  // ---- inspect-settings ----------------------------------------------------
  server.registerPrompt(
    "inspect-settings",
    {
      title: "Inspect one env's account settings",
      description:
        "Fetch and summarize an account's settings from a single env (prod or pre_prod), optionally scoped to a group.",
      argsSchema: {
        profile: completable(
          z.string().min(1).describe("Credential profile / account name from config (required)"),
          () => listProfiles(),
        ),
        env: completable(
          z.string().optional().describe("Env to inspect (prod | pre_prod)"),
          () => ENVS,
        ),
        group: z
          .string()
          .optional()
          .describe("Top-level settings group to focus on (e.g. 'orders')"),
      },
    },
    ({ profile, env, group }) => {
      const envArg = env ? `, env="${env}"` : "";
      const groupArg = group ? `, groups=["${group}"]` : "";
      const envReminder = env
        ? ""
        : " get_settings requires an env (prod or pre_prod) — confirm with the user which env to inspect before calling it.";
      return userText(
        `Inspect Copilot settings for profile="${profile}"${env ? ` in ${env}` : ""}${group ? ` (group: ${group})` : ""}. Everything here is READ-ONLY.\n` +
          `1. Call list_setting_sections${group ? ` with group="${group}"` : ""} to see the available sections; note which are 'derived' (crawled from order types — heavier to fetch).\n` +
          `2. Call get_settings (profile="${profile}"${envArg}${groupArg}) to fetch the sections from that one env.\n` +
          `3. Summarize per section: row counts for list sections, the key fields for object sections, and surface any per-section errors.${envReminder}`,
      );
    },
  );

  // ---- clone-and-verify-order --------------------------------------------
  server.registerPrompt(
    "clone-and-verify-order",
    {
      title: "Clone a prod order to pre-prod and verify",
      description:
        "Pick a cloneable prod order, clone it into pre-prod (clone-only), and confirm the new order.",
      argsSchema: {
        uid: z.string().optional().describe("A specific prod orderUid to clone; omit to pick one"),
        profile: completable(
          z.string().min(1).describe("Credential profile / account name from config (required)"),
          () => listProfiles(),
        ),
      },
    },
    ({ uid, profile }) => {
      const p = ` (profile="${profile}")`;
      const step1 = uid
        ? `1. You already have orderUid="${uid}". Optionally confirm it is cloneable with find_clone_candidates.`
        : `1. Call find_clone_candidates${p} to list recent prod orders that will actually clone, and pick one.`;
      return userText(
        `Clone a prod Copilot order into pre-prod and verify it${p}.\n` +
          `${step1}\n` +
          `2. Call clone_order with the chosen uid(s)${p} — clone-only (submit=false) unless the user explicitly authorizes submitting in pre-prod.\n` +
          `3. From the result, take each newUid and confirm it landed (it should reach forReview / ready-to-submit).\n` +
          `Report the prodUid -> newUid mapping and the verify status for each.`,
      );
    },
  );

  // ---- triage-stuck-orders -----------------------------------------------
  server.registerPrompt(
    "triage-stuck-orders",
    {
      title: "Triage stuck orders",
      description:
        "Find orders sitting in a non-terminal status and analyze each to decide remediation.",
      argsSchema: {
        profile: completable(
          z.string().min(1).describe("Credential profile / account name from config (required)"),
          () => listProfiles(),
        ),
      },
    },
    ({ profile }) => {
      const p = ` (profile="${profile}")`;
      return userText(
        `Triage stuck Copilot orders${p}.\n` +
          `1. Call find_stuck_orders${p} to list orders stuck in a non-terminal status.\n` +
          `2. For each stuck order, call analyze_order_execution to see whether its UiPath job failed, is still running, or never started.\n` +
          `3. Group them by root cause and recommend a remediation per group (retry, re-clone, fix data, escalate).\n` +
          `Do not take any write action without the user's explicit go-ahead.`,
      );
    },
  );

  // ---- send-mcp-feedback ---------------------------------------------------
  server.registerPrompt(
    "send-mcp-feedback",
    {
      title: "File feedback or a bug on this MCP server",
      description:
        "Turn the user's report about copilot-mcp itself — a bug or general feedback (idea, " +
        "friction, docs gap) — into a GitHub issue on the server's feedback repo.",
      argsSchema: {
        summary: z.string().min(1).describe("One-line summary of the feedback or bug"),
        kind: completable(
          z.string().optional().describe("bug | feedback (default: feedback)"),
          () => ["bug", "feedback"],
        ),
      },
    },
    ({ summary, kind }) => {
      const k = kind === "bug" ? "bug" : "feedback";
      return userText(
        `File ${k === "bug" ? "a bug report" : "feedback"} about the copilot-mcp server itself: "${summary}".\n` +
          `1. Gather the substance from the user/conversation: what happened or what they want changed, expected vs actual, and the related tool name if any. Never include credentials or PHI.\n` +
          `2. Call build_mcp_issue with kind="${k}", a short title, and those details. It returns {repo, title, body, labels, url} and posts NOTHING.\n` +
          `3. If a GitHub MCP server (or gh) with access to that repo is connected, search for an existing open issue on the same topic first — comment there instead of duplicating — else create the issue from the payload's title/body/labels and report its number/URL.\n` +
          `4. If no GitHub tooling is available, give the user the prefilled \`url\` to open (filing requires their GitHub account to have access to the repo).\n` +
          `Only write to GitHub — take no Copilot/UiPath action.`,
      );
    },
  );

  // ---- report-faulted-uipath-jobs ----------------------------------------
  server.registerPrompt(
    "report-faulted-uipath-jobs",
    {
      title: "Report faulted UiPath jobs as GitHub issues",
      description:
        "Find faulted UiPath jobs (prod by default) and file each as a GitHub issue on the RPA repo, " +
        "commenting on an existing issue when the same fault is already filed. Requires a connected GitHub MCP server.",
      argsSchema: {
        env: completable(
          z.string().optional().describe("Env to scan (prod | pre_prod); defaults to prod"),
          () => ENVS,
        ),
        since: z
          .string()
          .optional()
          .describe("ISO lower bound on job CreationTime (e.g. last 24h)"),
        top: z.string().optional().describe("Max recent jobs to scan (default 50)"),
        repo: z
          .string()
          .optional()
          .describe("Target repo owner/name (default Apex-Medical-AI-Inc/RPAPlaywright)"),
      },
    },
    ({ env, since, top, repo }) => {
      const e = env || "prod";
      const repoRef = repo || "Apex-Medical-AI-Inc/RPAPlaywright";
      const listArgs = [`env="${e}"`, since ? `since="${since}"` : "", top ? `top=${top}` : ""]
        .filter(Boolean)
        .join(", ");
      return userText(
        `File GitHub issues for faulted UiPath jobs in ${e} on ${repoRef}.\n` +
          `This requires a connected GitHub MCP server with write access to ${repoRef}; if none is available, stop and say so.\n` +
          `1. Call list_jobs with ${listArgs}, then keep only jobs whose state is "Faulted" or "Stopped". If none, report that and stop.\n` +
          `2. For each faulted job, call build_faulted_job_issue with env="${e}" and that job's key${repo ? `, repo="${repo}"` : ""} to get the issue payload (it does NOT post to GitHub).\n` +
          `3. Using the GitHub MCP server, run the payload's searchQuery to find an existing OPEN issue for this fault (matched by faultSignature):\n` +
          `   - If one exists, add the payload's recurrenceComment to that issue's discussion (do NOT open a duplicate).\n` +
          `   - Otherwise create a new issue with the payload's title, body, and labels.\n` +
          `4. Report a per-job summary: faulted job key -> created issue (#/url) or commented on existing issue (#/url).\n` +
          `Only write to GitHub — never take any UiPath write action.`,
      );
    },
  );
}
