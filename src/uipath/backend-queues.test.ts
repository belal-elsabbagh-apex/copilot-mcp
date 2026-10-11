import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: { sessionCache: false, prod: envCreds("prod"), pre_prod: envCreds("preprod") },
  uipath: {
    orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
    bearer: "test-bearer",
  },
};

let dir: string;
let prevConfig: string | undefined;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-backend-queues-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeFileSync(join(dir, "config.json"), JSON.stringify(FIXTURE));
  resetConfigCache();
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

// One handler per test decides each request's response from its URL.
let handler: (url: URL) => Response;
let calls: URL[];
const realFetch = globalThis.fetch;

beforeEach(() => {
  calls = [];
  handler = () => json({ value: [] });
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = new URL(String(input));
    calls.push(url);
    return handler(url);
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

import { resolvePortal } from "../mcp/reference.js";
import { ExpectedError } from "../shared/expected-error.js";
import {
  uipathFindOrderQueueItems,
  uipathListProcesses,
  uipathListQueueItems,
  uipathListQueues,
  uipathListTriggers,
  uipathPullQueueItem,
} from "./backend.js";

const ORDER_UID = "test-order-uid-1";
const PMG = resolvePortal("PMG");

// Exactly the fields uipath.ts's QUEUE_ITEM_SELECT projects for — the tier-1 query's $select.
const QUEUE_ITEM_SELECT_FIELDS =
  "Id,Reference,Status,ExecutorJobKey,ProcessingExceptionType,RetryNumber,CreationTime,SpecificContent,QueueDefinitionId";

const queueItemRaw = (overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  Id: 1,
  Reference: "REF-1",
  Status: "New",
  ExecutorJobKey: "",
  ProcessingExceptionType: "",
  RetryNumber: 0,
  CreationTime: "2026-08-26T00:00:00.000Z",
  SpecificContent: {
    orderUid: ORDER_UID,
    token: "secret-jwt",
    MemberID: "m1",
    MemberFullName: "Doe, Jane",
  },
  QueueDefinitionId: PMG.submitQueueDefId,
  QueueDefinition: { Name: "PMG auth submit queue" },
  ...overrides,
});

const queueItemCalls = (): URL[] => calls.filter((u) => u.pathname.endsWith("/odata/QueueItems"));

describe("uipathListQueueItems", () => {
  const base = { env: "prod" as const, queueKind: "submit" as const, top: 10 };
  const filterOf = (u: URL | undefined): string => u?.searchParams.get("$filter") ?? "";

  test("a portal key resolves to its submit queue; sync kind picks the sync queue", async () => {
    handler = () => json({ value: [queueItemRaw()] });
    const submit = await uipathListQueueItems({ ...base, queue: "PMG" });
    expect(filterOf(queueItemCalls()[0])).toContain(`QueueDefinitionId eq ${PMG.submitQueueDefId}`);
    expect(submit).toMatchObject({ queueId: String(PMG.submitQueueDefId), queueName: "PMG" });

    await uipathListQueueItems({ ...base, queue: "pmg", queueKind: "sync" });
    expect(filterOf(queueItemCalls()[1])).toContain(`QueueDefinitionId eq ${PMG.syncQueueDefId}`);
  });

  test("a numeric queue id is used as-is (dev-clone queues); unregistered names come from the item", async () => {
    handler = () => json({ value: [queueItemRaw({ QueueDefinitionId: 999 })] });
    const res = await uipathListQueueItems({ ...base, queue: "999" });
    expect(filterOf(queueItemCalls()[0])).toContain("QueueDefinitionId eq 999");
    expect(res.queueId).toBe("999");
    expect(res.queueName).toBe("PMG");
  });

  test("an unknown portal key is an ExpectedError that lists the known portals", async () => {
    const err = await uipathListQueueItems({ ...base, queue: "NOPE" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("PMG");
    expect(calls).toHaveLength(0);
  });

  test("a status filter reaches the query; items are PHI-light cores without specificContent", async () => {
    handler = () => json({ value: [queueItemRaw({ Status: "Failed", RetryNumber: 2 })] });
    const res = await uipathListQueueItems({ ...base, queue: "PMG", status: "Failed" });
    expect(filterOf(queueItemCalls()[0])).toContain("Status eq 'Failed'");
    expect(res.items[0]).toMatchObject({
      orchestrator: "uipath",
      itemId: "1",
      reference: "REF-1",
      orderUid: ORDER_UID,
      status: "Failed",
      specificContent: null,
      details: { retryNumber: 2, memberId: "m1", member: "Doe, Jane" },
    });
    expect(JSON.stringify(res)).not.toContain("secret-jwt");
  });
});

describe("uipathFindOrderQueueItems", () => {
  const base = { env: "prod" as const, orderUid: ORDER_UID, top: 50, includeSpecificContent: true };

  test("tier-1 query is time-bounded ($filter) and projected ($select)", async () => {
    handler = () => json({ value: [queueItemRaw()] });
    await uipathFindOrderQueueItems({ ...base, since: "2026-08-25" });
    expect(queueItemCalls()).toHaveLength(1);
    const params = queueItemCalls()[0]?.searchParams;
    expect(params?.get("$select")).toBe(QUEUE_ITEM_SELECT_FIELDS);
    expect(params?.get("$filter")).toContain("contains(SpecificData,");
    expect(params?.get("$filter")).toContain("CreationTime gt 2026-08-25T00:00:00.000Z");
  });

  test("a since-bounded search that finds nothing retries unbounded and says so", async () => {
    handler = () => json({ value: [] });
    const res = await uipathFindOrderQueueItems({ ...base, since: "2026-08-25" });
    expect(queueItemCalls()).toHaveLength(2);
    expect(queueItemCalls()[1]?.searchParams.get("$filter")).not.toContain("CreationTime");
    expect(res.notes.some((n) => n.includes("retried without the time bound"))).toBe(true);
  });

  test("items map to cores: job attempts only for picked-up items, exception classified", async () => {
    handler = () =>
      json({
        value: [
          queueItemRaw({
            Id: 1,
            ExecutorJobKey: "k1",
            Status: "Failed",
            ProcessingExceptionType: "BusinessException",
          }),
          queueItemRaw({ Id: 2, ExecutorJobKey: "" }),
        ],
      });
    const res = await uipathFindOrderQueueItems(base);
    expect(res.searchMode).toBe("contains");
    expect(res.scanned).toBe(2);
    expect(res.items[0]?.jobs).toEqual([
      { orchestrator: "uipath", id: "k1", state: null, neverStarted: null },
    ]);
    expect(res.items[0]?.exceptionType).toBe("business");
    expect(res.items[0]?.queueName).toBe("PMG");
    expect(res.items[1]?.jobs).toEqual([]);
    expect(res.items[1]?.exceptionType).toBeNull();
  });

  test("specificContent is returned with the token redacted", async () => {
    handler = () =>
      json({
        value: [
          queueItemRaw({
            SpecificContent: { orderUid: ORDER_UID, MemberID: "m1", token: "jwt.secret" },
          }),
        ],
      });
    const res = await uipathFindOrderQueueItems(base);
    expect(res.items[0]?.specificContent).toEqual({
      orderUid: ORDER_UID,
      MemberID: "m1",
      token: "[redacted]",
    });
    expect(JSON.stringify(res)).not.toContain("jwt.secret");
  });

  test("includeSpecificContent: false drops it from the projection (null)", async () => {
    handler = () => json({ value: [queueItemRaw()] });
    const res = await uipathFindOrderQueueItems({ ...base, includeSpecificContent: false });
    expect(res.items[0]?.specificContent).toBeNull();
    expect(res.items[0]?.orderUid).toBe(ORDER_UID);
  });
});

describe("uipathPullQueueItem", () => {
  const sc = {
    orderUid: ORDER_UID,
    IsApproved: true,
    MemberFirstName: "Jane",
    MemberLastName: "Doe",
  };
  const serveItem = (row: Record<string, unknown>): void => {
    handler = (url) =>
      url.pathname.includes("/odata/QueueItems(")
        ? json(row)
        : url.pathname.includes("/odata/QueueDefinitions(")
          ? json({ Name: "PMG auth submit queue" })
          : json({ value: [] });
  };

  test("by itemId + env: IsApproved forced false, portal resolved, filename from the member", async () => {
    serveItem(queueItemRaw({ Id: 5, SpecificContent: sc }));
    const res = await uipathPullQueueItem({ env: "pre_prod", itemId: "5" });
    expect(res.env).toBe("pre_prod");
    expect(res.isApprovedForced).toBe(true);
    expect(res.item.specificContent?.["IsApproved"]).toBe(false);
    expect(res.item).toMatchObject({ itemId: "5", queueName: "PMG", orderUid: ORDER_UID });
    expect(res.portal).toMatchObject({ matched: true, key: "PMG", buildArtifact: "pmg-auth.cjs" });
    expect(res.suggestedFilename).toBe("jane-doe.yaml");
    expect(calls[0]?.pathname).toContain("/odata/QueueItems(5)");
  });

  test("by url: env and folder come from the fid", async () => {
    serveItem(queueItemRaw({ Id: 7, SpecificContent: sc }));
    const url =
      "https://cloud.uipath.com/myorg/mytenant/orchestrator_/queues/38592/transactions(sidepanel:sidepanel/transactions/7/details)?fid=434039";
    const res = await uipathPullQueueItem({ url });
    expect(res.env).toBe("pre_prod");
    expect(res.item.itemId).toBe("7");
    expect(calls[0]?.pathname).toContain("/odata/QueueItems(7)");
  });

  test("an item with no usable queue name falls back to the QueueDefinition lookup", async () => {
    serveItem(queueItemRaw({ SpecificContent: sc, QueueDefinition: null, Name: "" }));
    const res = await uipathPullQueueItem({ env: "prod", itemId: "1" });
    expect(res.item.queueName).toBe("PMG");
    expect(res.portal.matched).toBe(true);
    expect(res).not.toHaveProperty("queueNameError");
  });

  test("a failing QueueDefinition lookup surfaces as queueNameError, the pull still succeeds", async () => {
    handler = (url) =>
      url.pathname.includes("/odata/QueueItems(")
        ? json(queueItemRaw({ SpecificContent: sc, QueueDefinition: null, Name: "" }))
        : json({ message: "boom" }, 500);
    const res = await uipathPullQueueItem({ env: "prod", itemId: "1" });
    expect(res.queueNameError).toMatch(/500/);
    expect(res.portal.matched).toBe(false);
  });

  test("itemId without env, or a non-numeric itemId, is an ExpectedError before any request", async () => {
    for (const args of [
      { itemId: "5" },
      { env: "prod" as const, itemId: "abc" },
      { env: "prod" as const },
    ]) {
      const err = await uipathPullQueueItem(args).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ExpectedError);
    }
    expect(calls).toHaveLength(0);
  });
});

describe("uipathListQueues / uipathListProcesses / uipathListTriggers", () => {
  test("queues and processes map to cores keyed the way the write tools take them", async () => {
    handler = (url) =>
      url.pathname.endsWith("/odata/QueueDefinitions")
        ? json({
            value: [{ Id: 3, Name: "PMG auth submit queue", Description: "d", CreationTime: "t" }],
          })
        : json({
            value: [
              {
                Id: 8,
                Key: "release-guid",
                Name: "PMG auth submit",
                ProcessKey: "pkg",
                ProcessVersion: "1.2.3",
                IsLatestVersion: true,
              },
            ],
          });
    const queues = await uipathListQueues({ env: "prod", top: 10 });
    expect(queues[0]).toMatchObject({ id: "3", name: "PMG auth submit queue" });
    const processes = await uipathListProcesses({ env: "prod", top: 10 });
    expect(processes[0]).toMatchObject({ id: "release-guid", package: "pkg", version: "1.2.3" });
  });

  test("triggers: queueId keeps only triggers watching that queue (time triggers never match)", async () => {
    const row = (Id: number, QueueDefinitionId: number) => ({
      Id,
      Name: `t${Id}`,
      Enabled: true,
      ReleaseId: 8,
      ReleaseKey: "release-guid",
      ReleaseName: "PMG auth submit",
      QueueDefinitionId,
      QueueDefinitionName: "",
      ItemsActivationThreshold: 1,
      MaxJobsForActivation: 1,
    });
    handler = () => json({ value: [row(1, 77), row(2, 88), row(3, 0)] });

    const all = await uipathListTriggers({ env: "prod", top: 10 });
    expect(all.map((t) => [t.id, t.kind])).toEqual([
      ["1", "queue"],
      ["2", "queue"],
      ["3", "time"],
    ]);
    const only = await uipathListTriggers({ env: "prod", top: 10, queueId: "77" });
    expect(only.map((t) => t.id)).toEqual(["1"]);
    expect(await uipathListTriggers({ env: "prod", top: 10, queueId: "0" })).toEqual([]);
  });
});
