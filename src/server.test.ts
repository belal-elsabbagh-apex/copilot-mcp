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
// additively to pre-prod only; the orchestrator writes (add_queue_item, delete_queue_item,
// start_job, stop_job) are schema-restricted to pre_prod.
const WRITE_TOOLS = new Set([
  "delete_preprod_order",
  "create_preprod_order",
  "submit_preprod_order",
  "apply_settings_sync",
  "add_queue_item",
  "delete_queue_item",
  "start_job",
  "stop_job",
]);
// Tools that touch no external service (pure/local). All others set openWorldHint=true.
const CLOSED_WORLD_TOOLS = new Set(["list_setting_sections", "build_mcp_issue"]);
const registered = (server as unknown as { _registeredTools: Record<string, RegisteredTool> })
  ._registeredTools;

const EXPECTED = [
  "find_clone_candidates",
  "delete_preprod_order",
  "create_preprod_order",
  "submit_preprod_order",
  "build_queue_item",
  "find_order_queue_items",
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
  "search_orders",
  "get_order_category_stats",
  "get_login_token",
  "list_clinics",
  "doctor",
  "list_queues",
  "list_processes",
  "list_triggers",
  "get_job",
  "get_job_artifacts",
  "add_queue_item",
  "delete_queue_item",
  "start_job",
  "stop_job",
  "build_mcp_issue",
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

const JOB_ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const PROCESS_ID = "609b3602-b6f2-44b2-9ee2-6a8988fac1f5";

const cases: Record<string, { valid: unknown; invalid?: unknown }> = {
  // profile is required (any non-empty string; validated against config at call time).
  // env is required on every env-taking tool. Invalid cases omit a required field or
  // use a wrong type.
  find_clone_candidates: { valid: { profile: "ossm" }, invalid: {} },
  delete_preprod_order: {
    valid: { uids: ["abcdefgh"], profile: "ossm" },
    invalid: { uids: [], profile: "ossm" },
  },
  create_preprod_order: {
    valid: {
      profile: "ossm",
      patientName: "DOE, JOHN",
      patientBirthDate: "01/02/1980",
      insuranceName: "Acme Health",
      typeUid: "abcdefgh-type",
      orderNamesUids: ["abcdefgh-name"],
      icdCodes: [{ code: "M54.5" }],
    },
    invalid: {
      // profile is required
      patientName: "DOE, JOHN",
      patientBirthDate: "01/02/1980",
      insuranceName: "Acme Health",
      typeUid: "abcdefgh-type",
      orderNamesUids: ["abcdefgh-name"],
    },
  },
  submit_preprod_order: {
    valid: { profile: "ossm", orderUid: "abcdefgh" },
    invalid: { orderUid: "abcdefgh" }, // profile is required
  },
  build_queue_item: {
    valid: { orderUid: "abcdefgh", env: "pre_prod", profile: "ossm", orchestrator: "uipath" },
    invalid: { orderUid: "abcdefgh", env: "pre_prod", profile: "ossm" }, // orchestrator is required
  },
  find_order_queue_items: {
    valid: { orderUid: "abcdefgh", env: "prod" }, // orchestrator omitted = every orchestrator
    invalid: { orderUid: "abcdefgh" }, // env is required
  },
  pull_queue_item: {
    valid: { orchestrator: "uipath", env: "pre_prod", itemId: "123" },
    invalid: { orchestrator: "uipath", url: "not a url" },
  },
  list_queue_items: {
    valid: { env: "pre_prod", orchestrator: "uipath", queue: "PMG", status: "New" },
    invalid: { env: "pre_prod", orchestrator: "uipath", queue: "PMG", status: "Bogus" },
  },
  diff_settings: { valid: { tags: ["orders"], profile: "ossm" }, invalid: { profile: 123 } },
  list_setting_sections: { valid: { tag: "orders" }, invalid: { tag: 123 } },
  get_settings: {
    valid: { env: "pre_prod", profile: "ossm", tags: ["orders"] },
    invalid: { profile: "ossm" }, // env is required
  },
  plan_settings_sync: {
    valid: { profile: "ossm", tags: ["orders"] },
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
  search_orders: {
    valid: { env: "prod", profile: "ossm", insurances: ["OPTUM CARE NETWORK"] },
    invalid: { profile: "ossm" }, // env is required
  },
  get_order_category_stats: {
    valid: { env: "prod", profile: "ossm" },
    invalid: { profile: "ossm" }, // env is required
  },
  get_login_token: {
    valid: { env: "prod", profile: "ossm" },
    invalid: { profile: "ossm" }, // env is required
  },
  list_clinics: {
    valid: { env: "prod" },
    invalid: {}, // env is required
  },
  doctor: { valid: { profile: "ossm" }, invalid: {} }, // profile is required
  build_faulted_job_issue: {
    valid: { env: "prod", orchestrator: "uipath", jobId: JOB_ID },
    invalid: { env: "prod", jobId: JOB_ID }, // orchestrator is required
  },
  list_jobs: {
    valid: { env: "prod", orchestrator: "uipath", processName: "OPTUM" },
    invalid: { env: "prod", processName: "OPTUM" }, // orchestrator is required
  },
  get_job_logs: {
    // The jobId-XOR-jobIds rule is enforced at runtime (ExpectedError), not in the schema.
    valid: { env: "prod", orchestrator: "uipath", jobIds: [JOB_ID, JOB_ID] },
    invalid: { env: "prod", orchestrator: "uipath", jobIds: Array(26).fill(JOB_ID) }, // max 25
  },
  list_queues: {
    valid: { env: "pre_prod", orchestrator: "uipath", nameContains: "auth" },
    invalid: { env: "pre_prod", nameContains: "auth" }, // orchestrator is required
  },
  list_processes: {
    valid: { env: "pre_prod", orchestrator: "uipath" },
    invalid: { env: "pre_prod" }, // orchestrator is required
  },
  list_triggers: {
    valid: { env: "pre_prod", orchestrator: "uipath", queueId: "79926" },
    invalid: { env: "pre_prod", queueId: "79926" }, // orchestrator is required
  },
  get_job: {
    valid: { env: "pre_prod", orchestrator: "uipath", jobId: JOB_ID },
    invalid: { env: "pre_prod", jobId: JOB_ID }, // orchestrator is required
  },
  get_job_artifacts: {
    valid: {
      env: "pre_prod",
      jobId: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
      download: ["job.log", "trace.zip"],
    },
    // `download` is "all" or a non-empty filename list — any other string is rejected
    invalid: {
      env: "pre_prod",
      jobId: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
      download: "everything",
    },
  },
  add_queue_item: {
    valid: {
      env: "pre_prod",
      orchestrator: "uipath",
      queue: "Scratch queue",
      reference: "TEST-1-abcd1234",
      specificContent: { orderUid: "abcd1234" },
    },
    // env is a z.literal("pre_prod") — prod must be rejected at the schema layer
    invalid: {
      env: "prod",
      orchestrator: "uipath",
      queue: "Scratch queue",
      reference: "TEST-1-abcd1234",
      specificContent: { orderUid: "abcd1234" },
    },
  },
  delete_queue_item: {
    valid: { env: "pre_prod", orchestrator: "uipath", itemId: "123" },
    invalid: { env: "prod", orchestrator: "uipath", itemId: "123" }, // prod rejected by the schema literal
  },
  start_job: {
    valid: { env: "pre_prod", orchestrator: "uipath", processId: PROCESS_ID },
    invalid: { env: "prod", orchestrator: "uipath", processId: PROCESS_ID }, // prod rejected by the schema literal
  },
  stop_job: {
    valid: { env: "pre_prod", orchestrator: "uipath", jobId: JOB_ID, strategy: "Kill" },
    invalid: { env: "prod", orchestrator: "uipath", jobId: JOB_ID }, // prod rejected by the schema literal
  },
  build_mcp_issue: {
    valid: {
      kind: "feedback",
      title: "clone_order output is hard to scan",
      details: "The per-order rows mix successes and failures; a split summary would read better.",
    },
    invalid: { kind: "complaint", title: "x", details: "too short" },
  },
};

describe("tool input schemas accept representative payloads", () => {
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

// Every tool that dispatches through backendFor() must name its orchestrator, and only an
// enabled one. Derived from the cases above so a new orchestrator tool can't skip the check.
describe("orchestrator argument", () => {
  const ORCHESTRATOR_TOOLS = [
    "build_queue_item",
    "find_order_queue_items",
    "pull_queue_item",
    "list_queue_items",
    "list_jobs",
    "get_job_logs",
    "build_faulted_job_issue",
    "list_queues",
    "list_processes",
    "list_triggers",
    "get_job",
    "add_queue_item",
    "delete_queue_item",
    "start_job",
    "stop_job",
  ] as const;

  for (const name of ORCHESTRATOR_TOOLS) {
    test(`${name} rejects an unknown orchestrator`, () => {
      const valid = (cases[name]?.valid ?? {}) as Record<string, unknown>;
      const schema = registered[name]?.inputSchema;
      expect(schema?.safeParse({ ...valid, orchestrator: "uipath" }).success).toBe(true);
      expect(schema?.safeParse({ ...valid, orchestrator: "bogus" }).success).toBe(false);
    });
  }
});
