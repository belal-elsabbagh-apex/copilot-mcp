import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import { assembleQueueItemContent } from "../orchestrator/queue-item.js";
import { EXPECTED_V1_33_1, NOW_SEC, ORDER, ORDER_UID } from "./payload.fixture.js";
import { serializeUipathContent, uipathBuildQueueItemPayload } from "./payload.js";

const UIPATH = {
  orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_/",
  bearer: "test-bearer",
  queueUrl: "https://sqs.example.com/queue",
  addQueueItemPath: "/odata/Queues/UiPathODataSvc.AddQueueItem",
  folderPath: "Authorization Dev Clone",
  serverUrlByEnv: { pre_prod: "https://pre-prod-be.example.com" },
};
const creds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});
const FIXTURE = {
  copilot: { sessionCache: false, prod: creds("prod"), pre_prod: creds("preprod") },
  uipath: UIPATH,
};

let dir: string;
let prevConfig: string | undefined;
const realNow = Date.now;

const writeConfig = (config: unknown): void => {
  writeFileSync(join(dir, "config.json"), JSON.stringify(config));
  resetConfigCache();
};

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-payload-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeConfig(FIXTURE);
  Date.now = () => NOW_SEC * 1000;
});

afterEach(() => writeConfig(FIXTURE)); // undo per-test config overrides

afterAll(() => {
  Date.now = realNow;
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

const assemble = () =>
  assembleQueueItemContent({
    order: ORDER,
    orderUid: ORDER_UID,
    profile: "ossm",
    env: "pre_prod",
    automationId: "21a5ab32-817c-4a4a-95a0-bff16605ca6b",
    token: "eyJhbGciOiJub25lIn0.eyJpZCI6NDJ9.sig",
    physicianId: 42,
    supportMode: true,
    providerMail: "owner@example.com",
  });

const payloadFor = (env: "prod" | "pre_prod", content: Record<string, unknown>) =>
  uipathBuildQueueItemPayload({
    env,
    queueName: "Acme Health auth submit queue",
    orderUid: ORDER_UID,
    content,
    beBase: "https://be.preprod.example.com",
  });

describe("v1.33.1 build_queue_item parity", () => {
  // build_queue_item = content builder + backend serializer, notes/meta merged in that order.
  test("content builder + UiPath serializer reproduce the shipped output for the fixture order", () => {
    const built = assemble();
    const p = uipathBuildQueueItemPayload({
      env: "pre_prod",
      queueName: built.queueName,
      orderUid: ORDER_UID,
      content: built.content,
      beBase: "https://be.preprod.example.com",
    });
    expect({
      payload: p.payload,
      notes: [...built.notes, ...p.notes],
      meta: { ...built.meta, ...p.meta },
    }).toEqual(EXPECTED_V1_33_1);
  });

  test("serverURL falls back to the BE the order was read from when no URL is pinned for the env", () => {
    const { payload } = payloadFor("prod", assemble().content);
    const sc = (payload as { itemData: { SpecificContent: Record<string, unknown> } }).itemData
      .SpecificContent;
    expect(sc["serverURL"]).toBe("https://be.preprod.example.com");
  });
});

describe("serializeUipathContent", () => {
  test("stringifies exactly the array fields and callbackContext, leaving native types elsewhere", () => {
    const out = serializeUipathContent({
      Diagnoses: ["I77.9"],
      Units: [],
      callbackContext: { physicianId: 7, orderUid: "u" },
      IsRetro: true,
      retryCount: 0,
      OpenReferralInfo: null,
      MemberID: "M1",
    });
    expect(out).toEqual({
      Diagnoses: '["I77.9"]',
      Units: "[]",
      callbackContext: '{"physicianId":7,"orderUid":"u"}',
      IsRetro: true,
      retryCount: 0,
      OpenReferralInfo: null,
      MemberID: "M1",
    });
  });

  test("does not invent a stringified field the content lacks", () => {
    expect(serializeUipathContent({ MemberID: "M1" })).toEqual({ MemberID: "M1" });
  });
});

describe("uipathBuildQueueItemPayload", () => {
  test("forces IsApproved=false even when the content says true", () => {
    const { payload } = payloadFor("pre_prod", { ...assemble().content, IsApproved: true });
    const sc = (payload as { itemData: { SpecificContent: Record<string, unknown> } }).itemData
      .SpecificContent;
    expect(sc["IsApproved"]).toBe(false);
  });

  test("names every missing queue-build field in the config error", () => {
    writeConfig({
      ...FIXTURE,
      uipath: { orchestratorUrl: UIPATH.orchestratorUrl, bearer: "test-bearer" },
    });
    expect(() => payloadFor("pre_prod", assemble().content)).toThrow(
      /missing fields required for build_queue_item: queueUrl, addQueueItemPath, folderPath/,
    );
  });
});
