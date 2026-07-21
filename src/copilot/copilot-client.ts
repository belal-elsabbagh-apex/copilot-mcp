// EHR Copilot BE HTTP client + shared order types/helpers.
//
// Ported natively (node/bun builtins + global fetch only — no external deps) from
// the legacy `.planning/copy_order_prod_to_preprod.mjs` harness. The order mirror
// (mirror.ts) and the UiPath queue-item builder (queue-item.ts) both import from here.

import type { Env } from "../config/config.js";
import { envelopeRows, isRecord, prop, safeJsonParse, stringProp } from "../shared/util.js";

// orderMode sent on every /orders/filter call (orders + pcp notes).
export const ORDER_MODE = '["orders_only_mode","pcp_notes_mode"]';

// ---- BE order shapes (large + loosely typed; we read a known subset) -------

export interface BeCode {
  code?: string;
  description?: string;
  units?: number | string;
  treatments?: unknown[] | null;
}
export interface BeFacility {
  referredFacilityUid?: string;
  name?: string;
  NPI?: string;
  npi?: string;
  address?: string;
  faxNumber?: string;
  phone?: string;
  placeOfService?: string;
  isSpeciality?: boolean;
  external?: boolean;
}
export interface BeSpeciality {
  specialityUid?: string;
  referredFacilities?: BeFacility[];
}
export interface BeOrder {
  orderUid?: string;
  status?: string;
  category?: string;
  creationDate?: string;
  submissionStatus?: string;
  submissionReference?: string;
  patient?: { patientName?: string; patientBirthDate?: string; patientPhoneNumber?: string };
  insurance?: { name?: string; memberId?: string };
  orderType?: { typeUid?: string; name?: string };
  speciality?: { name?: string };
  referredFacility?: BeFacility;
  // Mutually exclusive with referredFacility — provider-targeted orders are
  // PCP-notes sends (insurance null); payer referrals always target a facility.
  referredProvider?: { referredProviderUid?: string; name?: string; NPI?: string };
  clinicProvider?: { clinicProviderUid?: string };
  orderNames?: { nameUid?: string }[];
  ICDCodes?: BeCode[];
  CPTCodes?: BeCode[];
  location?: string;
  uploadAuth?: boolean;
  uploadFax?: boolean;
  authStatus?: string;
  uploadStatusAuth?: string;
  uploadStatusFax?: string;
  retro?: boolean;
  urgency?: string;
  comments?: string;
  authorization?: Record<string, unknown>;
  appointmentDate?: string;
  encounterDate?: string;
  orderDate?: string;
  placeOfService?: string;
  requiredAuthorization?: boolean;
  latestProgressNote?: unknown;
  // CDN document pointers (see order-docs.ts). The BE stamps authScreenshotFileUrl
  // with the deterministic CDN path even when the file is absent — hasAuthScreenshot
  // is the existence signal. authorizationResultURL holds the summary result when set.
  authScreenshotFileUrl?: string;
  hasAuthScreenshot?: boolean;
  authorizationResultURL?: string | null;
  referralFormJSON?: {
    from?: Record<string, unknown>;
    to?: Record<string, unknown>;
    patient?: { phoneNumber?: string };
    logoURL?: string;
  };
  faxCoverJSON?: { logoURL?: string };
  [k: string]: unknown;
}

export interface OrderVerify {
  status?: string | undefined;
  submissionStatus?: string | undefined;
  submissionReference?: string | undefined;
  patient?: string | undefined;
  insurance?: string | undefined;
  memberId?: string | undefined;
  type?: string | undefined;
  facility?: string | undefined;
  placeOfService?: string | undefined;
  requiredAuthorization?: boolean | undefined;
  appointmentDate?: string | undefined;
  icds?: string[] | undefined;
  cpts?: string[] | undefined;
  notePresent?: boolean | undefined;
}

// ---- HTTP client ----------------------------------------------------------

export interface HttpResponse {
  status: number;
  data: unknown;
  text: string;
}
export interface ReqBody {
  json?: unknown;
  form?: FormData;
}
export interface HttpClient {
  base: string;
  env?: Env; // which tenant this client points at; write engines refuse anything but a tagged pre_prod client
  req(method: string, path: string, body?: ReqBody): Promise<HttpResponse>;
}

// Fail-closed guard for the BE write engines (mint, order delete, settings apply):
// only a client explicitly tagged "pre_prod" may write. An untagged client is
// refused too, so a future call site can't silently write to the wrong tenant.
export function assertPreProdClient(c: HttpClient, what: string): void {
  if (c.env !== "pre_prod") {
    throw new Error(
      `${what} writes are pre_prod-only — refusing client tagged '${c.env ?? "untagged"}' (${c.base})`,
    );
  }
}

// Minimal cookie-jar fetch client (mirrors the BE's set-cookie session flow).
// Pass `env` to tag the client with its tenant — required for clients handed
// to a write engine (see assertPreProdClient).
export function makeClient(base: string, env?: Env): HttpClient {
  const jar = new Map<string, string>();
  async function req(
    method: string,
    path: string,
    { json, form }: ReqBody = {},
  ): Promise<HttpResponse> {
    const headers: Record<string, string> = {};
    if (jar.size) headers["cookie"] = [...jar.entries()].map(([k, v]) => `${k}=${v}`).join("; ");
    let body: string | FormData | undefined;
    if (json !== undefined) {
      headers["content-type"] = "application/json";
      body = JSON.stringify(json);
    } else if (form) {
      body = form; // FormData -> fetch sets multipart content-type + boundary
    }
    const res = await fetch(base + path, {
      method,
      headers,
      ...(body !== undefined ? { body } : {}),
    });
    const setCookies =
      (res.headers as Headers & { getSetCookie?: () => string[] }).getSetCookie?.() ?? [];
    for (const sc of setCookies) {
      const pair = sc.split(";")[0] ?? "";
      const i = pair.indexOf("=");
      if (i > 0) jar.set(pair.slice(0, i).trim(), pair.slice(i + 1).trim());
    }
    const text = await res.text();
    return { status: res.status, data: safeJsonParse(text), text };
  }
  return env ? { base, env, req } : { base, req };
}

// Retries once on a 403 missing_token (session-cookie race) — the same race recurs at
// every write call site (mintPreprodOrder's PUTs and note upload, submitOrder), so they
// all share this instead of hand-copying the check-refresh-retry sequence.
export async function reqWithRefresh(
  c: HttpClient,
  method: string,
  path: string,
  body?: ReqBody,
): Promise<HttpResponse> {
  let r = await c.req(method, path, body);
  if (r.status === 403 && /missing_token/.test(r.text)) {
    await c.req("GET", "/api/v1/physician/refresh");
    r = await c.req(method, path, body);
  }
  return r;
}

export async function login(c: HttpClient, email: string, password: string): Promise<void> {
  const r = await c.req("POST", "/api/v1/copilot/physician/login", { json: { email, password } });
  if (r.status >= 400) throw new Error(`login failed ${r.status}: ${r.text.slice(0, 300)}`);
}

// Log in and return the BE session JWT (the `token` field of the login response).
// Used as SpecificContent.token for UiPath callbacks; the cookie jar is also primed
// as a side effect. Throws if login fails or the response carries no token.
export async function loginToken(c: HttpClient, email: string, password: string): Promise<string> {
  const r = await c.req("POST", "/api/v1/copilot/physician/login", { json: { email, password } });
  if (r.status >= 400) throw new Error(`login failed ${r.status}: ${r.text.slice(0, 300)}`);
  const token = stringProp(r.data, "token");
  if (!token) throw new Error("login succeeded but returned no token");
  return token;
}

// The full documented /orders/filter request-body contract (see the
// copilot-orders-filter reference). Every array-valued filter is a JSON-ENCODED
// STRING, not a bare array (e.g. `locations: '["OSSM COR"]'`) — callers building
// these from real arrays should encode before constructing this body (see
// order-search.ts's buildFilterBody for the pure encoding step).
export interface OrderFilterBody {
  pageSize: number;
  pageNumber: number;
  type?: string;
  orderMode?: string;
  searchUid?: string;
  search?: string;
  locations?: string;
  insurances?: string;
  referredTo?: string;
  orderType?: string;
  authRequired?: string;
  authStatus?: string;
  uploadStatusAuth?: string;
  uploadStatusFax?: string;
  hasAuthScreenshot?: boolean;
  sendFax?: boolean;
  missingNotes?: boolean;
  mrn?: string;
  fromDate?: string;
  toDate?: string;
  orderDateFrom?: string;
  orderDateTo?: string;
  appointmentDateFrom?: string;
  appointmentDateTo?: string;
  submissionDateFrom?: string;
  submissionDateTo?: string;
  notificationDateFrom?: string;
  notificationDateTo?: string;
  sort?: string;
}

// Single low-level POST /orders/filter wrapper — every call site (fetchOrder,
// verify, findStuckOrders, find_clone_candidates, searchOrders) shares this
// instead of building its own request/response handling.
export async function filterOrders(
  c: HttpClient,
  body: OrderFilterBody,
): Promise<{ rows: BeOrder[]; total?: number }> {
  const r = await c.req("POST", "/api/v1/orders/filter", { json: body });
  if (r.status >= 400)
    throw new Error(`/orders/filter failed ${r.status}: ${r.text.slice(0, 300)}`);
  const total = prop(r.data, "totalNumberOfElements");
  return {
    rows: envelopeRows(r.data) as BeOrder[],
    ...(typeof total === "number" ? { total } : {}),
  };
}

// POST /orders/category/stats — same filter body (minus paging), returns per-category
// folder counts. Response shape is passed through as-is: no live sample exists in this
// codebase to type it against yet (see reference/copilot-orders-filter.md for the
// documented folder/sub-bucket names) — verify against a real account before relying
// on specific fields.
export async function categoryStats(
  c: HttpClient,
  body: Omit<OrderFilterBody, "pageSize" | "pageNumber">,
): Promise<Record<string, unknown>> {
  const r = await c.req("POST", "/api/v1/orders/category/stats", { json: body });
  if (r.status >= 400)
    throw new Error(`/orders/category/stats failed ${r.status}: ${r.text.slice(0, 300)}`);
  return isRecord(r.data) ? r.data : {};
}

// /orders/filter for one uid; throws if the order isn't found in this env.
export async function fetchOrder(c: HttpClient, uid: string): Promise<BeOrder> {
  const { rows } = await filterOrders(c, {
    searchUid: uid,
    pageSize: 30,
    pageNumber: 0,
    type: "Outbound Referral",
    orderMode: ORDER_MODE,
  });
  const o = rows[0];
  if (!o) throw new Error(`order ${uid} not found in this env`);
  return o;
}

// Normalize a raw BE order into the read-only status subset the tools expose. Pure.
export function normalizeOrder(o: BeOrder): OrderVerify {
  return {
    status: o.status,
    submissionStatus: o.submissionStatus,
    submissionReference: o.submissionReference,
    patient: o.patient?.patientName,
    insurance: o.insurance?.name,
    memberId: o.insurance?.memberId,
    type: o.orderType?.name,
    facility: o.referredFacility?.name,
    placeOfService: o.placeOfService,
    requiredAuthorization: o.requiredAuthorization,
    appointmentDate: o.appointmentDate,
    icds: (o.ICDCodes ?? []).map((i) => i.code ?? ""),
    cpts: (o.CPTCodes ?? []).map((c2) => c2.code ?? ""),
    notePresent: !!o.latestProgressNote,
  };
}

// Read-only status summary for an order (used by clone verify + analyze enrich).
// Swallows both "not found" and HTTP-error responses into null (matches the prior
// behavior of reading straight off the response body regardless of status).
export async function verify(c: HttpClient, uid: string): Promise<OrderVerify | null> {
  try {
    const { rows } = await filterOrders(c, {
      searchUid: uid,
      pageSize: 30,
      pageNumber: 0,
      type: "Outbound Referral",
      orderMode: ORDER_MODE,
    });
    const o = rows[0];
    return o ? normalizeOrder(o) : null;
  } catch {
    return null;
  }
}

// Submit a pre-prod order that is sitting at forReview (advances it to inProgress).
// `pre` must already be logged in and tagged pre_prod (fails closed via
// assertPreProdClient). Retries once on a 403 missing_token (session-cookie race),
// mirroring every other write in this codebase.
export async function submitOrder(pre: HttpClient, uid: string): Promise<OrderVerify | null> {
  assertPreProdClient(pre, "submitOrder");
  const r = await reqWithRefresh(pre, "POST", `/api/v1/orders/${uid}/submit`, {
    json: { actionsHistory: ["submit_action"] },
  });
  if (r.status >= 400) throw new Error(`/submit failed ${r.status}: ${r.text.slice(0, 300)}`);
  await new Promise((resolve) => setTimeout(resolve, 1500));
  return verify(pre, uid);
}

// ---- shared date helpers --------------------------------------------------

export const pad = (n: number | string): string => String(n).padStart(2, "0");

// Normalize a date string to MM/DD/YYYY. Returns null on empty/unparseable input.
export function toMDY(input: string | null | undefined): string | null {
  if (!input) return null;
  const s = String(input).trim();
  const us = s.match(/^(\d{1,2})\/(\d{1,2})\/(\d{4})$/);
  if (us) return `${pad(us[1] ?? "")}/${pad(us[2] ?? "")}/${us[3]}`;
  const iso = s.match(/^(\d{4})-(\d{2})-(\d{2})$/);
  if (iso) return `${iso[2]}/${iso[3]}/${iso[1]}`;
  const d = new Date(s); // handles "Jun 15, 1955"
  if (!Number.isNaN(d.getTime()))
    return `${pad(d.getMonth() + 1)}/${pad(d.getDate())}/${d.getFullYear()}`;
  throw new Error(`unrecognized date ${s}`);
}
