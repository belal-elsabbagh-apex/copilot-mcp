// EHR Copilot BE HTTP client + shared order types/helpers.
//
// Ported natively (node/bun builtins + global fetch only — no external deps) from
// the legacy `.planning/copy_order_prod_to_preprod.mjs` harness. The order mirror
// (mirror.ts) and the UiPath queue-item builder (queue-item.ts) both import from here.

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
}
export interface BeSpeciality {
  specialityUid?: string;
  referredFacilities?: BeFacility[];
}
export interface BeOrder {
  orderUid?: string;
  status?: string;
  submissionStatus?: string;
  submissionReference?: string;
  patient?: { patientName?: string; patientBirthDate?: string; patientPhoneNumber?: string };
  insurance?: { name?: string; memberId?: string };
  orderType?: { typeUid?: string; name?: string };
  speciality?: { name?: string };
  referredFacility?: BeFacility;
  clinicProvider?: { clinicProviderUid?: string };
  orderNames?: { nameUid?: string }[];
  ICDCodes?: BeCode[];
  CPTCodes?: BeCode[];
  location?: string;
  uploadAuth?: boolean;
  uploadFax?: boolean;
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
  req(method: string, path: string, body?: ReqBody): Promise<HttpResponse>;
}

// Minimal cookie-jar fetch client (mirrors the BE's set-cookie session flow).
export function makeClient(base: string): HttpClient {
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
    let data: unknown;
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
    return { status: res.status, data, text };
  }
  return { base, req };
}

export async function login(c: HttpClient, email: string, password: string): Promise<void> {
  const r = await c.req("POST", "/api/v1/copilot/physician/login", { json: { email, password } });
  if (r.status >= 400) throw new Error(`login failed ${r.status}: ${r.text.slice(0, 300)}`);
}

// Pull a single order row out of an /orders/filter response (defensive about shape).
const firstOrder = (data: unknown): BeOrder | undefined => {
  const rows = (data as { data?: unknown } | null)?.data;
  return Array.isArray(rows) ? (rows[0] as BeOrder | undefined) : undefined;
};

// /orders/filter for one uid; throws if the order isn't found in this env.
export async function fetchOrder(c: HttpClient, uid: string): Promise<BeOrder> {
  const r = await c.req("POST", "/api/v1/orders/filter", {
    json: {
      searchUid: uid,
      pageSize: 30,
      pageNumber: 0,
      type: "Outbound Referral",
      orderMode: ORDER_MODE,
    },
  });
  if (r.status >= 400)
    throw new Error(`/orders/filter failed ${r.status}: ${r.text.slice(0, 300)}`);
  const o = firstOrder(r.data);
  if (!o) throw new Error(`order ${uid} not found in this env`);
  return o;
}

// Read-only status summary for an order (used by clone verify + analyze enrich).
export async function verify(c: HttpClient, uid: string): Promise<OrderVerify | null> {
  const r = await c.req("POST", "/api/v1/orders/filter", {
    json: {
      searchUid: uid,
      pageSize: 30,
      pageNumber: 0,
      type: "Outbound Referral",
      orderMode: ORDER_MODE,
    },
  });
  const o = firstOrder(r.data);
  if (!o) return null;
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
