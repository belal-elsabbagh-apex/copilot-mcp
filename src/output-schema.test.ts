import { describe, expect, test } from "bun:test";
import { normalizeOutput, outputMatchesOrder } from "./output-schema.js";

describe("normalizeOutput — jobOutput (flat out_* schema)", () => {
  const raw = {
    out_Result: "Success",
    out_OrderUid: "order-123",
    out_Account: "ossm",
    unrelated: "ignored",
  };
  test("detects the schema and extracts the order uid", () => {
    const n = normalizeOutput(raw);
    expect(n.schema).toBe("jobOutput");
    expect(n.orderUid).toBe("order-123");
  });
  test("keeps only out_* fields, priority-ordered then alphabetical", () => {
    const keys = normalizeOutput(raw).fields.map(([k]) => k);
    expect(keys).toEqual(["out_OrderUid", "out_Result", "out_Account"]);
  });
});

describe("normalizeOutput — transactionItem schema", () => {
  const raw = {
    transactionItem: {
      SpecificContent: {
        orderUid: "order-999",
        MemberID: "M1",
        token: "secret-jwt",
        callbackContext: "{}",
      },
    },
  };
  test("detects the schema and extracts the order uid", () => {
    const n = normalizeOutput(raw);
    expect(n.schema).toBe("transactionItem");
    expect(n.orderUid).toBe("order-999");
  });
  test("hides token and callbackContext", () => {
    const keys = normalizeOutput(raw).fields.map(([k]) => k);
    expect(keys).toContain("orderUid");
    expect(keys).toContain("MemberID");
    expect(keys).not.toContain("token");
    expect(keys).not.toContain("callbackContext");
  });
});

describe("normalizeOutput — unknown schema", () => {
  test("falls back to raw entries with empty orderUid", () => {
    const n = normalizeOutput({ foo: "bar" });
    expect(n.schema).toBe("unknown");
    expect(n.orderUid).toBe("");
    expect(n.fields).toEqual([["foo", "bar"]]);
  });
});

describe("outputMatchesOrder", () => {
  test("matches on the normalized order uid", () => {
    const raw = { out_OrderUid: "order-123" };
    expect(outputMatchesOrder(raw, "order-123")).toBe(true);
    expect(outputMatchesOrder(raw, "order-456")).toBe(false);
  });
});
