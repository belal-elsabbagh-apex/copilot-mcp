import { describe, expect, test } from "bun:test";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { SetLevelRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import { _currentLevel, mcpLog, registerLogging, reportProgress, withHeartbeat } from "./notify.js";

// Minimal McpServer stand-in: capture outgoing logging notifications and the
// setLevel handler that registerLogging installs, without standing up a transport.
function makeFakeServer() {
  const logs: { level: string; data: unknown }[] = [];
  let setLevelHandler: ((req: { params: { level: string } }) => unknown) | undefined;
  const server = {
    server: {
      sendLoggingMessage: (params: { level: string; data: unknown }) => {
        logs.push({ level: params.level, data: params.data });
        return Promise.resolve();
      },
      setRequestHandler: (
        schema: unknown,
        handler: (req: { params: { level: string } }) => unknown,
      ) => {
        if (schema === SetLevelRequestSchema) setLevelHandler = handler;
      },
    },
  } as unknown as McpServer;
  return { server, logs, setLevel: (level: string) => setLevelHandler?.({ params: { level } }) };
}

// NOTE: the current level is module-global; these tests run top-to-bottom and the
// last one deliberately lowers it. Keep suppression assertions before that.
describe("mcpLog level gating", () => {
  test("suppresses messages below the default (info) level", () => {
    const { server, logs } = makeFakeServer();
    mcpLog(server, "debug", "noisy");
    expect(logs.length).toBe(0);
  });

  test("emits at or above the current level, packing message + data", () => {
    const { server, logs } = makeFakeServer();
    mcpLog(server, "info", "hi");
    mcpLog(server, "error", "boom", { code: 1 });
    expect(logs.map((l) => l.level)).toEqual(["info", "error"]);
    expect(logs[1]?.data).toEqual({ message: "boom", code: 1 });
  });

  test("setLevel lowers the threshold so debug becomes visible", () => {
    const { server, logs, setLevel } = makeFakeServer();
    registerLogging(server);
    setLevel("debug");
    expect(_currentLevel()).toBe("debug");
    mcpLog(server, "debug", "now visible");
    expect(logs.map((l) => l.level)).toEqual(["debug"]);
  });
});

describe("reportProgress", () => {
  function makeExtra(progressToken?: string | number) {
    const sent: unknown[] = [];
    const extra = {
      _meta: progressToken === undefined ? undefined : { progressToken },
      sendNotification: (n: unknown) => {
        sent.push(n);
        return Promise.resolve();
      },
    } as unknown as Parameters<typeof reportProgress>[0];
    return { extra, sent };
  }

  test("no-ops when the client did not request progress", () => {
    const { extra, sent } = makeExtra();
    reportProgress(extra, 1, 3, "x");
    expect(sent.length).toBe(0);
  });

  test("emits a progress notification when a token is present", () => {
    const { extra, sent } = makeExtra("tok-1");
    reportProgress(extra, 2, 5, "halfway");
    expect(sent).toEqual([
      {
        method: "notifications/progress",
        params: { progressToken: "tok-1", progress: 2, total: 5, message: "halfway" },
      },
    ]);
  });

  test("omits total/message when not supplied", () => {
    const { extra, sent } = makeExtra(7);
    reportProgress(extra, 1);
    expect(sent).toEqual([
      { method: "notifications/progress", params: { progressToken: 7, progress: 1 } },
    ]);
  });
});

describe("withHeartbeat", () => {
  function makeExtra(progressToken?: string | number) {
    const sent: { params: { progress: number; message?: string } }[] = [];
    const extra = {
      _meta: progressToken === undefined ? undefined : { progressToken },
      sendNotification: (n: unknown) => {
        sent.push(n as (typeof sent)[number]);
        return Promise.resolve();
      },
    } as unknown as Parameters<typeof withHeartbeat>[0];
    return { extra, sent };
  }

  const after = <T>(ms: number, value: T): Promise<T> =>
    new Promise((res) => setTimeout(() => res(value), ms));

  test("ticks while the work is in flight and stops once it settles", async () => {
    const { extra, sent } = makeExtra("tok");
    const got = await withHeartbeat(extra, "Orchestrator", after(120, "done"), 30);
    expect(got).toBe("done");
    // ~3 ticks at 30ms over 120ms; timing-tolerant lower bound.
    expect(sent.length).toBeGreaterThanOrEqual(2);
    expect(sent[0]?.params.message).toContain("Orchestrator");
    // Monotonically increasing progress, as the MCP spec requires.
    expect(sent.map((s) => s.params.progress)).toEqual(sent.map((_, i) => i + 1));
    const settled = sent.length;
    await after(80, null);
    expect(sent.length).toBe(settled);
  });

  test("never starts a timer when the client did not request progress", async () => {
    const { extra, sent } = makeExtra();
    await withHeartbeat(extra, "Orchestrator", after(80, 1), 20);
    expect(sent.length).toBe(0);
  });

  test("propagates rejection and clears the timer", async () => {
    const { extra, sent } = makeExtra("tok");
    const boom = new Promise((_res, rej) => setTimeout(() => rej(new Error("nope")), 60));
    await expect(withHeartbeat(extra, "BE", boom, 20)).rejects.toThrow("nope");
    const settled = sent.length;
    await after(80, null);
    expect(sent.length).toBe(settled);
  });
});
