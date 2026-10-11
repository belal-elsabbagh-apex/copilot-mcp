import { describe, expect, test } from "bun:test";
import {
  exceptionTypeOf,
  toJobCore,
  toProcessCore,
  toQueueCore,
  toQueueItemCoreFromItem,
  toQueueItemCoreFromMatch,
  toTriggerCore,
  toUiPathLogDigest,
  UIPATH_TERMINAL_STATES,
  uipathFault,
} from "./map.js";
import type { QueueItem, QueueItemMatch, UiPathJob, UiPathLogRow } from "./uipath.js";

const KEY = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
const LINK = `https://cloud.uipath.com/org/tenant/orchestrator_/jobs/${KEY}`;

const job: UiPathJob = {
  Id: "12345",
  Key: KEY,
  State: "Faulted",
  ReleaseName: "PMG auth submit",
  ProcessVersion: "1.2.3",
  RobotName: "robot-7",
  CreationTime: "2026-07-01T10:00:00Z",
  EndTime: "2026-07-01T10:01:30Z",
};

describe("UIPATH_TERMINAL_STATES", () => {
  test("is exactly Successful, Faulted, Stopped", () => {
    expect([...UIPATH_TERMINAL_STATES].sort()).toEqual(["Faulted", "Stopped", "Successful"]);
  });
});

describe("toJobCore", () => {
  test("maps a faulted job; state passes through verbatim and fault stays null", () => {
    const core = toJobCore(job, { link: LINK });
    expect(core).toEqual({
      orchestrator: "uipath",
      id: KEY,
      state: "Faulted",
      rawState: "Faulted",
      terminal: true,
      process: { id: null, name: "PMG auth submit", version: "1.2.3" },
      queueItem: null,
      createdAt: "2026-07-01T10:00:00Z",
      startedAt: null,
      endedAt: "2026-07-01T10:01:30Z",
      durationMs: 90_000,
      attempt: null,
      retryOfJobId: null,
      fault: null,
      link: LINK,
      details: { numericId: "12345", robotName: "robot-7" },
    });
  });

  test("a running job is not terminal and has no end/duration; absent fields default", () => {
    const core = toJobCore(
      { Key: KEY, State: "Running", CreationTime: "2026-07-01T10:00:00Z" },
      {
        link: "",
      },
    );
    expect(core.terminal).toBe(false);
    expect(core.endedAt).toBeNull();
    expect(core.durationMs).toBeNull();
    expect(core.process).toEqual({ id: null, name: "", version: null });
    expect(core.details).toEqual({ numericId: null, robotName: "" });
  });

  test("output is carried under details only when the read fetched it (null is a value)", () => {
    expect(toJobCore(job, { link: LINK }).details).not.toHaveProperty("output");
    expect(toJobCore(job, { link: LINK, output: null }).details).toHaveProperty("output", null);
    expect(toJobCore(job, { link: LINK, output: { ok: 1 } }).details.output).toEqual({ ok: 1 });
  });

  test("every UiPath state outside the terminal set is non-terminal", () => {
    for (const state of ["Pending", "Running", "Stopping", "Suspended", "Resumed"]) {
      expect(toJobCore({ Key: KEY, State: state }, { link: "" }).terminal).toBe(false);
    }
  });
});

describe("exceptionTypeOf", () => {
  const cases: Array<[string, "business" | "application" | null]> = [
    ["BusinessException", "business"],
    ["ApplicationException", "application"],
    ["Business", "business"],
    ["Application", "application"],
    ["System", "application"],
    [" businessexception ", "business"],
    ["", null],
    ["SomethingElse", null],
  ];
  for (const [input, expected] of cases) {
    test(`${JSON.stringify(input)} -> ${expected}`, () => {
      expect(exceptionTypeOf(input)).toBe(expected);
    });
  }
});

const item: QueueItem = {
  id: 5,
  status: "Failed",
  reference: "REF-1",
  creationTime: "2026-07-01T09:00:00Z",
  retryNumber: 1,
  queueDefinitionId: 77,
  name: "PMG auth submit queue",
  robotName: "robot-7",
  specificContent: { orderUid: "order-uid-1", MemberID: "m1", MemberFullName: "Doe, Jane" },
};

describe("toQueueItemCoreFromItem", () => {
  test("maps the item; the default projection withholds specificContent", () => {
    const core = toQueueItemCoreFromItem(item, { queueName: "PMG", specificContent: null });
    expect(core).toEqual({
      orchestrator: "uipath",
      queueId: "77",
      queueName: "PMG",
      itemId: "5",
      reference: "REF-1",
      orderUid: "order-uid-1",
      status: "Failed",
      rawStatus: "Failed",
      exceptionType: null,
      createdAt: "2026-07-01T09:00:00Z",
      endedAt: null,
      jobs: [],
      specificContent: null,
      details: {
        retryNumber: 1,
        robotName: "robot-7",
        queueDefinitionId: 77,
        memberId: "m1",
        member: "Doe, Jane",
      },
    });
    // PHI-light default: the member triage columns ride in details, the full payload does not
    expect(JSON.stringify(core.specificContent)).toBe("null");
  });

  test('member columns default to "" when absent or not strings', () => {
    const core = toQueueItemCoreFromItem(
      { ...item, specificContent: { MemberID: 5 } },
      { queueName: "PMG", specificContent: null },
    );
    expect(core.details.memberId).toBe("");
    expect(core.details.member).toBe("");
  });

  test("a supplied specificContent is carried as-is; a missing orderUid is null", () => {
    const sc = { IsApproved: false };
    const core = toQueueItemCoreFromItem(
      { ...item, specificContent: { orderUid: 42 } },
      { queueName: "PMG", specificContent: sc },
    );
    expect(core.specificContent).toBe(sc);
    expect(core.orderUid).toBeNull();
  });
});

const match: QueueItemMatch = {
  id: 9,
  reference: "REF-9",
  status: "Failed",
  executorJobKey: KEY,
  processingExceptionType: "BusinessException",
  retryNumber: 2,
  creationTime: "2026-07-01T09:30:00Z",
  specificContent: {
    orderUid: "order-uid-1",
    MemberID: "m1",
    MemberFullName: "Doe, Jane",
    token: "[redacted]",
  },
  queueDefinitionId: 77,
};

describe("toQueueItemCoreFromMatch", () => {
  test("a picked-up item yields one unknown-state job attempt and a classified exception", () => {
    const core = toQueueItemCoreFromMatch(match, { queueName: "PMG" });
    expect(core.jobs).toEqual([
      { orchestrator: "uipath", id: KEY, state: null, neverStarted: null },
    ]);
    expect(core.exceptionType).toBe("business");
    expect(core.orderUid).toBe("order-uid-1");
    expect(core.specificContent).toEqual({
      orderUid: "order-uid-1",
      MemberID: "m1",
      MemberFullName: "Doe, Jane",
      token: "[redacted]",
    });
    expect(core.details).toEqual({
      retryNumber: 2,
      robotName: "",
      queueDefinitionId: 77,
      memberId: "m1",
      member: "Doe, Jane",
    });
  });

  test("a not-yet-picked-up item has no jobs and no exception", () => {
    const core = toQueueItemCoreFromMatch(
      { ...match, executorJobKey: "", processingExceptionType: "", status: "New" },
      { queueName: "PMG" },
    );
    expect(core.jobs).toEqual([]);
    expect(core.exceptionType).toBeNull();
    expect(core.status).toBe("New");
  });
});

describe("toQueueCore / toProcessCore", () => {
  test("queue", () => {
    expect(
      toQueueCore({ id: 3, name: "PMG auth submit queue", description: "d", creationTime: "t" }),
    ).toEqual({
      orchestrator: "uipath",
      id: "3",
      name: "PMG auth submit queue",
      processId: null,
      active: true,
      details: { description: "d", creationTime: "t" },
    });
  });

  test("release: id is the Key (what start_job takes); empty package/version are null", () => {
    const base = {
      id: 8,
      key: KEY,
      name: "PMG auth submit",
      processKey: "pkg",
      processVersion: "1.2.3",
      isLatestVersion: true,
    };
    expect(toProcessCore(base)).toEqual({
      orchestrator: "uipath",
      id: KEY,
      name: "PMG auth submit",
      package: "pkg",
      version: "1.2.3",
      active: true,
      details: { numericId: "8", isLatestVersion: true },
    });
    const bare = toProcessCore({ ...base, processKey: "", processVersion: "" });
    expect(bare.package).toBeNull();
    expect(bare.version).toBeNull();
  });
});

describe("toTriggerCore", () => {
  const base = {
    id: 4,
    name: "trg",
    enabled: true,
    releaseId: 8,
    releaseKey: KEY,
    releaseName: "PMG auth submit",
    queueDefinitionId: 77,
    queueDefinitionName: "PMG auth submit queue",
    itemsActivationThreshold: 1,
    maxJobsForActivation: 2,
  };

  test("a queue trigger links process and queue", () => {
    const core = toTriggerCore(base);
    expect(core.kind).toBe("queue");
    expect(core.queueId).toBe("77");
    expect(core.processId).toBe(KEY);
    expect(core.cron).toBeNull();
    expect(core.details).toEqual({
      releaseId: 8,
      releaseName: "PMG auth submit",
      queueDefinitionName: "PMG auth submit queue",
      itemsActivationThreshold: 1,
      maxJobsForActivation: 2,
    });
  });

  test("a time trigger (queueDefinitionId 0) has no queue", () => {
    const core = toTriggerCore({ ...base, queueDefinitionId: 0, queueDefinitionName: "" });
    expect(core.kind).toBe("time");
    expect(core.queueId).toBeNull();
  });
});

const errLog = (Message: string): UiPathLogRow => ({
  Level: "Error",
  Message,
  TimeStamp: "2026-07-01T10:00:30Z",
});

describe("uipathFault", () => {
  test("class is the exception type parsed from the headline error; signature is stable", () => {
    const fault = uipathFault(job, [
      errLog("System.NullReferenceException: boom on 7bfea00c-1111-2222-3333-444444444444"),
    ]);
    expect(fault.class).toBe("System.NullReferenceException");
    expect(fault.signature).toContain("<id>");
    expect(fault.message).toContain("NullReferenceException");
  });

  test("no logs -> synthetic state message and a null class", () => {
    expect(uipathFault(job, [])).toEqual({
      message: "Job ended in state Faulted",
      signature: "job ended in state faulted",
      class: null,
    });
  });
});

describe("toUiPathLogDigest", () => {
  test("the generic digest plus the queue transaction's outcome", () => {
    const ended = JSON.stringify({
      transactionState: "Ended",
      processingExceptionType: "BusinessException",
    });
    const digest = toUiPathLogDigest([
      errLog("Submit failed"),
      {
        Level: "Info",
        Message: "Transaction Ended",
        TimeStamp: "2026-07-01T10:00:31Z",
        RawMessage: ended,
      },
    ]);
    expect(digest.fetched).toBe(2);
    expect(digest.failures.map((f) => f.message)).toEqual(["Submit failed"]);
    expect(digest.transactionOutcome?.processingExceptionType).toBe("BusinessException");
  });

  test("transactionOutcome is null when RawMessage was never fetched", () => {
    expect(toUiPathLogDigest([errLog("x")]).transactionOutcome).toBeNull();
  });
});
