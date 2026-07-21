import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../mcp/feedback.js";
import { isNotFound, UiPathApiError } from "./errors.js";

describe("UiPathApiError", () => {
  test("carries structured method/url/status/body", () => {
    const e = new UiPathApiError("GET", "https://host/odata/Jobs(1)", 500, { message: "boom" });
    expect(e.method).toBe("GET");
    expect(e.url).toBe("https://host/odata/Jobs(1)");
    expect(e.status).toBe(500);
    expect(e.body).toEqual({ message: "boom" });
    expect(e.name).toBe("UiPathApiError");
  });

  test("message matches the legacy 'METHOD url -> status: body' shape", () => {
    const e = new UiPathApiError("POST", "/odata/Jobs", 404, "not found");
    expect(e.message).toBe("UiPath POST /odata/Jobs -> 404: not found");
  });

  test("stringifies a JSON body in the message and truncates long bodies", () => {
    const e1 = new UiPathApiError("GET", "/x", 500, { a: 1 });
    expect(e1.message).toContain('{"a":1}');

    const long = "x".repeat(400);
    const e2 = new UiPathApiError("GET", "/x", 500, long);
    expect(e2.message).toContain(`${"x".repeat(300)}…`);
    expect(e2.message.length).toBeLessThan(long.length + 50);
  });

  test("is an ExpectedError (classify() treats it as expected)", () => {
    const e = new UiPathApiError("GET", "/x", 500, "boom");
    expect(e).toBeInstanceOf(ExpectedError);
    expect(e).toBeInstanceOf(Error);
  });
});

describe("isNotFound", () => {
  test("true only for a UiPathApiError with status 404", () => {
    expect(isNotFound(new UiPathApiError("GET", "/x", 404, ""))).toBe(true);
  });

  test("false for a UiPathApiError with any other status", () => {
    expect(isNotFound(new UiPathApiError("GET", "/x", 401, ""))).toBe(false);
    expect(isNotFound(new UiPathApiError("GET", "/x", 500, ""))).toBe(false);
  });

  test("false for a plain Error or non-error value", () => {
    expect(isNotFound(new Error("404 not found"))).toBe(false);
    expect(isNotFound("not found")).toBe(false);
    expect(isNotFound(null)).toBe(false);
  });
});
