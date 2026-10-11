import { describe, expect, test } from "bun:test";
import { EXPECTED_V1_33_1, NOW_SEC, ORDER, ORDER_UID } from "./payload.fixture.js";
import { assembleQueueItem } from "./queue-item.js";

describe("v1.33.1 build_queue_item golden", () => {
  test("assembleQueueItem reproduces the shipped output for the fixture order", () => {
    const out = assembleQueueItem({
      order: ORDER,
      orderUid: ORDER_UID,
      profile: "ossm",
      env: "pre_prod",
      automationId: "21a5ab32-817c-4a4a-95a0-bff16605ca6b",
      token: "eyJhbGciOiJub25lIn0.eyJpZCI6NDJ9.sig", // {"id":42}
      physicianId: 42,
      supportMode: true,
      providerMail: "owner@example.com",
      beBase: "https://be.preprod.example.com",
      uipath: {
        orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_/",
        queueUrl: "https://sqs.example.com/queue",
        addQueueItemPath: "/odata/Queues/UiPathODataSvc.AddQueueItem",
        folderPath: "Authorization Dev Clone",
        serverUrl: "https://pre-prod-be.example.com",
      },
      nowSec: NOW_SEC,
    });
    expect(out).toEqual(EXPECTED_V1_33_1);
  });
});
