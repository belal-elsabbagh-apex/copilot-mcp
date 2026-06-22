import { describe, expect, test } from "bun:test";
import { server } from "./server.js";

// The SDK keeps registered tools in a private map; each entry carries the Zod
// object schema built from the tool's inputSchema. We read it here only to assert
// the server wired up exactly the tools we expect with valid schemas — a cheap
// smoke test that catches a missing/duplicate registration or a malformed schema
// without standing up the stdio transport.
interface RegisteredTool {
  inputSchema?: { safeParse(value: unknown): { success: boolean } };
}
const registered = (server as unknown as { _registeredTools: Record<string, RegisteredTool> })
  ._registeredTools;

const EXPECTED = [
  "clone_order",
  "find_clone_candidates",
  "delete_preprod_order",
  "build_queue_item",
  "analyze_order_execution",
] as const;

describe("server tool registration", () => {
  test("registers exactly the expected tools", () => {
    expect(Object.keys(registered).sort()).toEqual([...EXPECTED].sort());
  });

  test("every tool exposes an input schema", () => {
    for (const name of EXPECTED) {
      expect(registered[name]?.inputSchema).toBeDefined();
    }
  });
});

describe("tool input schemas accept representative payloads", () => {
  const cases: Record<string, { valid: unknown; invalid?: unknown }> = {
    clone_order: { valid: { uids: ["abcdefgh"] }, invalid: {} },
    find_clone_candidates: { valid: {} },
    delete_preprod_order: { valid: { uids: ["abcdefgh"] }, invalid: { uids: [] } },
    build_queue_item: { valid: { orderUid: "abcdefgh" }, invalid: { orderUid: "short" } },
    analyze_order_execution: { valid: { orderUid: "abcdefgh" } },
  };

  for (const [name, c] of Object.entries(cases)) {
    test(`${name} accepts a valid payload`, () => {
      expect(registered[name]?.inputSchema?.safeParse(c.valid).success).toBe(true);
    });
    if (c.invalid !== undefined) {
      test(`${name} rejects an invalid payload`, () => {
        expect(registered[name]?.inputSchema?.safeParse(c.invalid).success).toBe(false);
      });
    }
  }
});
