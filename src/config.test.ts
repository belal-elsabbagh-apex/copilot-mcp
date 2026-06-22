import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { getUipath, resetConfigCache, resolveCreds } from "./config.js";

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: {
    prod: envCreds("prod"),
    pre_prod: envCreds("preprod"),
    profiles: { ossm: { prod: envCreds("ossm-prod"), pre_prod: envCreds("ossm-preprod") } },
  },
  uipath: {
    orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
    bearer: "test-bearer",
  },
};

let dir: string;
let prevConfig: string | undefined;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-cfg-"));
  const path = join(dir, "config.json");
  writeFileSync(path, JSON.stringify(FIXTURE));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = path;
  resetConfigCache();
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

describe("resolveCreds", () => {
  test("returns the top-level prod/pre_prod pair by default", () => {
    const creds = resolveCreds();
    expect(creds.prod.email).toBe("prod@example.com");
    expect(creds.pre_prod.email).toBe("preprod@example.com");
  });

  test("returns a named profile when given", () => {
    const creds = resolveCreds("ossm");
    expect(creds.prod.email).toBe("ossm-prod@example.com");
    expect(creds.pre_prod.email).toBe("ossm-preprod@example.com");
  });

  test("throws a helpful error for an unknown profile", () => {
    expect(() => resolveCreds("nope")).toThrow(/unknown profile 'nope'/);
  });
});

describe("getUipath", () => {
  test("returns the validated uipath block", () => {
    expect(getUipath().bearer).toBe("test-bearer");
    expect(getUipath().orchestratorUrl).toContain("cloud.uipath.com");
  });
});
