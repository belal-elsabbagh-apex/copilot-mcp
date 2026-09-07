import { describe, expect, test } from "bun:test";
import { parseRawMessage } from "./raw-message.js";
import type { JobLog } from "./uipath.js";

const log = (RawMessage?: string): JobLog => ({
  Level: "Info",
  Message: "Transaction Ended",
  TimeStamp: "2026-09-06T15:46:18.36Z",
  ...(RawMessage !== undefined ? { RawMessage } : {}),
});

describe("parseRawMessage", () => {
  test("returns null when RawMessage is absent", () => {
    expect(parseRawMessage(log())).toBeNull();
  });

  test("returns null on unparseable RawMessage (never throws)", () => {
    expect(parseRawMessage(log("not json"))).toBeNull();
    expect(parseRawMessage(log('"just a json string, not an object"'))).toBeNull();
  });

  // Shape confirmed live against real Transaction Ended rows (2026-09-07).
  test("extracts the known transaction/exception fields off a Transaction Ended row", () => {
    const raw = JSON.stringify({
      message: "Transaction Ended",
      level: "Information",
      transactionState: "Ended",
      transactionExecutionTime: 49.93,
      transactionId: "d211b063-20eb-45a9-801a-5634cd13568a",
      queueItemPriority: "Normal",
      processingExceptionReason: "",
      queueName: "CenCal auth sync queue",
      processingExceptionType: "BusinessException",
      queueItemReviewStatus: "None",
      // Confirmed broken live: UiPath logs the unresolved InArgument binding here,
      // never the actual status — must never surface as a real field.
      transactionStatus: "System.Activities.InArgument`1[UiPath.Core.ProcessingStatus]",
    });
    const parsed = parseRawMessage(log(raw));
    expect(parsed).not.toBeNull();
    expect(parsed?.transactionId).toBe("d211b063-20eb-45a9-801a-5634cd13568a");
    expect(parsed?.transactionState).toBe("Ended");
    expect(parsed?.queueName).toBe("CenCal auth sync queue");
    expect(parsed?.processingExceptionType).toBe("BusinessException");
    expect(parsed?.transactionExecutionTimeSec).toBe(49.93);
    expect(parsed?.queueItemPriority).toBe("Normal");
    expect(parsed?.queueItemReviewStatus).toBe("None");
  });

  test("an empty-string field (present but blank) collapses to null, same as absent", () => {
    const raw = JSON.stringify({ processingExceptionReason: "" });
    expect(parseRawMessage(log(raw))?.processingExceptionReason).toBeNull();
  });

  test("never surfaces transactionStatus even when present in the raw JSON", () => {
    const raw = JSON.stringify({
      transactionStatus: "System.Activities.InArgument`1[UiPath.Core.ProcessingStatus]",
    });
    const parsed = parseRawMessage(log(raw));
    expect(parsed).not.toBeNull();
    expect(parsed).not.toHaveProperty("transactionStatus");
  });

  test("extracts totalExecutionTime fields off an execution-ended row", () => {
    const raw = JSON.stringify({
      message: "CenCal Sync by Playwright execution ended",
      totalExecutionTimeInSeconds: 52,
      totalExecutionTime: "00:00:52",
    });
    expect(parseRawMessage(log(raw))?.totalExecutionTimeInSeconds).toBe(52);
  });

  // Confirmed live: Studio's "Add Log Fields" activity output shows up as an
  // ordinary top-level RawMessage key under whatever name the process author
  // chose (this org's example: healingAgentConfig on one specific log row).
  test("collects unrecognized fields (Add Log Fields output) into custom, verbatim", () => {
    const raw = JSON.stringify({
      message: "Healing agent configuration.",
      healingAgentConfig: { orchestratorEnableHeal: false, orchestratorEnableAnalysis: false },
    });
    const parsed = parseRawMessage(log(raw));
    expect(parsed?.custom).toEqual({
      healingAgentConfig: { orchestratorEnableHeal: false, orchestratorEnableAnalysis: false },
    });
    // Known fields never leak into custom.
    expect(parsed?.custom["message"]).toBeUndefined();
  });

  test("fields absent from an ordinary Info row are all null, not an error", () => {
    const raw = JSON.stringify({ message: "Login successful.", level: "Information" });
    const parsed = parseRawMessage(log(raw));
    expect(parsed?.transactionId).toBeNull();
    expect(parsed?.processingExceptionType).toBeNull();
    expect(parsed?.totalExecutionTimeInSeconds).toBeNull();
    expect(parsed?.custom).toEqual({});
  });
});
