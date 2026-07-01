import { describe, expect, test } from "bun:test";
import type { BeOrder } from "./copilot-client.js";
import { cdnContext, summaryUrlFor } from "./order-docs.js";

describe("cdnContext", () => {
  test("parses origin + account from authScreenshotFileUrl", () => {
    const o = {
      authScreenshotFileUrl:
        "https://cdn.prod.ehrcopilotbe.com/ossm/orders/a1f504e2/screenshots/authScreen.pdf",
    } as BeOrder;
    expect(cdnContext(o)).toEqual({ origin: "https://cdn.prod.ehrcopilotbe.com", account: "ossm" });
  });

  test("falls back to authorizationResultURL", () => {
    const o = {
      authorizationResultURL:
        "https://cdn.prod.ehrcopilotbe.com/kafri/medicalAuthorizations/x/summary.pdf",
    } as BeOrder;
    expect(cdnContext(o)).toEqual({
      origin: "https://cdn.prod.ehrcopilotbe.com",
      account: "kafri",
    });
  });

  test("returns null when no CDN url is present", () => {
    expect(cdnContext({} as BeOrder)).toBeNull();
  });
});

describe("summaryUrlFor", () => {
  const cdn = { origin: "https://cdn.prod.ehrcopilotbe.com", account: "ossm" };

  test("constructs from a uid + cdn context", () => {
    expect(summaryUrlFor({ uid: "0d26bf01" }, cdn)).toBe(
      "https://cdn.prod.ehrcopilotbe.com/ossm/medicalAuthorizations/0d26bf01/summary.pdf",
    );
  });

  test("accepts alternate uid field names", () => {
    expect(summaryUrlFor({ medicalAuthorizationUid: "abc" }, cdn)).toContain(
      "/medicalAuthorizations/abc/summary.pdf",
    );
    expect(summaryUrlFor({ id: "zzz" }, cdn)).toContain("/medicalAuthorizations/zzz/summary.pdf");
  });

  test("prefers a direct url field over constructing", () => {
    expect(summaryUrlFor({ uid: "x", summaryUrl: "https://cdn/x/summary.pdf" }, cdn)).toBe(
      "https://cdn/x/summary.pdf",
    );
  });

  test("returns null when neither a url nor a uid+cdn is resolvable", () => {
    expect(summaryUrlFor({ uid: "x" }, null)).toBeNull();
    expect(summaryUrlFor({}, cdn)).toBeNull();
  });
});
