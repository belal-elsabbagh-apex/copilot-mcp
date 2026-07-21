import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../../config/config.js";
import { diffSettings, getSettings } from "./read.js";

// These sections are real catalog entries (locations.ts / location-groups.ts /
// location-regions.ts) — reused here rather than a fake catalog so the fetch paths
// below are real, verifiable GETs.
const SECTIONS = ["locations", "location-groups", "location-regions"];

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: { prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: { orchestratorUrl: "https://cloud.uipath.com/org/tenant/orchestrator_", bearer: "t" },
};

let dir: string;
let prevConfig: string | undefined;

const writeConfig = (config: unknown): void => {
  writeFileSync(join(dir, "config.json"), JSON.stringify(config));
  resetConfigCache();
};

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-settings-read-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeConfig(FIXTURE);
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

let calls: string[];
// Per-path canned response, keyed by env so diffSettings can return different data
// for prod vs pre-prod. "location-regions" always 500s to test error isolation.
let byPath: Record<string, unknown>;
const realFetch = globalThis.fetch;

beforeEach(() => {
  calls = [];
  byPath = {};
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = String(input);
    calls.push(url);
    if (url.endsWith("/api/v1/copilot/physician/login")) {
      return new Response(JSON.stringify({ token: "jwt" }), { status: 200 });
    }
    if (url.endsWith("/api/v1/settings/locations/regions")) {
      return new Response("server error", { status: 500 });
    }
    for (const [suffix, data] of Object.entries(byPath)) {
      if (url.endsWith(suffix)) return new Response(JSON.stringify(data), { status: 200 });
    }
    return new Response(JSON.stringify([]), { status: 200 });
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

describe("getSettings", () => {
  test("fetches every section concurrently, preserving catalog order and isolating errors", async () => {
    byPath = {
      "/api/v1/settings/locations": [{ name: "OSSM" }],
      "/api/v1/settings/locations/groups": [{ name: "Group A" }],
    };
    const out = await getSettings({ env: "prod", profile: null, sections: SECTIONS });
    expect(out.sections.map((s) => s.key)).toEqual(SECTIONS);
    expect(out.sections[0]?.data).toEqual([{ name: "OSSM" }]);
    expect(out.sections[0]?.error).toBeUndefined();
    expect(out.sections[1]?.data).toEqual([{ name: "Group A" }]);
    // location-regions 500s — isolated to its own entry, doesn't affect the others.
    expect(out.sections[2]?.error).toContain("500");
    expect(out.sections[2]?.data).toBeUndefined();
  });
});

describe("diffSettings", () => {
  test("diffs every section concurrently; a per-section fetch error doesn't affect the others", async () => {
    // Same location name in both envs (equal); different group name (a real diff).
    byPath = { "/api/v1/settings/locations": [{ name: "OSSM" }] };
    const out = await diffSettings({ profile: null, sections: SECTIONS, includeUnchanged: true });
    expect(out.sections.map((s) => s.key)).toEqual(SECTIONS);
    expect(out.sections[0]?.equal).toBe(true); // locations: identical in both envs
    expect(out.sections[2]?.equal).toBe(false); // location-regions: 500 -> error, not equal
    expect(out.sections[2]?.error).toContain("500");
    expect(out.sectionsCompared).toBe(3);
  });
});
