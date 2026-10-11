// The single Apex HTTP chokepoint — nothing else in the repo may fetch against Apex.
// Owns: login (JWT from the Set-Cookie, in-memory, single-flighted, refreshed before its
// own `exp`), folder-name -> X-Folder-Id resolution (sent on EVERY request: omitting it
// silently reads the user's default folder), the pre_prod-only write gate, retries
// (GETs only), and error shaping. Apex routes take paths relative to `/api/v1`.

import { type ApexConfig, type Env, onConfigReload, resolveApex } from "../config/config.js";
import { ExpectedError } from "../shared/expected-error.js";
import { rateLimitDelayMs, sleep } from "../shared/http.js";
import { prop, safeJsonParse } from "../shared/util.js";
import { ApexApiError, ApexNotConfiguredError, apexErrorDetail } from "./errors.js";

type Method = "GET" | "POST" | "PATCH" | "DELETE";

const API = "/api/v1";
const LOGIN_PATH = "/auth/login";
const MAX_PAGE_SIZE = 200; // above this the server answers 422
const GET_ATTEMPTS = 3; // network-error retries apply to GETs only: a retried write could double-post
const NETWORK_BACKOFF_MS = 50; // × attempt number
const MAX_RATE_LIMIT_RETRIES = 3;
const TOKEN_SKEW_MS = 60_000; // refresh this long before the JWT's own expiry
const FALLBACK_TOKEN_TTL_MS = 10 * 60_000; // when the JWT carries no readable exp

interface Token {
  value: string;
  expiresAt: number; // epoch ms
}

let token: Token | undefined;
let loginInFlight: Promise<string> | undefined;
let folderIds: Promise<Map<string, string>> | undefined;

// Test-only (and config-reload): forget the session token and the folder-id cache.
export function _resetApexClient(): void {
  token = undefined;
  loginInFlight = undefined;
  folderIds = undefined;
}

// A config edit may change the account, URL or folders — never keep a token/ids from the old one.
onConfigReload(_resetApexClient);

// `exp` (seconds) from the JWT payload, as epoch ms; undefined when not readable.
function jwtExpiryMs(jwt: string): number | undefined {
  const payload = jwt.split(".")[1];
  const exp = payload
    ? prop(safeJsonParse(Buffer.from(payload, "base64url").toString()), "exp")
    : undefined;
  return typeof exp === "number" ? exp * 1000 : undefined;
}

// Login's only useful output is the `access_token` Set-Cookie — the body holds user info.
async function login(cfg: ApexConfig): Promise<Token> {
  const res = await fetch(`${cfg.apiUrl}${API}${LOGIN_PATH}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ username: cfg.username, password: cfg.password }),
  });
  const text = await res.text();
  if (res.status >= 400) {
    throw new ApexApiError(
      "POST",
      LOGIN_PATH,
      res.status,
      apexErrorDetail(safeJsonParse(text), res.status),
    );
  }
  const cookie = res.headers.getSetCookie().find((c) => c.startsWith("access_token="));
  const value = cookie?.slice("access_token=".length).split(";")[0];
  if (!value) {
    throw new ApexApiError(
      "POST",
      LOGIN_PATH,
      res.status,
      "login succeeded but returned no access_token cookie",
    );
  }
  const expiry = jwtExpiryMs(value) ?? Date.now() + FALLBACK_TOKEN_TTL_MS;
  return { value, expiresAt: expiry - TOKEN_SKEW_MS };
}

// Concurrent callers share one login (the login route is rate-limited).
function bearerToken(cfg: ApexConfig): Promise<string> {
  if (token && token.expiresAt > Date.now()) return Promise.resolve(token.value);
  loginInFlight ??= login(cfg)
    .then((t) => {
      token = t;
      return t.value;
    })
    .finally(() => {
      loginInFlight = undefined;
    });
  return loginInFlight;
}

// One authenticated request. A 401 re-logins exactly once and retries (any method: the
// server rejected the credentials, so nothing ran). Only GETs retry on network errors or
// 429; writes surface the first failure.
async function transport(
  cfg: ApexConfig,
  method: Method,
  url: string,
  headers: Record<string, string>,
  body?: string,
): Promise<Response> {
  let relogged = false;
  let rateLimited = 0;
  let netFailures = 0;
  for (;;) {
    const used = await bearerToken(cfg);
    let res: Response;
    try {
      res = await fetch(url, {
        method,
        headers: { ...headers, Authorization: `Bearer ${used}` },
        ...(body !== undefined ? { body } : {}),
      });
    } catch (e) {
      netFailures++;
      if (method !== "GET" || netFailures >= GET_ATTEMPTS) throw e;
      await sleep(NETWORK_BACKOFF_MS * netFailures);
      continue;
    }
    if (res.status === 401 && !relogged) {
      relogged = true;
      if (token?.value === used) token = undefined; // a concurrent request may already have refreshed it
      continue;
    }
    if (res.status === 429 && method === "GET" && rateLimited < MAX_RATE_LIMIT_RETRIES) {
      rateLimited++;
      await sleep(rateLimitDelayMs(res.headers.get("Retry-After")));
      continue;
    }
    return res;
  }
}

// name -> uuid for every folder. Cached for the process; a failed fetch is not cached.
// ponytail: one page (200 folders) — walk total_pages if a deployment ever has more.
// This is the one call that cannot carry X-Folder-Id: it is how the ids are found.
function folderMap(cfg: ApexConfig): Promise<Map<string, string>> {
  folderIds ??= (async () => {
    const res = await transport(
      cfg,
      "GET",
      `${cfg.apiUrl}${API}/folders?page_size=${MAX_PAGE_SIZE}`,
      {
        Accept: "application/json",
      },
    );
    const body = safeJsonParse(await res.text());
    if (res.status >= 400) {
      throw new ApexApiError("GET", "/folders", res.status, apexErrorDetail(body, res.status));
    }
    const items = prop(body, "items");
    const rows = Array.isArray(items) ? items : [];
    const map = new Map<string, string>();
    for (const row of rows) {
      const id = prop(row, "id");
      const name = prop(row, "name");
      if (typeof id === "string" && typeof name === "string") map.set(name, id);
    }
    return map;
  })().catch((e: unknown) => {
    folderIds = undefined;
    throw e;
  });
  return folderIds;
}

async function lookupFolder(
  cfg: ApexConfig,
  env: Env,
  folder: string | undefined,
): Promise<{ id: string; name: string }> {
  const name = folder ?? cfg.folderByEnv[env];
  const ids = await folderMap(cfg);
  const id = ids.get(name);
  if (!id) {
    throw new ExpectedError(
      `Apex folder '${name}' not found (available: ${[...ids.keys()].join(", ") || "none"})`,
    );
  }
  return { id, name };
}

// The X-Folder-Id uuid for an env's configured folder, or for an explicit `folder` name.
export function resolveFolderId(env: Env, folder?: string): Promise<{ id: string; name: string }> {
  return lookupFolder(resolveApex(), env, folder);
}

// Prod and test folders share one deployment and one login, so this gate — checked
// before anything touches the network — is the only thing keeping writes out of prod.
function assertWritable(env: Env, method: Method, path: string, folder: string | undefined): void {
  if (method === "GET") return;
  if (env !== "pre_prod") {
    throw new ExpectedError(
      `Apex ${method} ${path} refused: writes are pre_prod-only (env=${env})`,
    );
  }
  if (folder) {
    throw new ExpectedError(`Apex ${method} ${path} refused: writes never take a folder override`);
  }
}

// One Apex JSON call, scoped to the env's folder (or `opts.folder`, reads only).
// Returns the parsed body (null when empty); throws ApexApiError on a non-2xx.
export async function apexRequest(
  env: Env,
  method: Method,
  path: string,
  opts: { query?: Record<string, string>; body?: unknown; folder?: string | undefined } = {},
): Promise<unknown> {
  assertWritable(env, method, path, opts.folder);
  const cfg = resolveApex();
  const { id } = await lookupFolder(cfg, env, opts.folder);
  const qs = new URLSearchParams(opts.query ?? {}).toString();
  const hasBody = opts.body !== undefined;
  const res = await transport(
    cfg,
    method,
    `${cfg.apiUrl}${API}${path}${qs ? `?${qs}` : ""}`,
    {
      Accept: "application/json",
      "X-Folder-Id": id,
      ...(hasBody ? { "Content-Type": "application/json" } : {}),
    },
    hasBody ? JSON.stringify(opts.body) : undefined,
  );
  const parsed = safeJsonParse(await res.text());
  if (res.status >= 400) {
    throw new ApexApiError(method, path, res.status, apexErrorDetail(parsed, res.status));
  }
  return parsed ?? null;
}

// Rows of a paged route. Walks `page` up to `total_pages` until `max` rows are collected;
// bare-array routes (workflow versions, review-queue, artifacts) come back as-is.
export async function apexPages<T>(
  env: Env,
  path: string,
  query: Record<string, string>,
  max: number,
  folder?: string,
): Promise<T[]> {
  const rows: T[] = [];
  const pageSize = Math.min(Number(query["page_size"] ?? MAX_PAGE_SIZE), MAX_PAGE_SIZE, max);
  for (let page = 1; rows.length < max; page++) {
    const body = await apexRequest(env, "GET", path, {
      query: { ...query, page: String(page), page_size: String(pageSize) },
      folder,
    });
    if (Array.isArray(body)) return body.slice(0, max) as T[];
    const items = prop(body, "items");
    if (Array.isArray(items)) rows.push(...(items as T[]));
    const totalPages = prop(body, "total_pages");
    if (
      !Array.isArray(items) ||
      items.length === 0 ||
      typeof totalPages !== "number" ||
      page >= totalPages
    )
      break;
  }
  return rows.slice(0, max);
}

// Log in now (or reuse the live session) without making a data call — what `doctor` probes.
export async function apexLogin(): Promise<void> {
  await bearerToken(resolveApex());
}

// Artifact bytes from a `download_url` exactly as the artifact listing returns it
// (`/api/v1/jobs/{id}/artifacts/{artifactId}`). Bearer only: downloads need no folder
// header. A Range read (`bytes=-65536`) gives a cheap tail of a large file. GET-only, so
// no write gate; `env` is accepted for call-site symmetry with the other client entry points.
export async function apexDownload(
  _env: Env,
  downloadUrl: string,
  opts: { range?: string } = {},
): Promise<{ status: number; bytes: Uint8Array; contentRange: string | null }> {
  // Only same-origin API paths: the Bearer token must never be sent to another host.
  if (!downloadUrl.startsWith(`${API}/`) || downloadUrl.includes("..")) {
    throw new ExpectedError(`Apex download refused: '${downloadUrl}' is not an ${API}/ path`);
  }
  const cfg = resolveApex();
  const path = downloadUrl.slice(API.length);
  const res = await transport(cfg, "GET", `${cfg.apiUrl}${downloadUrl}`, {
    ...(opts.range ? { Range: opts.range } : {}),
  });
  if (res.status >= 400) {
    const detail = apexErrorDetail(safeJsonParse(await res.text()), res.status);
    throw new ApexApiError("GET", path, res.status, detail);
  }
  return {
    status: res.status,
    bytes: new Uint8Array(await res.arrayBuffer()),
    contentRange: res.headers.get("content-range"),
  };
}

// Dashboard deep link (`/jobs/{id}` is served from the same base URL); "" when Apex is unconfigured.
export function apexDashboardLink(path: string): string {
  try {
    return `${resolveApex().apiUrl}${path}`;
  } catch (e) {
    if (e instanceof ApexNotConfiguredError) return "";
    throw e;
  }
}
