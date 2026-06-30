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
          `3. Review those missing items with the user. Only if they explicitly approve, call sync_settings — it ADDITIVELY adds the prod-only items to pre-prod and never overwrites or deletes existing pre-prod settings.\n` +
          `Never sync without confirming the diff first.`,
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
