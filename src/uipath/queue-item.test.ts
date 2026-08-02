import { describe, expect, test } from "bun:test";
import { decodeJwtId, resolveAutomationId, splitName, toMDY } from "./queue-item.js";

describe("toMDY", () => {
  test("zero-pads US M/D/YYYY", () => {
    expect(toMDY("1/2/2024")).toBe("01/02/2024");
    expect(toMDY("12/31/2024")).toBe("12/31/2024");
  });
  test("converts ISO YYYY-MM-DD to MM/DD/YYYY", () => {
    expect(toMDY("2024-03-04")).toBe("03/04/2024");
  });
  test("returns '' for empty/nullish/unparseable input", () => {
    expect(toMDY("")).toBe("");
    expect(toMDY(null)).toBe("");
    expect(toMDY(undefined)).toBe("");
    expect(toMDY("not a date")).toBe("");
  });
});

describe("splitName", () => {
  test("splits 'Last, First M' and uppercases", () => {
    expect(splitName("Doe, John A")).toEqual({ last: "DOE", first: "JOHN" });
  });
  test("treats a comma-less name as the last name", () => {
    expect(splitName("Madonna")).toEqual({ last: "MADONNA", first: "" });
  });
  test("handles undefined", () => {
    expect(splitName(undefined)).toEqual({ last: "", first: "" });
  });
});

describe("resolveAutomationId", () => {
  test("an explicit automationId always wins over the account table", () => {
    expect(resolveAutomationId("explicit-id", "kafri")).toBe("explicit-id");
  });
  test("resolves from a known account (profile), case-insensitively", () => {
    expect(resolveAutomationId(undefined, "kafri")).toBe("c741d791-3e3b-4102-b95c-f94549949c9a");
    expect(resolveAutomationId(undefined, "SCLC")).toBe("85619cc6-5e25-4219-ad12-fc9877aaf841");
  });
  test("returns undefined for an unknown/missing account and no explicit id", () => {
    expect(resolveAutomationId(undefined, "unknown-account")).toBeUndefined();
    expect(resolveAutomationId(undefined, null)).toBeUndefined();
    expect(resolveAutomationId("", null)).toBeUndefined();
  });
});

describe("decodeJwtId", () => {
  test("extracts a numeric id from a JWT payload", () => {
    const payload = Buffer.from(JSON.stringify({ id: 42 })).toString("base64");
    expect(decodeJwtId(`header.${payload}.sig`)).toBe(42);
  });
  test("extracts a string id", () => {
    const payload = Buffer.from(JSON.stringify({ id: "abc" })).toString("base64");
    expect(decodeJwtId(`header.${payload}.sig`)).toBe("abc");
  });
  test("returns null for malformed tokens or missing id", () => {
    expect(decodeJwtId(undefined)).toBeNull();
    expect(decodeJwtId("garbage")).toBeNull();
    const noId = Buffer.from(JSON.stringify({ sub: "x" })).toString("base64");
    expect(decodeJwtId(`h.${noId}.s`)).toBeNull();
  });
});
