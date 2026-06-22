import { describe, expect, test } from "bun:test";
import { envelopeRows, isRecord, prop, stringProp } from "./util.js";

describe("isRecord", () => {
  test("accepts plain objects", () => {
    expect(isRecord({})).toBe(true);
    expect(isRecord({ a: 1 })).toBe(true);
  });
  test("rejects arrays, null, and primitives", () => {
    expect(isRecord([])).toBe(false);
    expect(isRecord(null)).toBe(false);
    expect(isRecord(undefined)).toBe(false);
    expect(isRecord("x")).toBe(false);
    expect(isRecord(3)).toBe(false);
  });
});

describe("prop", () => {
  test("reads a key off an object", () => {
    expect(prop({ a: 1 }, "a")).toBe(1);
  });
  test("returns undefined for missing keys or non-objects", () => {
    expect(prop({ a: 1 }, "b")).toBeUndefined();
    expect(prop(null, "a")).toBeUndefined();
    expect(prop([1, 2], "0")).toBeUndefined();
  });
});

describe("envelopeRows", () => {
  test("pulls the rows out of { data: [...] }", () => {
    expect(envelopeRows({ data: [1, 2, 3] })).toEqual([1, 2, 3]);
  });
  test("returns [] when data is missing or not an array", () => {
    expect(envelopeRows({})).toEqual([]);
    expect(envelopeRows({ data: "nope" })).toEqual([]);
    expect(envelopeRows(null)).toEqual([]);
  });
});

describe("stringProp", () => {
  test("returns the value only when it is a string", () => {
    expect(stringProp({ msg: "hi" }, "msg")).toBe("hi");
    expect(stringProp({ msg: 5 }, "msg")).toBeUndefined();
    expect(stringProp({}, "msg")).toBeUndefined();
    expect(stringProp(null, "msg")).toBeUndefined();
  });
});
