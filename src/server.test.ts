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

// Tools that mutate state (everything else is read-only). apply_settings_sync writes
// additively to pre-prod only; the UiPath writes (add_queue_item, delete_queue_item,
// start_job) are schema-restricted to the pre_prod dev clone.
const WRITE_TOOLS = new Set([
  "clone_order",
  "delete_preprod_order",
  "apply_settings_sync",
  "add_queue_item",
  "delete_queue_item",
  "start_job",
]);
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
  "build_faulted_job_issue",
  "find_stuck_orders",
  "diff_settings",
  "list_setting_sections",
  "get_settings",
  "plan_settings_sync",
  "apply_settings_sync",
  "get_order",
  "get_login_token",
  "doctor",
  "list_queues",
  "list_processes",
  "list_triggers",
  "get_job",
  "add_queue_item",
  "delete_queue_item",
  "start_job",
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
    get_settings: {
      valid: { env: "pre_prod", profile: "ossm", groups: ["orders"] },
      invalid: { profile: "ossm" }, // env is required
    },
    plan_settings_sync: {
      valid: { profile: "ossm", groups: ["orders"] },
      invalid: {}, // profile is required
    },
    apply_settings_sync: {
      // The actionIds-XOR-all rule is enforced at runtime (ExpectedError), not in the schema.
      valid: { profile: "ossm", actionIds: ["specialties:create:MRI:Cardiology"] },
      invalid: { actionIds: ["specialties:create:MRI:Cardiology"] }, // profile is required
    },
    get_order: {
      valid: { orderUid: "abcdefgh", env: "prod", profile: "ossm" },
      invalid: { orderUid: "abcdefgh", env: "prod" }, // profile is required
    },
    get_login_token: {
      valid: { env: "prod", profile: "ossm" },
      invalid: { profile: "ossm" }, // env is required
    },
    doctor: { valid: { profile: "ossm" }, invalid: {} }, // profile is required
    build_faulted_job_issue: {
      valid: { env: "prod", jobKey: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" },
      invalid: { jobKey: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" }, // env is required
    },
    list_jobs: {
      valid: { env: "prod", processName: "OPTUM" },
      invalid: { processName: "OPTUM" }, // env is required
    },
    list_queues: {
      valid: { env: "pre_prod", nameContains: "auth" },
      invalid: { nameContains: "auth" }, // env is required
    },
    list_processes: {
      valid: { env: "pre_prod" },
      invalid: {}, // env is required
    },
    list_triggers: {
      valid: { env: "pre_prod", queueDefinitionId: 79926 },
      invalid: { queueDefinitionId: 79926 }, // env is required
    },
    get_job: {
      valid: { env: "pre_prod", jobKey: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" },
      invalid: { jobKey: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" }, // env is required
    },
    add_queue_item: {
      valid: {
        env: "pre_prod",
        queueName: "Scratch queue",
        reference: "TEST-1-abcd1234",
        specificContent: { orderUid: "abcd1234" },
      },
      // env is a z.literal("pre_prod") — prod must be rejected at the schema layer
      invalid: {
        env: "prod",
        queueName: "Scratch queue",
        reference: "TEST-1-abcd1234",
        specificContent: { orderUid: "abcd1234" },
      },
    },
    delete_queue_item: {
      valid: { env: "pre_prod", itemId: 123 },
      invalid: { env: "prod", itemId: 123 }, // prod rejected by the schema literal
    },
    start_job: {
      valid: { env: "pre_prod", releaseKey: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5" },
      invalid: { releaseKey: "609b3602-b6f2-44b2-9ee2-6a8988fac1f5" }, // env is required
    },
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
