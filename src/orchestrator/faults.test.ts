import { describe, expect, test } from "bun:test";
import { faultSearchQuery, firstLine, normalizeError, signatureMarker } from "./faults.js";

describe("normalizeError", () => {
  test("two faults with different ids/timestamps collapse to the same signature", () => {
    const a = normalizeError(
      "Selector not found for order 998877 at 2026-06-30T10:00:30Z (job 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d)",
    );
    const b = normalizeError(
      "Selector not found for order 112233 at 2026-06-29T22:14:01Z (job 9f8e7d6c-5b4a-3210-fedc-ba9876543210)",
    );
    expect(a).toBe(b);
    expect(a).toContain("selector not found");
    expect(a).toContain("<id>");
    expect(a).toContain("<n>");
    expect(a).toContain("<ts>");
  });

  test("lowercases, drops quotes/backticks, collapses whitespace, truncates", () => {
    const s = normalizeError(`  Login   FAILED:  "bad creds"  \n\n with \`token\` `);
    expect(s).toBe("login failed: bad creds with token");
    expect(normalizeError("x".repeat(300)).length).toBe(160);
  });
});

describe("firstLine", () => {
  test("keeps only the first line, trimmed", () => {
    expect(firstLine("  boom  \nstack frame 1\nstack frame 2")).toBe("boom");
  });

  test("caps a long first line at 120 characters", () => {
    expect(firstLine("y".repeat(300))).toHaveLength(120);
  });
});

describe("signatureMarker / faultSearchQuery", () => {
  const signature = "selector not found for order <n>";

  test("the marker is the exact hidden-comment string the issue body embeds", () => {
    expect(signatureMarker(signature)).toBe(
      "<!-- fault-signature: selector not found for order <n> -->",
    );
  });

  test("the search query targets the repo, open issues, and the marker's phrase", () => {
    expect(faultSearchQuery("acme/repo", signature)).toBe(
      'repo:acme/repo is:issue is:open in:body "fault-signature: selector not found for order <n>"',
    );
  });

  test("the quoted search phrase occurs verbatim inside the marker, so a search finds its own issue", () => {
    const phrase = faultSearchQuery("acme/repo", signature).split('in:body "')[1]?.slice(0, -1);
    expect(phrase).toBeDefined();
    expect(signatureMarker(signature)).toContain(phrase ?? "");
  });
});
