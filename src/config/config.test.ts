import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  getUipath,
  onConfigReload,
  resetConfigCache,
  resolveAuth,
  resolveSupport,
} from "./config.js";

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: {
    sessionCache: false,
    prod: envCreds("prod"),
    pre_prod: envCreds("preprod"),
    support: { prod: envCreds("support-prod") },
    profiles: {
      ossm: { prod: envCreds("ossm-prod"), pre_prod: envCreds("ossm-preprod") },
      // prod migrated to support-account auth; pre_prod still a direct login.
      kafri: {
        prod: { clinicUid: "b58a95cf-e53d-4eae-bf86-9a57ad624022" },
        pre_prod: envCreds("kafri-preprod"),
      },
      // carries BOTH — clinicUid must win (mid-migration shape).
      both: {
        prod: { ...envCreds("both-prod"), clinicUid: "c741d791-3e3b-4102-b95c-f94549949c9a" },
      },
      // neither clinicUid nor full creds in pre_prod; no pre_prod entry at all.
      prodonly: { prod: envCreds("prodonly") },
    },
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

describe("resolveAuth", () => {
  test("falls back to the top-level pair when no profile is given", () => {
    expect(resolveAuth(null, "prod")).toEqual({
      mode: "direct",
      be: "https://be.prod.example.com",
      email: "prod@example.com",
      password: "pw",
    });
    expect(resolveAuth(null, "pre_prod").email).toBe("preprod@example.com");
  });

  test("a fully direct profile stays on direct login in both envs", () => {
    expect(resolveAuth("ossm", "prod")).toEqual({
      mode: "direct",
      be: "https://be.ossm-prod.example.com",
      email: "ossm-prod@example.com",
      password: "pw",
    });
    expect(resolveAuth("ossm", "pre_prod").mode).toBe("direct");
  });

  test("a clinicUid env entry uses the support account's credentials", () => {
    expect(resolveAuth("kafri", "prod")).toEqual({
      mode: "support",
      be: "https://be.support-prod.example.com",
      email: "support-prod@example.com",
      password: "pw",
      clinicUid: "b58a95cf-e53d-4eae-bf86-9a57ad624022",
    });
  });

  test("the same profile's non-migrated env still logs in directly", () => {
    expect(resolveAuth("kafri", "pre_prod")).toEqual({
      mode: "direct",
      be: "https://be.kafri-preprod.example.com",
      email: "kafri-preprod@example.com",
      password: "pw",
    });
  });

  test("clinicUid wins over credentials present on the same entry", () => {
    const auth = resolveAuth("both", "prod");
    expect(auth.mode).toBe("support");
    // be may be overridden per profile; the account is the support account's.
    expect(auth.email).toBe("support-prod@example.com");
    expect(auth.be).toBe("https://be.both-prod.example.com");
  });

  test("throws when clinicUid is used but the env has no support account", () => {
    expect(() => resolveAuth("kafri", "prod")).not.toThrow();
    expect(() => resolveSupport("pre_prod")).toThrow(/copilot\.support\.pre_prod is required/);
  });

  test("throws a helpful error for an unknown profile", () => {
    expect(() => resolveAuth("nope", "prod")).toThrow(/unknown profile 'nope'/);
  });

  test("throws when the profile has no entry for the requested env", () => {
    expect(() => resolveAuth("prodonly", "pre_prod")).toThrow(/has no pre_prod auth/);
  });

  test("rejects an env entry carrying neither clinicUid nor full credentials", () => {
    const bad = {
      ...FIXTURE,
      copilot: {
        ...FIXTURE.copilot,
        profiles: {
          ...FIXTURE.copilot.profiles,
          broken: { prod: { be: "https://x.example.com" } },
        },
      },
    };
    const path = join(dir, "bad.json");
    writeFileSync(path, JSON.stringify(bad));
    const prev = process.env["COPILOT_MCP_CONFIG"];
    process.env["COPILOT_MCP_CONFIG"] = path;
    resetConfigCache();
    try {
      expect(() => resolveAuth("broken", "prod")).toThrow(/needs either clinicUid/);
    } finally {
      if (prev === undefined) delete process.env["COPILOT_MCP_CONFIG"];
      else process.env["COPILOT_MCP_CONFIG"] = prev;
      resetConfigCache();
    }
  });
});

describe("resolveSupport", () => {
  test("returns the configured support account for the env", () => {
    expect(resolveSupport("prod").email).toBe("support-prod@example.com");
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
    expect(resolveAuth(null, "prod").email).toBe("prod@example.com");
    writeFileSync(
      liveConfigPath,
      JSON.stringify({ ...FIXTURE, copilot: { ...FIXTURE.copilot, prod: envCreds("prod-v2") } }),
    );
    touch(liveConfigPath);
    expect(resolveAuth(null, "prod").email).toBe("prod-v2@example.com");
  });

  test("notifies onConfigReload listeners on a reload, but not on the first load", () => {
    const events: Array<{ source: string }> = [];
    const unsubscribe = onConfigReload((info) => events.push(info));
    try {
      resolveAuth(null, "prod");
      expect(events).toHaveLength(0);
      writeFileSync(liveConfigPath, JSON.stringify(FIXTURE));
      touch(liveConfigPath);
      resolveAuth(null, "prod");
      expect(events).toHaveLength(1);
      expect(events[0]?.source).toBe(liveConfigPath);
    } finally {
      unsubscribe();
    }
  });

  test("fails closed on an invalid edit, then recovers once fixed", () => {
    expect(resolveAuth(null, "prod").email).toBe("prod@example.com");
    writeFileSync(liveConfigPath, "{ not valid json");
    touch(liveConfigPath);
    expect(() => resolveAuth(null, "prod")).toThrow(/not valid JSON/);
    writeFileSync(liveConfigPath, JSON.stringify(FIXTURE));
    touch(liveConfigPath);
    expect(resolveAuth(null, "prod").email).toBe("prod@example.com");
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
    expect(resolveAuth(null, "prod").email).toBe("prod@example.com");
  });

  test("falls back to the legacy config.local.json name when the new one is absent", () => {
    writeFileSync(join(tmp, "config.local.json"), JSON.stringify(FIXTURE));
    expect(resolveAuth(null, "prod").email).toBe("prod@example.com");
  });
});
