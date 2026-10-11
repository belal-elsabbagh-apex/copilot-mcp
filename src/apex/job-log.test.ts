import { describe, expect, test } from "bun:test";
import { parseJobLog } from "./job-log.js";

const JOB = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const HEADER = `=== apex job ${JOB} started (workflow=demo-portal runtime=python) ===`;
const FOOTER = `=== apex job ${JOB} finished: exit_code=21 timed_out=False cancelled=False suspended=False duration=9s ===`;
const nd = (o: Record<string, unknown>): string =>
  JSON.stringify({ v: 1, portal: "demo", timestamp: "2026-10-08T00:40:00.000Z", ...o });

// One of every line kind, in the order a failed run writes them.
const FULL = [
  HEADER,
  "[apex-adapter] claimed job, starting runtime",
  nd({ level: "INFO", message: "login ok" }),
  nd({
    level: "WARN",
    message: "slow page",
    timestamp: "2026-10-08T00:40:05.000Z",
    fields: { ms: 5200 },
  }),
  nd({ level: "ERROR", message: "row not found", timestamp: "2026-10-08T00:40:09.000Z" }),
  "a plain untyped line",
  "[trace] some other bracket line",
  "",
  "Traceback (most recent call last):",
  '  File "/app/main.py", line 10, in run',
  '    raise ValueError("boom")',
  "ValueError: boom",
  "[apex-adapter] result=FAILED reason=business_failure exit=21 outcomes=not_found,retry",
  FOOTER,
].join("\n");

describe("parseJobLog line kinds", () => {
  const parsed = parseJobLog(FULL);

  test("classifies every line, skipping blank ones", () => {
    expect(parsed.rows.map((r) => r.kind)).toEqual([
      "header",
      "adapter",
      "ndjson",
      "ndjson",
      "ndjson",
      "plain",
      "plain",
      "traceback",
      "traceback",
      "traceback",
      "traceback",
      "adapter-result",
      "footer",
    ]);
  });

  test("NDJSON rows map levels and keep message, timestamp and fields", () => {
    const [info, warn, error] = parsed.rows.filter((r) => r.kind === "ndjson");
    expect(info).toMatchObject({ Level: "Info", Message: "login ok" });
    expect(warn).toMatchObject({
      Level: "Warn",
      Message: "slow page",
      TimeStamp: "2026-10-08T00:40:05.000Z",
      fields: { ms: 5200 },
    });
    expect(error).toMatchObject({ Level: "Error", Message: "row not found" });
    expect(info?.fields).toBeUndefined();
  });

  test("non-NDJSON, non-traceback lines are Info and keep their text", () => {
    const plain = parsed.rows.filter((r) => r.kind === "plain");
    expect(plain.map((r) => [r.Level, r.Message])).toEqual([
      ["Info", "a plain untyped line"],
      ["Info", "[trace] some other bracket line"],
    ]);
  });

  test("a Python traceback is Error level through its exception line, and no further", () => {
    const tb = parsed.rows.filter((r) => r.kind === "traceback");
    expect(tb.every((r) => r.Level === "Error")).toBe(true);
    expect(tb.at(-1)?.Message).toBe("ValueError: boom");
    expect(parsed.rows.find((r) => r.kind === "adapter-result")?.Level).toBe("Info");
  });
});

describe("levels", () => {
  const level = (l: unknown): string | undefined =>
    parseJobLog(nd({ level: l, message: "m" })).rows[0]?.Level;

  test("known levels, case-insensitively", () => {
    expect(level("DEBUG")).toBe("Debug");
    expect(level("warning")).toBe("Warn");
    expect(level("error")).toBe("Error");
  });

  test("unknown levels are kept verbatim, capitalized; absent level is Info", () => {
    expect(level("NOTICE")).toBe("Notice");
    expect(level(undefined)).toBe("Info");
    expect(level(7)).toBe("Info");
  });
});

describe("timestamps", () => {
  test("untimed lines inherit the previous row's timestamp", () => {
    const text = [
      nd({ message: "a", timestamp: "2026-10-08T00:40:00.000Z" }),
      "[apex-adapter] midway note",
      nd({ message: "b", timestamp: "2026-10-08T00:41:00.000Z" }),
      FOOTER,
    ].join("\n");
    expect(parseJobLog(text).rows.map((r) => r.TimeStamp)).toEqual([
      "2026-10-08T00:40:00.000Z",
      "2026-10-08T00:40:00.000Z",
      "2026-10-08T00:41:00.000Z",
      "2026-10-08T00:41:00.000Z",
    ]);
  });

  test("leading untimed rows take the first timestamp that follows", () => {
    const rows = parseJobLog([HEADER, "[apex-adapter] boot", nd({ message: "a" })].join("\n")).rows;
    expect(rows.map((r) => r.TimeStamp)).toEqual(Array(3).fill("2026-10-08T00:40:00.000Z"));
  });

  test("a log with no timestamps keeps empty ones", () => {
    expect(parseJobLog(`${HEADER}\nplain`).rows.map((r) => r.TimeStamp)).toEqual(["", ""]);
  });
});

describe("footer", () => {
  test("parses exit code and flags", () => {
    expect(parseJobLog(FULL).footer).toEqual({
      exitCode: 21,
      timedOut: false,
      cancelled: false,
      suspended: false,
    });
  });

  test("True flags and exit_code=None", () => {
    const f = `=== apex job ${JOB} finished: exit_code=None timed_out=True cancelled=True suspended=True ===`;
    expect(parseJobLog(f).footer).toEqual({
      exitCode: null,
      timedOut: true,
      cancelled: true,
      suspended: true,
    });
  });

  test("a missing footer (killed run) is null, rows still parse", () => {
    const parsed = parseJobLog([HEADER, nd({ message: "a" })].join("\n"));
    expect(parsed.footer).toBeNull();
    expect(parsed.rows).toHaveLength(2);
  });
});

describe("adapter result", () => {
  test("parses result, reason, exit and outcomes", () => {
    expect(parseJobLog(FULL).adapterResult).toEqual({
      result: "FAILED",
      reason: "business_failure",
      exit: 21,
      outcomes: ["not_found", "retry"],
    });
  });

  test("`-` placeholders become null / []", () => {
    const line = "[apex-adapter] result=OK reason=- exit=- outcomes=-";
    expect(parseJobLog(line).adapterResult).toEqual({
      result: "OK",
      reason: null,
      exit: null,
      outcomes: [],
    });
  });

  test("an unparseable result line is a plain adapter row and no result", () => {
    const parsed = parseJobLog("[apex-adapter] result=OK");
    expect(parsed.adapterResult).toBeNull();
    expect(parsed.rows[0]?.kind).toBe("adapter");
  });
});

describe("malformed input", () => {
  test("broken NDJSON and JSON without a message are plain lines", () => {
    const text = ['{"v":1,"level":"ERROR","message":"cut off', '{"v":1,"level":"ERROR"}'].join(
      "\n",
    );
    const rows = parseJobLog(text).rows;
    expect(rows.map((r) => [r.kind, r.Level])).toEqual([
      ["plain", "Info"],
      ["plain", "Info"],
    ]);
  });

  test("a non-object `fields` is dropped", () => {
    expect(parseJobLog(nd({ message: "m", fields: "x" })).rows[0]?.fields).toBeUndefined();
  });

  test("empty text", () => {
    expect(parseJobLog("")).toEqual({ rows: [], footer: null, adapterResult: null });
  });

  test("CRLF line endings", () => {
    const parsed = parseJobLog(`${HEADER}\r\n${nd({ message: "a" })}\r\n${FOOTER}\r\n`);
    expect(parsed.rows.map((r) => r.kind)).toEqual(["header", "ndjson", "footer"]);
    expect(parsed.footer?.exitCode).toBe(21);
  });
});
