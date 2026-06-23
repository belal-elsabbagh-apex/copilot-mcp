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

// Tools that mutate state (everything else is read-only). sync_settings is a stub but
// declares write semantics for its intended behavior.
const WRITE_TOOLS = new Set(["clone_order", "delete_preprod_order", "sync_settings"]);
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
  "diff_settings",
  "list_setting_sections",
  "sync_settings",
  "get_order",
  "doctor",
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
    // profile is required (any non-empty string; validated against config at call time).
    // env is required on every env-taking tool. Invalid cases omit a required field or
    // use a wrong type.
    clone_order: {
      valid: { uids: ["abcdefgh"], profile: "ossm" },
      invalid: { uids: ["abcdefgh"] },
    },
    find_clone_candidates: { valid: { profile: "ossm" }, invalid: {} },
    delete_preprod_order: {
      valid: { uids: ["abcdefgh"], profile: "ossm" },
      invalid: { uids: [], profile: "ossm" },
    },
    build_queue_item: {
      valid: { orderUid: "abcdefgh", env: "pre_prod", profile: "ossm" },
      invalid: { orderUid: "abcdefgh", profile: "ossm" }, // env is required
    },
    analyze_order_execution: {
      valid: { orderUid: "abcdefgh", env: "prod", profile: "ossm" },
      invalid: { orderUid: "abcdefgh", profile: "ossm" }, // env is required
    },
    diff_settings: { valid: { groups: ["orders"], profile: "ossm" }, invalid: { profile: 123 } },
    list_setting_sections: { valid: { group: "orders" }, invalid: { group: 123 } },
    sync_settings: { valid: { groups: ["orders"], profile: "ossm" }, invalid: { profile: 123 } },
    get_order: {
      valid: { orderUid: "abcdefgh", env: "prod", profile: "ossm" },
      invalid: { orderUid: "abcdefgh", env: "prod" }, // profile is required
    },
    doctor: { valid: { profile: "ossm" }, invalid: {} }, // profile is required
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
