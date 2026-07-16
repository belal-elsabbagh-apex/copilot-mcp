import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { getUipath, onConfigReload, resetConfigCache, resolveCreds } from "./config.js";

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

describe("uipath auth schema", () => {
  let authDir: string;
  let authConfigPath: string;
  let prevEnv: string | undefined;

  beforeEach(() => {
    authDir = mkdtempSync(join(tmpdir(), "copilot-mcp-uipath-auth-"));
    authConfigPath = join(authDir, "config.json");
    prevEnv = process.env["COPILOT_MCP_CONFIG"];
    process.env["COPILOT_MCP_CONFIG"] = authConfigPath;
  });

  afterEach(() => {
    if (prevEnv === undefined) delete process.env["COPILOT_MCP_CONFIG"];
    else process.env["COPILOT_MCP_CONFIG"] = prevEnv;
    resetConfigCache();
    rmSync(authDir, { recursive: true, force: true });
  });

  const withUipath = (uipath: Record<string, unknown>) => {
    writeFileSync(authConfigPath, JSON.stringify({ ...FIXTURE, uipath }));
    resetConfigCache();
  };

  test("accepts bearer only", () => {
    withUipath({ orchestratorUrl: FIXTURE.uipath.orchestratorUrl, bearer: "b" });
    expect(getUipath().bearer).toBe("b");
  });

  test("accepts oauth only, no bearer", () => {
    withUipath({
      orchestratorUrl: FIXTURE.uipath.orchestratorUrl,
      oauth: { clientId: "id", clientSecret: "secret" },
    });
    expect(getUipath().oauth?.clientId).toBe("id");
    expect(getUipath().bearer).toBeUndefined();
  });

  test("accepts both bearer and oauth", () => {
    withUipath({
      orchestratorUrl: FIXTURE.uipath.orchestratorUrl,
      bearer: "b",
      oauth: { clientId: "id", clientSecret: "secret" },
    });
    expect(getUipath().bearer).toBe("b");
    expect(getUipath().oauth?.clientId).toBe("id");
  });

  test("rejects neither bearer nor oauth", () => {
    withUipath({ orchestratorUrl: FIXTURE.uipath.orchestratorUrl });
    expect(() => getUipath()).toThrow(/bearer, oauth, or both/);
  });
});

describe("live reload", () => {
  let liveDir: string;
  let liveConfigPath: string;
  let prevEnv: string | undefined;

  beforeEach(() => {
    liveDir = mkdtempSync(join(tmpdir(), "copilot-mcp-live-"));
    liveConfigPath = join(liveDir, "config.json");
    writeFileSync(liveConfigPath, JSON.stringify(FIXTURE));
    prevEnv = process.env["COPILOT_MCP_CONFIG"];
    process.env["COPILOT_MCP_CONFIG"] = liveConfigPath;
    resetConfigCache();
  });

  afterEach(() => {
    if (prevEnv === undefined) delete process.env["COPILOT_MCP_CONFIG"];
    else process.env["COPILOT_MCP_CONFIG"] = prevEnv;
    resetConfigCache();
    rmSync(liveDir, { recursive: true, force: true });
  });

  // Force a detectable mtime change regardless of the filesystem's timestamp
  // resolution, rather than relying on real wall-clock elapsing between writes.
  const touch = (path: string) => {
    const future = new Date(Date.now() + 2000);
    utimesSync(path, future, future);
  };

  test("picks up an edited file on the next call, without resetConfigCache()", () => {
    expect(resolveCreds().prod.email).toBe("prod@example.com");
    writeFileSync(
      liveConfigPath,
      JSON.stringify({ ...FIXTURE, copilot: { ...FIXTURE.copilot, prod: envCreds("prod-v2") } }),
    );
    touch(liveConfigPath);
    expect(resolveCreds().prod.email).toBe("prod-v2@example.com");
  });

  test("notifies onConfigReload listeners on a reload, but not on the first load", () => {
    const events: Array<{ source: string }> = [];
    const unsubscribe = onConfigReload((info) => events.push(info));
    try {
      resolveCreds();
      expect(events).toHaveLength(0);
      writeFileSync(liveConfigPath, JSON.stringify(FIXTURE));
      touch(liveConfigPath);
      resolveCreds();
      expect(events).toHaveLength(1);
      expect(events[0]?.source).toBe(liveConfigPath);
    } finally {
      unsubscribe();
    }
  });

  test("fails closed on an invalid edit, then recovers once fixed", () => {
    expect(resolveCreds().prod.email).toBe("prod@example.com");
    writeFileSync(liveConfigPath, "{ not valid json");
    touch(liveConfigPath);
    expect(() => resolveCreds()).toThrow(/not valid JSON/);
    writeFileSync(liveConfigPath, JSON.stringify(FIXTURE));
    touch(liveConfigPath);
    expect(resolveCreds().prod.email).toBe("prod@example.com");
  });
});

describe("default filename", () => {
  let tmp: string;
  let prevEnv: string | undefined;
  let prevCwd: string;

  beforeEach(() => {
    prevEnv = process.env["COPILOT_MCP_CONFIG"];
    delete process.env["COPILOT_MCP_CONFIG"];
    prevCwd = process.cwd();
    tmp = mkdtempSync(join(tmpdir(), "copilot-mcp-default-"));
    process.chdir(tmp);
    resetConfigCache();
  });

  afterEach(() => {
    process.chdir(prevCwd);
    if (prevEnv === undefined) delete process.env["COPILOT_MCP_CONFIG"];
    else process.env["COPILOT_MCP_CONFIG"] = prevEnv;
    resetConfigCache();
    rmSync(tmp, { recursive: true, force: true });
  });

  test("prefers copilot-mcp.config.json when present", () => {
    writeFileSync(join(tmp, "copilot-mcp.config.json"), JSON.stringify(FIXTURE));
    expect(resolveCreds().prod.email).toBe("prod@example.com");
  });

  test("falls back to the legacy config.local.json name when the new one is absent", () => {
    writeFileSync(join(tmp, "config.local.json"), JSON.stringify(FIXTURE));
    expect(resolveCreds().prod.email).toBe("prod@example.com");
  });
});
