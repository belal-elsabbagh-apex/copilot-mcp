// Surface an order's CDN documents (auth-screenshot PDF + medical-authorization
// summary PDF) as clickable links on get_order.
//
// The CDN (cdn.<env>.ehrcopilotbe.com) is CloudFront signed-cookie protected, so
// these URLs return 403 (MissingKey) to any plain request — they open only in a
// context that holds the signed cookies (the Copilot web app in the user's browser).
// We therefore NEVER fetch or existence-probe the CDN here; "exists" is decided from
// the BE's own signals: the order's hasAuthScreenshot flag, and the authoritative
// /medicalAuthorizations?orderUid= listing.

import type { BeOrder, HttpClient } from "./copilot-client.js";

export interface OrderDocument {
  kind: "authScreenshot" | "authSummary";
  url: string;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

// CDN origin + account slug, parsed from a CDN url the BE already stamped on the
// order (authScreenshotFileUrl is present even when the file is absent):
//   https://cdn.prod.ehrcopilotbe.com/ossm/orders/<uid>/screenshots/authScreen.pdf
//   -> { origin: "https://cdn.prod.ehrcopilotbe.com", account: "ossm" }
export function cdnContext(order: BeOrder): { origin: string; account: string } | null {
  for (const raw of [order.authScreenshotFileUrl, order.authorizationResultURL]) {
    const m = str(raw).match(/^(https?:\/\/[^/]+)\/([^/]+)\//);
    if (m) return { origin: m[1] ?? "", account: m[2] ?? "" };
  }
  return null;
}

// The CloudFront origin (https://cdn.<env>.ehrcopilotbe.com) derived from the BE
// base url (https://be.<env>.ehrcopilotbe.com): the CDN mirrors the BE host with the
// leading `be.` label swapped for `cdn.`. Returns null if base isn't a `be.` host.
export function cdnOriginFromBase(base: string): string | null {
  try {
    const u = new URL(base);
    if (!u.hostname.startsWith("be.")) return null;
    return `${u.protocol}//cdn.${u.hostname.slice("be.".length)}`;
  } catch {
    return null;
  }
}

// Resolve the CDN origin + account for an order. Prefer the DETERMINISTIC pairing —
// the env's CDN host (from the client base) + the account (the config profile) — so
// links resolve even for failed orders that carry no stamped CDN url. Fall back to
// parsing a url the BE stamped on the order (cdnContext).
export function resolveCdn(
  order: BeOrder,
  base?: string,
  account?: string,
): { origin: string; account: string } | null {
  const origin = base ? cdnOriginFromBase(base) : null;
  if (origin && account) return { origin, account };
  return cdnContext(order);
}

// Summary.pdf url for one medical-authorization entity: prefer a direct url the BE
// may already provide, else construct {origin}/{account}/medicalAuthorizations/{uid}/
// summary.pdf from the entity's uid + the order's CDN context. Returns null if neither
// is resolvable (so the caller just omits it rather than emitting a broken link).
export function summaryUrlFor(
  auth: Record<string, unknown>,
  cdn: { origin: string; account: string } | null,
): string | null {
  const direct =
    str(auth["summaryUrl"]) ||
    str(auth["summaryFileUrl"]) ||
    str(auth["summaryPdfUrl"]) ||
    str(auth["resultUrl"]);
  if (direct) return direct;
  const uid =
    str(auth["uid"]) ||
    str(auth["medicalAuthorizationUid"]) ||
    str(auth["medicalAuthorizationsUid"]) ||
    str(auth["id"]);
  if (cdn?.origin && cdn.account && uid) {
    return `${cdn.origin}/${cdn.account}/medicalAuthorizations/${uid}/summary.pdf`;
  }
  return null;
}

// Build the list of CDN document links that actually exist for an order. Never throws
// (a failed medicalAuthorizations lookup just yields no summary link).
export async function orderDocuments(
  c: HttpClient,
  order: BeOrder,
  orderUid: string,
  account?: string,
): Promise<OrderDocument[]> {
  const docs: OrderDocument[] = [];
  const cdn = resolveCdn(order, c.base, account);

  // 1) auth-screenshot PDF — hasAuthScreenshot is the BE's existence signal. Prefer
  //    the url the BE stamped, else construct the deterministic CDN path so the link
  //    still resolves when the order carries no stamped url.
  if (order.hasAuthScreenshot === true) {
    const shotUrl =
      str(order.authScreenshotFileUrl) ||
      (cdn ? `${cdn.origin}/${cdn.account}/orders/${orderUid}/screenshots/authScreen.pdf` : "");
    if (shotUrl) docs.push({ kind: "authScreenshot", url: shotUrl });
  }

  // 2) medical-authorization summary PDFs — the listing is the authoritative
  //    existence check (only real auths come back).
  try {
    const r = await c.req(
      "GET",
      `/api/v1/medicalAuthorizations?orderUid=${encodeURIComponent(orderUid)}`,
    );
    const list = (r.data as { data?: unknown[] } | undefined)?.data;
    if (Array.isArray(list)) {
      for (const a of list) {
        if (a && typeof a === "object") {
          const url = summaryUrlFor(a as Record<string, unknown>, cdn);
          if (url) docs.push({ kind: "authSummary", url });
        }
      }
    }
  } catch {
    // listing unavailable — fall through to the order-level fallback below.
  }

  // 3) fallback: order-level result url, if the listing yielded nothing.
  if (!docs.some((d) => d.kind === "authSummary")) {
    const resultUrl = str(order.authorizationResultURL);
    if (resultUrl) docs.push({ kind: "authSummary", url: resultUrl });
  }

  return docs;
}
