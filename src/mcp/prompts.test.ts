import { describe, expect, test } from "bun:test";
import { server } from "../server.js";

// The SDK keeps prompts and resource templates in private maps, mirroring the tool
// registry asserted in server.test.ts. We read them here only to confirm the server
// wired up the workflow prompts and the per-portal resource template.
const prompts = (server as unknown as { _registeredPrompts: Record<string, unknown> })
  ._registeredPrompts;
const resourceTemplates = (
  server as unknown as { _registeredResourceTemplates: Record<string, unknown> }
)._registeredResourceTemplates;

describe("prompt + resource template registration", () => {
  test("registers exactly the workflow prompts", () => {
    expect(Object.keys(prompts).sort()).toEqual([
      "clone-and-verify-order",
      "diagnose-order",
      "reconcile-settings",
      "report-faulted-uipath-jobs",
      "triage-stuck-orders",
    ]);
  });

  test("registers the per-portal resource template", () => {
    expect(resourceTemplates["portal"]).toBeDefined();
  });
});
