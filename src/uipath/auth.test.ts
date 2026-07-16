import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { resetOAuthTokenCache } from "./auth.js";
import { listRecentJobs } from "./uipath.js";

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const ORCHESTRATOR_URL = "https://cloud.uipath.com/myorg/mytenant/orchestrator_";
const TOKEN_URL = "https://cloud.uipath.com/myorg/identity_/connect/token";

const fixture = (uipath: Record<string, unknown>) => ({
  copilot: { prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: { orchestratorUrl: ORCHESTRATOR_URL, ...uipath },
});

let dir: string;
let configPath: string;
let prevConfig: string | undefined;

// Fixture switch via resetConfigCache — matches actions.test.ts. Doesn't exercise the
// mtime-based live-reload path (see the dedicated reload test below, which does).
const writeConfig = (uipath: Record<string, unknown>): void => {
  writeFileSync(configPath, JSON.stringify(fixture(uipath)));
  resetConfigCache();
};

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-uipath-auth-"));
  configPath = join(dir, "config.json");
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = configPath;
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

interface RecordedCall {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string | undefined;
}

let calls: RecordedCall[];
let responses: Response[];
const realFetch = globalThis.fetch;

beforeEach(() => {
  calls = [];
  responses = [];
  resetOAuthTokenCache();
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const headers: Record<string, string> = {};
    for (const [k, v] of new Headers(init?.headers).entries()) headers[k] = v;
    calls.push({
      method: init?.method ?? "GET",
      url: String(input),
      headers,
      body: typeof init?.body === "string" ? init.body : undefined,
    });
    return responses.shift() ?? new Response("{}", { status: 200 });
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  resetOAuthTokenCache();
});

const jobsPage = (jobs: unknown[] = []): Response =>
  new Response(JSON.stringify({ value: jobs }), { status: 200 });
const tokenResponse = (accessToken: string, expiresIn = 3600): Response =>
  new Response(JSON.stringify({ access_token: accessToken, expires_in: expiresIn }), {
    status: 200,
  });
const tokenCallsOf = (): RecordedCall[] =>
  calls.filter((c) => c.url.includes("identity_/connect/token"));

describe("resolveBearerToken (exercised via uipathRequest/listRecentJobs)", () => {
  test("oauth configured: fetches a token then carries it on the Orchestrator call", async () => {
    writeConfig({ oauth: { clientId: "id", clientSecret: "secret" } });
    responses.push(tokenResponse("tok-1"), jobsPage());
    await listRecentJobs(undefined, 1);
    expect(calls.length).toBe(2);
    expect(calls[0]?.method).toBe("POST");
    expect(calls[0]?.url).toBe(TOKEN_URL);
    expect(calls[0]?.headers["content-type"]).toBe("application/x-www-form-urlencoded");
    expect(calls[0]?.body).toBe("grant_type=client_credentials&client_id=id&client_secret=secret");
    expect(calls[1]?.method).toBe("GET");
    expect(calls[1]?.headers["authorization"]).toBe("Bearer tok-1");
  });

  test("includes scope in the token request when configured", async () => {
    writeConfig({ oauth: { clientId: "id", clientSecret: "secret", scope: "OR.Jobs OR.Queues" } });
    responses.push(tokenResponse("tok-1"), jobsPage());
    await listRecentJobs(undefined, 1);
    expect(calls[0]?.body).toContain("scope=OR.Jobs+OR.Queues");
  });

  test("caches the oauth token across calls until it expires", async () => {
    writeConfig({ oauth: { clientId: "id", clientSecret: "secret" } });
    responses.push(tokenResponse("tok-1", 3600), jobsPage(), jobsPage());
    await listRecentJobs(undefined, 1);
    await listRecentJobs(undefined, 1);
    expect(tokenCallsOf().length).toBe(1);
    expect(calls.filter((c) => c.url.includes("/odata/Jobs")).length).toBe(2);
  });

  test("falls back to bearer when the oauth token request fails and bearer is configured", async () => {
    writeConfig({ oauth: { clientId: "id", clientSecret: "secret" }, bearer: "fallback-bearer" });
    responses.push(new Response("boom", { status: 500 }), jobsPage());
    await listRecentJobs(undefined, 1);
    expect(calls[1]?.headers["authorization"]).toBe("Bearer fallback-bearer");
  });

  test("propagates the oauth error when the token request fails and no bearer is configured", async () => {
    writeConfig({ oauth: { clientId: "id", clientSecret: "secret" } });
    responses.push(new Response("boom", { status: 500 }));
    await expect(listRecentJobs(undefined, 1)).rejects.toThrow(/UiPath OAuth token request -> 500/);
    expect(calls.length).toBe(1);
  });

  test("derives the token URL from orchestratorUrl when oauth.tokenUrl is omitted", async () => {
    writeConfig({ oauth: { clientId: "id", clientSecret: "secret" } });
    responses.push(tokenResponse("tok-1"), jobsPage());
    await listRecentJobs(undefined, 1);
    expect(calls[0]?.url).toBe(TOKEN_URL);
  });

  test("uses an explicit oauth.tokenUrl when given", async () => {
    writeConfig({
      oauth: {
        clientId: "id",
        clientSecret: "secret",
        tokenUrl: "https://example.com/custom/token",
      },
    });
    responses.push(tokenResponse("tok-1"), jobsPage());
    await listRecentJobs(undefined, 1);
    expect(calls[0]?.url).toBe("https://example.com/custom/token");
  });

  test("no oauth configured: uses bearer directly, no token endpoint call", async () => {
    writeConfig({ bearer: "plain-bearer" });
    responses.push(jobsPage());
    await listRecentJobs(undefined, 1);
    expect(calls.length).toBe(1);
    expect(calls[0]?.headers["authorization"]).toBe("Bearer plain-bearer");
  });

  test("a config reload clears the cached oauth token and re-fetches", async () => {
    writeConfig({ oauth: { clientId: "id-1", clientSecret: "secret" } });
    responses.push(tokenResponse("tok-1"), jobsPage());
    await listRecentJobs(undefined, 1);

    // Live-edit without resetConfigCache so onConfigReload listeners fire (matches
    // config.test.ts's "live reload" block) — this is what auth.ts's cache-clearing
    // subscription reacts to.
    writeFileSync(
      configPath,
      JSON.stringify(fixture({ oauth: { clientId: "id-2", clientSecret: "secret" } })),
    );
    const future = new Date(Date.now() + 2000);
    utimesSync(configPath, future, future);

    responses.push(tokenResponse("tok-2"), jobsPage());
    await listRecentJobs(undefined, 1);

    expect(tokenCallsOf().length).toBe(2);
  });
});
