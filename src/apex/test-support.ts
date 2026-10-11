// Shared fixtures for tests that run the REAL Apex client against a stubbed fetch.
// Not a test file itself; imported by apex/*.test.ts and copilot/doctor.test.ts.
// useApexStub() installs hooks, so call it inside a describe (or at file top level).

import { afterAll, afterEach, beforeAll, beforeEach } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { _resetApexClient } from "./client.js";

export const APEX_URL = "https://apex.example.com";
const API_PREFIX = `${APEX_URL}/api/v1`;

export const FOLDER_IDS: Record<string, string> = {
  Authorization: "11111111-1111-1111-1111-111111111111",
  "Authorization Test": "22222222-2222-2222-2222-222222222222",
  "Eligibility Check": "33333333-3333-3333-3333-333333333333",
};

export interface ApexCall {
  method: string;
  path: string; // after /api/v1, query string included
  headers: Record<string, string>; // lower-cased keys
  body: unknown; // parsed JSON, undefined when none
}
type Handler = (call: ApexCall) => Response | Promise<Response>;

export const json = (body: unknown, status = 200, headers: Record<string, string> = {}): Response =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", ...headers },
  });

const b64url = (v: unknown): string => Buffer.from(JSON.stringify(v)).toString("base64url");

// A structurally real (unsigned) JWT: the client only decodes the payload's `exp`.
export const fakeJwt = (exp = Math.floor(Date.now() / 1000) + 86_400): string =>
  `${b64url({ alg: "none" })}.${b64url({ sub: "u", exp })}.sig`;

const loginResponse = (jwt: string): Response =>
  json({ user: { username: "tester" } }, 200, {
    "Set-Cookie": `access_token=${jwt}; HttpOnly; Path=/; SameSite=Lax; Secure`,
  });

const foldersResponse = (): Response =>
  json({
    items: Object.entries(FOLDER_IDS).map(([name, id]) => ({ id, name })),
    total: 3,
    page: 1,
    page_size: 200,
    total_pages: 1,
  });

const baseConfig = (apex: Record<string, unknown> | null): string => {
  const creds = (n: string) => ({
    be: `https://be.${n}.example.com`,
    email: `${n}@x.com`,
    password: "pw",
  });
  return JSON.stringify({
    copilot: { sessionCache: false, prod: creds("prod"), pre_prod: creds("preprod") },
    uipath: { orchestratorUrl: "https://cloud.uipath.com/o/t/orchestrator_", bearer: "b" },
    ...(apex ? { apex } : {}),
  });
};

const APEX_ENV = ["APEX_API_URL", "APEX_USER_NAME", "APEX_PASSWORD"];

// `apex` = the whole config section (null: no apex section). Default: a fully configured one.
// Routes match by method + path prefix (after /api/v1); the LAST registered match wins, so a
// test overrides the default login/folders routes by registering its own. A handler that throws
// or rejects behaves like a network error. Unmatched requests answer 404.
export function useApexStub(opts: { apex?: Record<string, unknown> | null } = {}) {
  const apexSection =
    opts.apex === undefined
      ? {
          apiUrl: APEX_URL,
          username: "tester",
          password: "pw",
          serverUrlByEnv: { pre_prod: "https://be.pre.example" },
          queueUrl: "https://sqs.example/q",
        }
      : opts.apex;
  const realFetch = globalThis.fetch;
  const calls: ApexCall[] = [];
  let routes: { method: string; prefix: string; handler: Handler }[] = [];
  let dir: string;
  let prevConfig: string | undefined;
  const prevEnv: Record<string, string | undefined> = {};

  const route = (method: string, prefix: string, handler: Handler): void => {
    routes.push({ method, prefix, handler });
  };
  const callsTo = (method: string, prefix: string): ApexCall[] =>
    calls.filter((c) => c.method === method && c.path.startsWith(prefix));

  beforeAll(() => {
    dir = mkdtempSync(join(tmpdir(), "copilot-mcp-apex-"));
    const path = join(dir, "config.json");
    writeFileSync(path, baseConfig(apexSection));
    prevConfig = process.env["COPILOT_MCP_CONFIG"];
    process.env["COPILOT_MCP_CONFIG"] = path;
    for (const k of APEX_ENV) {
      prevEnv[k] = process.env[k];
      delete process.env[k];
    }
    resetConfigCache();
  });

  afterAll(() => {
    if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
    else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
    for (const k of APEX_ENV) {
      const v = prevEnv[k];
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
    resetConfigCache();
    rmSync(dir, { recursive: true, force: true });
  });

  beforeEach(() => {
    calls.length = 0;
    routes = [];
    route("POST", "/auth/login", () => loginResponse(fakeJwt()));
    route("GET", "/folders", () => foldersResponse());
    _resetApexClient();
    globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      const headers: Record<string, string> = {};
      for (const [k, v] of new Headers(init?.headers).entries()) headers[k] = v;
      const call: ApexCall = {
        method: init?.method ?? "GET",
        path: url.startsWith(API_PREFIX) ? url.slice(API_PREFIX.length) : url,
        headers,
        body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      };
      calls.push(call);
      const hit = [...routes]
        .reverse()
        .find((r) => r.method === call.method && call.path.startsWith(r.prefix));
      return hit
        ? hit.handler(call)
        : json({ detail: `no stub for ${call.method} ${call.path}` }, 404);
    }) as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = realFetch;
    _resetApexClient();
  });

  return { calls, route, callsTo };
}
