// UiPath jobs surface their result as an `OutputArguments` JSON string, but the
// shape varies by automation. This module normalizes every known shape behind a
// single interface so order-UID matching and field rendering work uniformly.
//
// Ported from copilot-doctor src/outputSchema.ts. To support a new shape: write
// an OutputAdapter and register it in ADAPTERS — matching + display both read the
// normalized view.

import { isRecord } from "../shared/util.js";

export type OutputSchemaId = "jobOutput" | "transactionItem" | "unknown";

export interface NormalizedOutput {
  schema: OutputSchemaId;
  orderUid: string;
  fields: [string, unknown][];
  raw: Record<string, unknown>;
}

const asRecord = (v: unknown): Record<string, unknown> | null => (isRecord(v) ? v : null);
const asString = (v: unknown): string => (typeof v === "string" ? v : "");

interface OutputAdapter {
  id: OutputSchemaId;
  matches(raw: Record<string, unknown>): boolean;
  orderUid(raw: Record<string, unknown>): string;
  fields(raw: Record<string, unknown>): [string, unknown][];
}

// Schema A — flat job output keyed by `out_`-prefixed fields (e.g. out_OrderUid).
const JOB_OUTPUT_ORDER = [
  "out_OrderUid",
  "out_Result",
  "out_Account",
  "out_QueueItemReference",
  "out_AuthId",
];
const jobOutputAdapter: OutputAdapter = {
  id: "jobOutput",
  matches: (raw) => Object.keys(raw).some((k) => k.startsWith("out_")),
  orderUid: (raw) => asString(raw["out_OrderUid"]),
  fields: (raw) =>
    Object.entries(raw)
      .filter(([k]) => k.startsWith("out_"))
      .sort(([a], [b]) => {
        const ia = JOB_OUTPUT_ORDER.indexOf(a);
        const ib = JOB_OUTPUT_ORDER.indexOf(b);
        if (ia !== -1 && ib !== -1) return ia - ib;
        if (ia !== -1) return -1;
        if (ib !== -1) return 1;
        return a.localeCompare(b);
      }),
};

// Schema B — a UiPath queue transaction item; order data lives under
// transactionItem.SpecificContent. token/callbackContext carry an auth JWT and
// internal routing data, so they are hidden.
const TX_HIDDEN_FIELDS = new Set(["token", "callbackContext"]);
const specificContent = (raw: Record<string, unknown>): Record<string, unknown> | null =>
  asRecord(asRecord(raw["transactionItem"])?.["SpecificContent"]);
const transactionItemAdapter: OutputAdapter = {
  id: "transactionItem",
  matches: (raw) => specificContent(raw) !== null,
  orderUid: (raw) => asString(specificContent(raw)?.["orderUid"]),
  fields: (raw) =>
    Object.entries(specificContent(raw) ?? {}).filter(([k]) => !TX_HIDDEN_FIELDS.has(k)),
};

const ADAPTERS: OutputAdapter[] = [jobOutputAdapter, transactionItemAdapter];

export function normalizeOutput(raw: Record<string, unknown>): NormalizedOutput {
  for (const adapter of ADAPTERS) {
    if (adapter.matches(raw)) {
      return {
        schema: adapter.id,
        orderUid: adapter.orderUid(raw),
        fields: adapter.fields(raw),
        raw,
      };
    }
  }
  return { schema: "unknown", orderUid: "", fields: Object.entries(raw), raw };
}

export const outputMatchesOrder = (raw: Record<string, unknown>, orderId: string): boolean =>
  normalizeOutput(raw).orderUid === orderId;
