import { describe, expect, test } from "bun:test";
import { server } from "./server.js";

// The SDK keeps registered tools in a private map; each entry carries the Zod
// object schema built from the tool's inputSchema. We read it here only to assert
// the server wired up exactly the tools we expect with valid schemas — a cheap
// smoke test that catches a missing/duplicate registration or a malformed schema
// without standing up the stdio transport.
interface RegisteredTool {
  inputSchema?: { safeParse(value: unknown): { success: boolean } };
  title?: string;
  description?: string;
  annotations?: {
    readOnlyHint?: boolean;
    destructiveHint?: boolean;
    idempotentHint?: boolean;
    openWorldHint?: boolean;
  };
}

// Tools that mutate state (everything else is read-only).
const WRITE_TOOLS = new Set(["clone_order", "delete_preprod_order"]);
// Tools that touch no external service (pure/local). All others set openWorldHint=true.
const CLOSED_WORLD_TOOLS = new Set(["list_setting_sections"]);
const registered = (server as unknown as { _registeredTools: Record<string, RegisteredTool> })
  ._registeredTools;

const EXPECTED = [
  "clone_order",
  "find_clone_candidates",
  "delete_preprod_order",
  "build_queue_item",
  "analyze_order_execution",
  "pull_queue_item",
  "list_queue_items",
  "list_jobs",
  "get_job_logs",
  "find_stuck_orders",
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

  test("every tool declares the full set of four annotation hints", () => {
    for (const name of EXPECTED) {
      const t = registered[name];
      expect(t?.title, `${name} title`).toBeTruthy();
      expect((t?.description?.length ?? 0) > 30, `${name} description`).toBe(true);
      const a = t?.annotations;
      expect(a, `${name} annotations`).toBeDefined();
      // all four hints present on every tool
      expect(typeof a?.readOnlyHint, `${name} readOnlyHint`).toBe("boolean");
      expect(typeof a?.destructiveHint, `${name} destructiveHint`).toBe("boolean");
      expect(typeof a?.idempotentHint, `${name} idempotentHint`).toBe("boolean");
      // openWorldHint must be present and accurate (most tools hit an external service)
      expect(typeof a?.openWorldHint, `${name} openWorldHint`).toBe("boolean");
      expect(a?.openWorldHint, `${name} openWorldHint value`).toBe(!CLOSED_WORLD_TOOLS.has(name));
      // read/write hint must match the known write-tool set
      expect(a?.readOnlyHint, `${name} readOnlyHint value`).toBe(!WRITE_TOOLS.has(name));
    }
  });

  test("read-only tools are non-destructive and idempotent", () => {
    for (const name of EXPECTED) {
      if (WRITE_TOOLS.has(name)) continue;
      const a = registered[name]?.annotations;
      expect(a?.destructiveHint, `${name} destructiveHint`).toBe(false);
      expect(a?.idempotentHint, `${name} idempotentHint`).toBe(true);
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
