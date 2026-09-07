// The single auth chokepoint for the Copilot BE: login, switch-clinic, session
// caching (memory + disk), and the rate-limit message. Nothing else in this
// codebase may POST to physician/login or support/switch-clinic.
//
// Two auth modes, resolved per profile per env by config.ts's resolveAuth:
// - "direct": the pre-existing per-account physician login (unchanged behavior,
//   now cached).
// - "support": prod's new model — the support account logs in to a clinic-less
//   session, then POSTs /support/switch-clinic to get the clinic-scoped token
//   that carries MANAGE_ORDERS. See
//   ~/projects/apex-auth-submit/anthem-v2-migration/docs/copilot-support-account-auth.md
//   for the full ground truth (referenced inline as §N below).
//
// Caching: sessions are cached by (env, be, email, scope) where scope is
// "direct", "support" (the clinic-less support session), or `clinic:<uid>`.
// In-memory for the process lifetime; on disk (0600, under the user's cache
// dir) so a restart doesn't burn the 10-logins-per-15-minutes support login
// budget (§2). Single-flight in memory so concurrent tool calls never
// double-login.

import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, isAbsolute, join } from "node:path";
import process from "node:process";
import type { Env, EnvAuth } from "../config/config.js";
import { resolveAuth, resolveSupport, sessionCacheEnabled } from "../config/config.js";
import { isRecord, prop, stringProp } from "../shared/util.js";
import type { HttpClient } from "./copilot-client.js";
import { makeClient } from "./copilot-client.js";

export interface Session {
  client: HttpClient; // authenticated (and switched, in support mode); tagged with env
  env: Env;
  profile: string | null;
  mode: "support" | "direct";
  email: string; // the account that authenticated
  clinicUid: string | null; // support mode only
  activeClinicId: number | null; // asserted non-null in support mode; may be null in direct mode
  token: string;
}

export interface Clinic {
  clinicUid: string;
  name: string;
  lastActiveAt: string | null;
  ownerEmail: string;
}

// ---- pure helpers -----------------------------------------------------------

// Base64-decodes JWT segment 1 and reads one claim. Never throws — a malformed
// token, missing claim, or non-numeric value all read as null. Same decode idiom
// as queue-item.ts's decodeJwtId.
function jwtClaim(token: string, claim: string): unknown {
  try {
    const seg = token.split(".")[1] ?? "";
    const payload: unknown = JSON.parse(Buffer.from(seg, "base64").toString());
    return prop(payload, claim);
  } catch {
    return null;
  }
}

// The JWT `timestamp` claim (already milliseconds — §2), or null when absent/malformed.
export function tokenExpiry(token: string): number | null {
  const v = jwtClaim(token, "timestamp");
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

// The JWT `activeClinicId` claim, or null when absent/malformed (a clinic-less token).
export function tokenClinicId(token: string): number | null {
  const v = jwtClaim(token, "activeClinicId");
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

// Hashed cache key so the on-disk cache carries no account email.
export function sessionKey(env: Env, be: string, email: string, scope: string): string {
  return createHash("sha256").update(`${env}|${be}|${email}|${scope}`).digest("hex").slice(0, 16);
}

// ---- cache shape --------------------------------------------------------------

interface CachedSession {
  scope: string;
  activeClinicId: number | null;
  token: string;
  cookies: Record<string, string>;
  expiresAt: number;
}

interface DiskCache {
  version: 1;
  sessions: Record<string, CachedSession>;
}

const SKEW_MS = 10 * 60_000;
const fresh = (e: CachedSession): boolean => e.expiresAt - Date.now() > SKEW_MS;

const mem = new Map<string, CachedSession>();
const inflight = new Map<string, Promise<Session>>();

// XDG Base Directory spec: $XDG_CACHE_HOME must be an absolute path; a relative or
// empty value is invalid and must be treated as unset, falling back to ~/.cache.
function cachePath(): string {
  const override = process.env["COPILOT_MCP_CACHE"];
  if (override) return override;
  const xdg = process.env["XDG_CACHE_HOME"];
  const base = xdg && isAbsolute(xdg) ? xdg : join(homedir(), ".cache");
  return join(base, "copilot-mcp", "sessions.json");
}

// Any read failure (missing, unparseable, wrong version) is treated as an empty
// cache and never throws.
function readDisk(): DiskCache {
  const path = cachePath();
  if (!existsSync(path)) return { version: 1, sessions: {} };
  try {
    const parsed: unknown = JSON.parse(readFileSync(path, "utf8"));
    if (isRecord(parsed) && parsed["version"] === 1 && isRecord(parsed["sessions"])) {
      return parsed as unknown as DiskCache;
    }
    return { version: 1, sessions: {} };
  } catch {
    return { version: 1, sessions: {} };
  }
}

// Re-reads and merges before writing (two host processes share the file; a
// sibling's entry must not be clobbered), and drops expired entries. No torn
// file: write to a temp path, then rename.
function writeDisk(key: string, entry: CachedSession): void {
  if (!sessionCacheEnabled()) return;
  const path = cachePath();
  const current = readDisk();
  const sessions: Record<string, CachedSession> = {};
  for (const [k, v] of Object.entries(current.sessions)) {
    if (v.expiresAt > Date.now()) sessions[k] = v;
  }
  sessions[key] = entry;
  const out: DiskCache = { version: 1, sessions };
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const tmp = `${path}.tmp`;
  writeFileSync(tmp, JSON.stringify(out), { mode: 0o600 });
  renameSync(tmp, path);
}

// The jar is caller-owned so this module can snapshot cookies back out after a
// request populates them (makeClient closes over whatever Map it is handed).
function clientWithJar(
  base: string,
  env: Env,
  cookies?: Record<string, string>,
): { client: HttpClient; jar: Map<string, string> } {
  const jar = new Map<string, string>(cookies ? Object.entries(cookies) : []);
  return { client: makeClient(base, env, jar), jar };
}

// ---- login primitives (the ONLY two call sites that may POST these paths) ----

async function doLogin(
  be: string,
  env: Env,
  email: string,
  password: string,
): Promise<{ client: HttpClient; token: string; jar: Map<string, string> }> {
  const { client, jar } = clientWithJar(be, env);
  const r = await client.req("POST", "/api/v1/copilot/physician/login", {
    json: { email, password },
  });
  if (r.status === 429) {
    const reset = r.headers?.get("ratelimit-reset") ?? "?";
    throw new Error(
      `login failed 429: support login rate-limited (10 logins per 15 minutes) — retry after ${reset}s; do not rotate to another account`,
    );
  }
  if (r.status >= 400) throw new Error(`login failed ${r.status}: ${r.text.slice(0, 300)}`);
  const token = stringProp(r.data, "token");
  if (!token) throw new Error("login succeeded but returned no token");
  return { client, token, jar };
}

async function doSwitchClinic(
  be: string,
  env: Env,
  cookies: Record<string, string>,
  clinicUid: string,
): Promise<{ client: HttpClient; token: string; jar: Map<string, string> }> {
  const { client, jar } = clientWithJar(be, env, cookies);
  const r = await client.req("POST", "/api/v1/support/switch-clinic", { json: { clinicUid } });
  if (r.status >= 400) {
    throw new Error(
      `switch-clinic failed ${r.status} for clinic ${clinicUid}: ${r.text.slice(0, 300)}`,
    );
  }
  const token = stringProp(r.data, "token");
  if (!token)
    throw new Error(`switch-clinic succeeded but returned no token for clinic ${clinicUid}`);
  return { client, token, jar };
}

// ---- the chokepoint -----------------------------------------------------------

function cacheGet(key: string): CachedSession | null {
  const m = mem.get(key);
  if (m && fresh(m)) return m;
  const d = readDisk().sessions[key];
  if (d && fresh(d)) {
    mem.set(key, d);
    return d;
  }
  return null;
}

function cachePut(key: string, entry: CachedSession): void {
  mem.set(key, entry);
  writeDisk(key, entry);
}

function sessionFromEntry(
  entry: CachedSession,
  env: Env,
  be: string,
  profile: string | null,
  mode: "support" | "direct",
  email: string,
  clinicUid: string | null,
): Session {
  return {
    client: clientWithJar(be, env, entry.cookies).client,
    env,
    profile,
    mode,
    email,
    clinicUid,
    activeClinicId: entry.activeClinicId,
    token: entry.token,
  };
}

function cacheEntryFor(
  jar: Map<string, string>,
  token: string,
  activeClinicId: number | null,
): CachedSession {
  const expiresAt = tokenExpiry(token);
  // A token with no readable `timestamp` claim is not cached (fail closed rather
  // than guess a TTL) — signaled by expiresAt: -1, filtered out before caching.
  return {
    scope: "",
    activeClinicId,
    token,
    cookies: Object.fromEntries(jar),
    expiresAt: expiresAt ?? -1,
  };
}

async function connectDirect(
  auth: Extract<EnvAuth, { mode: "direct" }>,
  env: Env,
): Promise<Session> {
  const key = sessionKey(env, auth.be, auth.email, "direct");
  const cached = cacheGet(key);
  if (cached) return sessionFromEntry(cached, env, auth.be, null, "direct", auth.email, null);

  const { client, token, jar } = await doLogin(auth.be, env, auth.email, auth.password);
  const activeClinicId = tokenClinicId(token);
  const entry = cacheEntryFor(jar, token, activeClinicId);
  if (entry.expiresAt > 0) cachePut(key, { ...entry, scope: "direct" });
  return {
    client,
    env,
    profile: null,
    mode: "direct",
    email: auth.email,
    clinicUid: null,
    activeClinicId,
    token,
  };
}

async function connectSupport(
  auth: Extract<EnvAuth, { mode: "support" }>,
  env: Env,
): Promise<Session> {
  const scope = `clinic:${auth.clinicUid}`;
  const key = sessionKey(env, auth.be, auth.email, scope);
  const cached = cacheGet(key);
  if (cached && cached.scope === scope && tokenClinicId(cached.token) === cached.activeClinicId) {
    return sessionFromEntry(cached, env, auth.be, null, "support", auth.email, auth.clinicUid);
  }

  // Cold: obtain the clinic-less support session first (cached under its own
  // scope — it survives every switch and is the cheap way to re-list clinics).
  const supportKey = sessionKey(env, auth.be, auth.email, "support");
  let supportCookies: Record<string, string>;
  const cachedSupport = cacheGet(supportKey);
  if (cachedSupport) {
    supportCookies = cachedSupport.cookies;
  } else {
    const { token, jar } = await doLogin(auth.be, env, auth.email, auth.password);
    const entry = cacheEntryFor(jar, token, null);
    if (entry.expiresAt > 0) cachePut(supportKey, { ...entry, scope: "support" });
    supportCookies = Object.fromEntries(jar);
  }

  const { client, token, jar } = await doSwitchClinic(auth.be, env, supportCookies, auth.clinicUid);
  const activeClinicId = tokenClinicId(token);
  if (activeClinicId === null) {
    throw new Error(
      `switch-clinic returned a clinic-less token for ${auth.clinicUid} — refusing to use it`,
    );
  }
  const entry = cacheEntryFor(jar, token, activeClinicId);
  if (entry.expiresAt > 0) cachePut(key, { ...entry, scope });
  return {
    client,
    env,
    profile: null,
    mode: "support",
    email: auth.email,
    clinicUid: auth.clinicUid,
    activeClinicId,
    token,
  };
}

export async function connect(env: Env, profile: string | null | undefined): Promise<Session> {
  const auth = resolveAuth(profile, env);
  const scope = auth.mode === "support" ? `clinic:${auth.clinicUid}` : "direct";
  const key = sessionKey(env, auth.be, auth.email, scope);
  const running = inflight.get(key);
  if (running) {
    const s = await running;
    return { ...s, profile: profile ?? null };
  }
  const task = (
    auth.mode === "support" ? connectSupport(auth, env) : connectDirect(auth, env)
  ).finally(() => inflight.delete(key));
  inflight.set(key, task);
  const s = await task;
  return { ...s, profile: profile ?? null };
}

export async function listClinics(env: Env): Promise<Clinic[]> {
  const support = resolveSupport(env);
  const supportKey = sessionKey(env, support.be, support.email, "support");
  let cookies: Record<string, string>;
  const cached = cacheGet(supportKey);
  if (cached) {
    cookies = cached.cookies;
  } else {
    const { token, jar } = await doLogin(support.be, env, support.email, support.password);
    const entry = cacheEntryFor(jar, token, null);
    if (entry.expiresAt > 0) cachePut(supportKey, { ...entry, scope: "support" });
    cookies = Object.fromEntries(jar);
  }
  const { client } = clientWithJar(support.be, env, cookies);
  const r = await client.req("GET", "/api/v1/support/clinics");
  if (r.status >= 400) throw new Error(`list clinics failed ${r.status}: ${r.text.slice(0, 300)}`);
  const clinics = prop(r.data, "clinics");
  if (!Array.isArray(clinics)) return [];
  return clinics.map((c) => ({
    clinicUid: stringProp(c, "clinicUid") ?? "",
    name: stringProp(c, "name") ?? "",
    lastActiveAt: stringProp(c, "lastActiveAt") ?? null,
    ownerEmail: stringProp(c, "ownerEmail") ?? "",
  }));
}

export async function clinicOwnerEmail(env: Env, clinicUid: string): Promise<string | null> {
  const clinics = await listClinics(env);
  return clinics.find((c) => c.clinicUid === clinicUid)?.ownerEmail ?? null;
}

// Clears the in-memory caches. Tests only — does not touch disk (tests point
// COPILOT_MCP_CACHE at a tmp file and remove the dir themselves).
export function resetSessionCache(): void {
  mem.clear();
  inflight.clear();
}
