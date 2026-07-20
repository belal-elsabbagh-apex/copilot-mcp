import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { NotImplementedError } from "../copilot/settings/index.js";
import {
  classify,
  ExpectedError,
  formatMcpIssue,
  issueUrl,
  REPO_URL,
  toolError,
} from "./feedback.js";

// Parse the JSON body that toolError() emits and pull out the reportIssue block (if any).
function decode(res: { content: { text: string }[] }): {
  error: string;
  reportIssue?: { message: string; url: string };
} {
  return JSON.parse(res.content[0]?.text ?? "{}");
}

describe("classify", () => {
  test.each([
    ["NotImplementedError (stub)", new NotImplementedError("sync_settings not implemented")],
    ["explicit ExpectedError", new ExpectedError("nope")],
    ["unknown profile", new Error("unknown profile 'x' (available: ossm)")],
    ["auth 401 (-> status in msg)", new Error("login failed 401: forbidden")],
    ["upstream 500", new Error("UiPath GET /odata/Jobs -> 500: boom")],
    ["not found", new Error("order abc not found in this env")],
    ["missing config", new Error("No config found. Set COPILOT_MCP_CONFIG ...")],
    ["invalid config", new Error("Config from x is invalid:\n  - copilot.prod: be is required")],
    ["bad env", new Error("env 'x' not in profile (expected 'prod' or 'pre_prod')")],
  ])("treats %s as expected", (_label, e) => {
    expect(classify(e)).toBe("expected");
  });

  test.each([
    ["TypeError", new TypeError("Cannot read properties of undefined (reading 'x')")],
    ["missing field in response", new Error("create draft returned no orderUid: {}")],
    ["bad date", new Error("unrecognized date 13/40/2026")],
    ["bare error", new Error("boom")],
    ["non-error throw", "raw string blew up"],
  ])("treats %s as unknown", (_label, e) => {
    expect(classify(e)).toBe("unknown");
  });
});

describe("issueUrl", () => {
  test("builds an encoded new-issue URL with tool, version, and bug label", () => {
    const url = issueUrl({ tool: "clone_order", message: "boom happened", version: "9.9.9" });
    expect(url.startsWith(`${REPO_URL}/issues/new?`)).toBe(true);
    expect(url).toContain("labels=bug");
    // title is encoded — decode the query to assert content
    const params = new URLSearchParams(url.split("?")[1]);
    expect(params.get("title")).toContain("[tool: clone_order]");
    expect(params.get("body")).toContain("clone_order");
    expect(params.get("body")).toContain("9.9.9");
    expect(params.get("body")).toContain("boom happened");
  });

  test("respects a repositoryUrl override and trims a trailing slash", () => {
    const url = issueUrl({
      tool: "t",
      message: "m",
      version: "1",
      repositoryUrl: "https://github.com/acme/repo/",
    });
    expect(url.startsWith("https://github.com/acme/repo/issues/new?")).toBe(true);
  });
});

describe("formatMcpIssue", () => {
  test("bug kind: bug label, [bug] title, tool + version in body, matching url", () => {
    const p = formatMcpIssue({
      kind: "bug",
      title: "clone_order loses ICD codes",
      details: "Cloned order abc had 3 ICDs in prod but 0 in pre-prod.",
      tool: "clone_order",
      version: "9.9.9",
    });
    expect(p.repo).toBe(REPO_URL);
    expect(p.labels).toEqual(["bug"]);
    expect(p.title).toBe("[bug] clone_order loses ICD codes");
    expect(p.body).toContain("`clone_order`");
    expect(p.body).toContain("9.9.9");
    expect(p.body).toContain("0 in pre-prod");
    const params = new URLSearchParams(p.url.split("?")[1]);
    expect(p.url.startsWith(`${REPO_URL}/issues/new?`)).toBe(true);
    expect(params.get("title")).toBe(p.title);
    expect(params.get("body")).toBe(p.body);
    expect(params.get("labels")).toBe("bug");
  });

  test("feedback kind: enhancement label, no tool line, repositoryUrl override", () => {
    const p = formatMcpIssue({
      kind: "feedback",
      title: "add a dry-run flag to apply_settings_sync",
      details: "A dry-run would let us preview exact request bodies before applying.",
      version: "1.0.0",
      repositoryUrl: "https://github.com/acme/repo/",
    });
    expect(p.repo).toBe("https://github.com/acme/repo");
    expect(p.labels).toEqual(["enhancement"]);
    expect(p.title).toBe("[feedback] add a dry-run flag to apply_settings_sync");
    expect(p.body).not.toContain("**Tool:**");
    expect(p.url.startsWith("https://github.com/acme/repo/issues/new?")).toBe(true);
  });
});

describe("toolError", () => {
  test("unknown failure carries a reportIssue with a prefilled URL", () => {
    const res = toolError("get_order", new TypeError("undefined is not a function"), "1.5.0");
    const body = decode(res);
    expect(res.isError).toBe(true);
    expect(body.error).toBe("undefined is not a function");
    expect(body.reportIssue?.url).toContain("/issues/new?");
    expect(body.reportIssue?.url).toContain("get_order");
  });

  test("expected failure has no reportIssue", () => {
    const res = toolError("clone_order", new Error("unknown profile 'x'"), "1.5.0");
    const body = decode(res);
    expect(body.error).toBe("unknown profile 'x'");
    expect(body.reportIssue).toBeUndefined();
  });
});

describe("toolError honors config", () => {
  let dir: string;
  let prev: string | undefined;

  const FIXTURE = {
    copilot: {
      prod: { be: "https://p.example.com", email: "p@example.com", password: "pw" },
      pre_prod: { be: "https://pp.example.com", email: "pp@example.com", password: "pw" },
    },
    uipath: {
      orchestratorUrl: "https://cloud.uipath.com/org/tenant/orchestrator_",
      bearer: "test-bearer",
    },
    feedback: { enabled: false, repositoryUrl: "https://github.com/acme/repo" },
  };

  beforeAll(() => {
    dir = mkdtempSync(join(tmpdir(), "copilot-mcp-fb-"));
    const path = join(dir, "config.json");
    writeFileSync(path, JSON.stringify(FIXTURE));
    prev = process.env["COPILOT_MCP_CONFIG"];
    process.env["COPILOT_MCP_CONFIG"] = path;
    resetConfigCache();
  });

  afterAll(() => {
    if (prev === undefined) delete process.env["COPILOT_MCP_CONFIG"];
    else process.env["COPILOT_MCP_CONFIG"] = prev;
    resetConfigCache();
    rmSync(dir, { recursive: true, force: true });
  });

  test("suppresses reportIssue when feedback.enabled is false", () => {
    const res = toolError("doctor", new TypeError("kaboom"), "1.5.0");
    expect(decode(res).reportIssue).toBeUndefined();
  });
});
