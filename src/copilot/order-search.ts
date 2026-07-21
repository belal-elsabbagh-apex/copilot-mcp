// search_orders / get_order_category_stats domain logic: build the full
// /orders/filter request-body contract (reference: the copilot-orders-filter skill,
// ported from the OSSM referral-report kit) from a friendlier tool-input shape, and
// map results into slim, non-PHI rows. Read-only. Matches the find_stuck_orders /
// find_clone_candidates bulk-tool convention (no `patient` field), not get_order's
// full-detail-with-patient shape — this tool can return many rows per call.

import { type Env, resolveCreds } from "../config/config.js";
import {
  type BeOrder,
  categoryStats,
  filterOrders,
  login,
  makeClient,
  ORDER_MODE,
  type OrderFilterBody,
} from "./copilot-client.js";

// Every array-valued filter is sent to the BE as a JSON-encoded STRING, never a
// bare array (the single biggest /orders/filter gotcha). Omitting a dimension means
// "no filter" — matches the FE's own omission semantics (no filter, or "everything
// selected", both send no param), so an empty array is treated the same as omitted
// rather than "match nothing".
export interface OrderFilterDimensions {
  type?: string | undefined;
  locations?: string[] | undefined;
  insurances?: string[] | undefined;
  referredTo?: string[] | undefined;
  orderType?: string[] | undefined;
  authRequired?: string[] | undefined;
  authStatus?: string[] | undefined;
  uploadStatusAuth?: string[] | undefined;
  uploadStatusFax?: string[] | undefined;
  hasAuthScreenshot?: boolean | undefined;
  sendFax?: boolean | undefined;
  missingNotes?: boolean | undefined;
  mrn?: string | undefined;
  search?: string | undefined;
  fromDate?: string | undefined;
  toDate?: string | undefined;
  orderDateFrom?: string | undefined;
  orderDateTo?: string | undefined;
  appointmentDateFrom?: string | undefined;
  appointmentDateTo?: string | undefined;
  submissionDateFrom?: string | undefined;
  submissionDateTo?: string | undefined;
  notificationDateFrom?: string | undefined;
  notificationDateTo?: string | undefined;
  sort?: string | undefined;
}

export interface SearchOrdersArgs extends OrderFilterDimensions {
  env: Env;
  profile?: string | null | undefined;
  pageSize?: number | undefined;
  pageNumber?: number | undefined;
}

export interface CategoryStatsArgs extends OrderFilterDimensions {
  env: Env;
  profile?: string | null | undefined;
}

const ARRAY_FIELDS = [
  "locations",
  "insurances",
  "referredTo",
  "orderType",
  "authRequired",
  "authStatus",
  "uploadStatusAuth",
  "uploadStatusFax",
] as const;

const SCALAR_FIELDS = [
  "mrn",
  "search",
  "fromDate",
  "toDate",
  "orderDateFrom",
  "orderDateTo",
  "appointmentDateFrom",
  "appointmentDateTo",
  "submissionDateFrom",
  "submissionDateTo",
  "notificationDateFrom",
  "notificationDateTo",
  "sort",
  "hasAuthScreenshot",
  "sendFax",
  "missingNotes",
] as const;

// Pure: builds the /orders/category/stats dimensions (no paging) from friendlier
// array-of-strings inputs. Omits any dimension the caller didn't set. `type` and
// `orderMode` default to the same values every other tool in this codebase
// hardcodes (Outbound Referral / both order+pcp-notes modes) unless the caller
// overrides `type`.
export function buildFilterDimensions(
  args: OrderFilterDimensions,
): Omit<OrderFilterBody, "pageSize" | "pageNumber"> {
  const body: Record<string, unknown> = {
    type: args.type ?? "Outbound Referral",
    orderMode: ORDER_MODE,
  };
  for (const key of ARRAY_FIELDS) {
    const v = args[key];
    if (v && v.length > 0) body[key] = JSON.stringify(v);
  }
  for (const key of SCALAR_FIELDS) {
    const v = args[key];
    if (v !== undefined) body[key] = v;
  }
  return body as Omit<OrderFilterBody, "pageSize" | "pageNumber">;
}

// Pure: builds a full /orders/filter request body (dimensions + paging).
export function buildFilterBody(
  args: OrderFilterDimensions & { pageSize?: number | undefined; pageNumber?: number | undefined },
): OrderFilterBody {
  return {
    pageSize: args.pageSize ?? 100,
    pageNumber: args.pageNumber ?? 1,
    ...buildFilterDimensions(args),
  };
}

export interface SlimReferredTo {
  name?: string | undefined;
  npi?: string | undefined;
  external?: boolean | undefined;
}

export interface SlimOrderRow {
  orderUid?: string | undefined;
  creationDate?: string | undefined;
  status?: string | undefined;
  category?: string | undefined;
  orderType?: string | undefined;
  insurance?: string | undefined;
  speciality?: string | undefined;
  referredTo?: SlimReferredTo | undefined;
  authRequired?: boolean | undefined;
  authStatus?: string | undefined;
  uploadStatusAuth?: string | undefined;
  uploadStatusFax?: string | undefined;
}

// Pure mapping from a raw BE order row to the slim, non-PHI shape search_orders
// returns — no `patient` field. referredFacility and referredProvider are mutually
// exclusive on the BE row; prefer whichever is present.
export function slimOrderRow(o: BeOrder): SlimOrderRow {
  const rf = o.referredFacility;
  const rp = o.referredProvider;
  const referredTo: SlimReferredTo | undefined =
    rf || rp
      ? { name: rf?.name ?? rp?.name, npi: rf?.NPI ?? rf?.npi ?? rp?.NPI, external: rf?.external }
      : undefined;
  return {
    orderUid: o.orderUid,
    creationDate: o.creationDate,
    status: o.status,
    category: o.category,
    orderType: o.orderType?.name,
    insurance: o.insurance?.name,
    speciality: o.speciality?.name,
    referredTo,
    authRequired: o.requiredAuthorization,
    authStatus: o.authStatus,
    uploadStatusAuth: o.uploadStatusAuth,
    uploadStatusFax: o.uploadStatusFax,
  };
}

// Drop a row field when the caller's own filter already pins it to one value — repeating
// it back on every row of a 100-row page is pure restatement of the request, not new
// information. Left alone (not stripped) whenever the dimension is unfiltered or matches
// more than one value, since then per-row values can differ and are informative.
export function stripFilterEchoedFields<T extends SlimOrderRow>(
  row: T,
  args: Pick<OrderFilterDimensions, "insurances" | "orderType">,
): T {
  const out = { ...row };
  if (args.insurances?.length === 1) delete out.insurance;
  if (args.orderType?.length === 1) delete out.orderType;
  return out;
}

export interface SearchOrdersResult {
  env: Env;
  profile: string;
  pageSize: number;
  pageNumber: number;
  totalNumberOfElements?: number | undefined;
  count: number;
  rows: SlimOrderRow[];
}

export async function searchOrders(args: SearchOrdersArgs): Promise<SearchOrdersResult> {
  const creds = resolveCreds(args.profile ?? null)[args.env];
  const client = makeClient(creds.be, args.env);
  await login(client, creds.email, creds.password);
  const body = buildFilterBody(args);
  const { rows, total } = await filterOrders(client, body);
  return {
    env: args.env,
    profile: args.profile ?? "(default)",
    pageSize: body.pageSize,
    pageNumber: body.pageNumber,
    totalNumberOfElements: total,
    count: rows.length,
    rows: rows.map((o) => stripFilterEchoedFields(slimOrderRow(o), args)),
  };
}

export async function getOrderCategoryStats(
  args: CategoryStatsArgs,
): Promise<Record<string, unknown>> {
  const creds = resolveCreds(args.profile ?? null)[args.env];
  const client = makeClient(creds.be, args.env);
  await login(client, creds.email, creds.password);
  return categoryStats(client, buildFilterDimensions(args));
}
