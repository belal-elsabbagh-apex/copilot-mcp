import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import {
  connect,
  listClinics,
  resetSessionCache,
  sessionKey,
  tokenClinicId,
  tokenExpiry,
} from "./session.js";

// ---- pure helpers -----------------------------------------------------------

const jwt = (claims: Record<string, unknown>): string =>
  `h.${Buffer.from(JSON.stringify(claims)).toString("base64")}.s`;

const DAY = 86_400_000;
const CLINIC_UID = "b58a95cf-e53d-4eae-bf86-9a57ad624022";
const CLINIC_UID_2 = "c741d791-3e3b-4102-b95c-f94549949c9a";

describe("tokenExpiry / tokenClinicId", () => {
  test("read the claims off a well-formed token", () => {
    const t = jwt({ id: 335, timestamp: 1_770_000_000_000, activeClinicId: 25 });
    expect(tokenExpiry(t)).toBe(1_770_000_000_000);
    expect(tokenClinicId(t)).toBe(25);
  });

  test("return null for garbage, missing claims, and a clinic-less token", () => {
    expect(tokenExpiry("not-a-jwt")).toBeNull();
    expect(tokenClinicId("not-a-jwt")).toBeNull();
    expect(tokenExpiry(jwt({ id: 335 }))).toBeNull();
    // clinic-less login token: has an expiry, no activeClinicId
    const clinicless = jwt({ id: 335, timestamp: Date.now() + DAY });
    expect(tokenExpiry(clinicless)).not.toBeNull();
    expect(tokenClinicId(clinicless)).toBeNull();
    // non-numeric claim values are refused rather than coerced
    expect(tokenExpiry(jwt({ timestamp: "soon" }))).toBeNull();
    expect(tokenClinicId(jwt({ activeClinicId: "25" }))).toBeNull();
  });
});

describe("sessionKey", () => {
  test("is stable, hashed (carries no email), and scope-sensitive", () => {
    const a = sessionKey("prod", "https://be.example.com", "s@example.com", "support");
    expect(a).toBe(sessionKey("prod", "https://be.example.com", "s@example.com", "support"));
    expect(a).not.toContain("s@example.com");
    expect(a).not.toBe(
      sessionKey("prod", "https://be.example.com", "s@example.com", `clinic:${CLINIC_UID}`),
    );
    expect(a).not.toBe(
      sessionKey("pre_prod", "https://be.example.com", "s@example.com", "support"),
    );
  });
});

// ---- HTTP wiring (config fixture + fetch stub) -------------------------------

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: {
    support: { prod: envCreds("support") },
    profiles: {
      // prod migrated to support-account auth; pre_prod still a direct login.
      ossm: { prod: { clinicUid: CLINIC_UID }, pre_prod: envCreds("ossm-preprod") },
      // second support profile — must reuse the cached clinic-less session.
      kafri: { prod: { clinicUid: CLINIC_UID_2 } },
    },
  },
  uipath: {
    orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
    bearer: "test-bearer",
  },
};

let dir: string;
let prevConfig: string | undefined;
let prevCache: string | undefined;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-session-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  prevCache = process.env["COPILOT_MCP_CACHE"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  process.env["COPILOT_MCP_CACHE"] = join(dir, "sessions.json");
  writeFileSync(join(dir, "config.json"), JSON.stringify(FIXTURE));
  resetConfigCache();
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  if (prevCache === undefined) delete process.env["COPILOT_MCP_CACHE"];
  else process.env["COPILOT_MCP_CACHE"] = prevCache;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

let calls: { method: string; url: string; body: unknown }[];
const realFetch = globalThis.fetch;

// Responds per path so the two-step support flow works regardless of call order.
type Handler = (url: string) => Response;
let handler: Handler;

const json = (obj: unknown, status = 200, headers?: Record<string, string>): Response =>
  new Response(JSON.stringify(obj), { status, ...(headers ? { headers } : {}) });

const liveToken = (activeClinicId?: number) =>
  jwt({
    id: 335,
    timestamp: Date.now() + DAY,
    ...(activeClinicId === undefined ? {} : { activeClinicId }),
  });

const defaultHandler: Handler = (url) => {
  if (url.includes("physician/login")) return json({ token: liveToken() });
  if (url.includes("support/switch-clinic")) return json({ token: liveToken(25) });
  return json({});
};

beforeEach(() => {
  calls = [];
  handler = defaultHandler;
  rmSync(join(dir, "sessions.json"), { force: true });
  resetSessionCache();
  resetConfigCache();
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      method: init?.method ?? "GET",
      url,
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    });
    return handler(url);
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const paths = () => calls.map((c) => new URL(c.url).pathname);

describe("connect — support mode", () => {
  test("logs in then switches clinic, in that order", async () => {
    const s = await connect("prod", "ossm");
    expect(paths()).toEqual(["/api/v1/copilot/physician/login", "/api/v1/support/switch-clinic"]);
    expect(calls[0]?.body).toEqual({ email: "support@example.com", password: "pw" });
    expect(calls[1]?.body).toEqual({ clinicUid: CLINIC_UID });
    expect(s.mode).toBe("support");
    expect(s.clinicUid).toBe(CLINIC_UID);
    expect(s.activeClinicId).toBe(25);
    expect(s.client.env).toBe("prod");
    expect(s.client.base).toBe("https://be.support.example.com");
  });

  test("refuses a switch-clinic response whose token has no clinic, and caches nothing", async () => {
    handler = (url) =>
      url.includes("physician/login") ? json({ token: liveToken() }) : json({ token: liveToken() }); // clinic-less switch result
    await expect(connect("prod", "ossm")).rejects.toThrow(/clinic-less/);

    // nothing cached for the clinic: a retry re-runs the switch
    calls = [];
    handler = defaultHandler;
    const s = await connect("prod", "ossm");
    expect(paths()).toEqual(["/api/v1/support/switch-clinic"]); // login was cached, clinic was not
    expect(s.activeClinicId).toBe(25);
  });

  test("surfaces a rate-limited login with its RateLimit-Reset", async () => {
    handler = () => json({ error: "too many" }, 429, { "RateLimit-Reset": "412" });
    await expect(connect("prod", "ossm")).rejects.toThrow(
      /login failed 429: support login rate-limited \(10 logins per 15 minutes\) — retry after 412s/,
    );
  });
});

describe("connect — direct mode (non-migrated env)", () => {
  test("issues exactly one login, with the profile's own credentials", async () => {
    const s = await connect("pre_prod", "ossm");
    expect(paths()).toEqual(["/api/v1/copilot/physician/login"]);
    expect(calls[0]?.body).toEqual({ email: "ossm-preprod@example.com", password: "pw" });
    expect(s.mode).toBe("direct");
    expect(s.clinicUid).toBeNull();
    expect(s.client.base).toBe("https://be.ossm-preprod.example.com");
    expect(s.client.env).toBe("pre_prod");
  });
});

describe("session caching", () => {
  test("a repeated connect in the same process issues no requests", async () => {
    await connect("prod", "ossm");
    await connect("pre_prod", "ossm");
    calls = [];
    const prod = await connect("prod", "ossm");
    const pre = await connect("pre_prod", "ossm");
    expect(calls).toHaveLength(0);
    expect(prod.activeClinicId).toBe(25);
    expect(pre.mode).toBe("direct");
  });

  test("the disk cache carries both sessions across a fresh process", async () => {
    await connect("prod", "ossm");
    await connect("pre_prod", "ossm");
    resetSessionCache(); // simulates a server restart: memory gone, file kept
    calls = [];
    expect((await connect("prod", "ossm")).activeClinicId).toBe(25);
    expect((await connect("pre_prod", "ossm")).mode).toBe("direct");
    expect(calls).toHaveLength(0);
  });

  test("a second support profile reuses the clinic-less session — switch only, no login", async () => {
    await connect("prod", "ossm");
    resetSessionCache();
    calls = [];
    handler = (url) =>
      url.includes("physician/login")
        ? json({ token: liveToken() })
        : json({ token: liveToken(6) });
    const s = await connect("prod", "kafri");
    expect(paths()).toEqual(["/api/v1/support/switch-clinic"]);
    expect(calls[0]?.body).toEqual({ clinicUid: CLINIC_UID_2 });
    expect(s.activeClinicId).toBe(6);
  });

  test("an expired cached token re-authenticates", async () => {
    const expired = jwt({ id: 335, timestamp: Date.now() - DAY, activeClinicId: 25 });
    handler = (url) =>
      url.includes("physician/login")
        ? json({ token: jwt({ id: 335, timestamp: Date.now() - DAY }) })
        : json({ token: expired });
    await connect("prod", "ossm");
    resetSessionCache();
    calls = [];
    handler = defaultHandler;
    const s = await connect("prod", "ossm");
    expect(paths()).toEqual(["/api/v1/copilot/physician/login", "/api/v1/support/switch-clinic"]);
    expect(s.activeClinicId).toBe(25);
  });

  test("concurrent connects single-flight into one login+switch", async () => {
    const [a, b] = await Promise.all([connect("prod", "ossm"), connect("prod", "ossm")]);
    expect(paths()).toEqual(["/api/v1/copilot/physician/login", "/api/v1/support/switch-clinic"]);
    expect(a.activeClinicId).toBe(25);
    expect(b.activeClinicId).toBe(25);
  });
});

describe("listClinics", () => {
  test("returns the parsed clinics without switching clinic", async () => {
    handler = (url) => {
      if (url.includes("physician/login")) return json({ token: liveToken() });
      if (url.includes("support/clinics")) {
        return json({
          clinics: [
            {
              clinicUid: CLINIC_UID,
              name: "OSSM Clinic",
              lastActiveAt: "2026-09-07T00:00:00Z",
              ownerEmail: "owner@ossm.example.com",
            },
            { clinicUid: CLINIC_UID_2, name: "Hassan Kafri", lastActiveAt: null },
          ],
        });
      }
      throw new Error(`unexpected request: ${url}`);
    };
    const clinics = await listClinics("prod");
    expect(paths()).toEqual(["/api/v1/copilot/physician/login", "/api/v1/support/clinics"]);
    expect(clinics).toEqual([
      {
        clinicUid: CLINIC_UID,
        name: "OSSM Clinic",
        lastActiveAt: "2026-09-07T00:00:00Z",
        ownerEmail: "owner@ossm.example.com",
      },
      { clinicUid: CLINIC_UID_2, name: "Hassan Kafri", lastActiveAt: null, ownerEmail: "" },
    ]);
  });

  test("throws a config-actionable error for an env with no support account", async () => {
    await expect(listClinics("pre_prod")).rejects.toThrow(/copilot\.support\.pre_prod is required/);
    expect(calls).toHaveLength(0);
  });
});
