import { describe, expect, test } from "bun:test";
import {
  DEFAULT_RATE_LIMIT_RETRY_MS,
  MAX_RATE_LIMIT_RETRY_MS,
  rateLimitDelayMs,
  sleep,
} from "./http.js";

describe("rateLimitDelayMs", () => {
  test("honors a Retry-After given in seconds", () => {
    expect(rateLimitDelayMs("7")).toBe(7_000);
    expect(rateLimitDelayMs("0")).toBe(0);
  });

  test("falls back to the default when the header is missing, negative or not a number", () => {
    expect(rateLimitDelayMs(null)).toBe(DEFAULT_RATE_LIMIT_RETRY_MS);
    expect(rateLimitDelayMs("-3")).toBe(DEFAULT_RATE_LIMIT_RETRY_MS);
    expect(rateLimitDelayMs("soon")).toBe(DEFAULT_RATE_LIMIT_RETRY_MS);
  });

  test("clamps to one rate-limit window", () => {
    expect(rateLimitDelayMs("999")).toBe(MAX_RATE_LIMIT_RETRY_MS);
  });
});

describe("sleep", () => {
  test("resolves after the delay", async () => {
    const start = Date.now();
    await sleep(20);
    expect(Date.now() - start).toBeGreaterThanOrEqual(15);
  });
});
