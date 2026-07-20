import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resetConfigCache } from "../config/config.js";
import type { BeOrder } from "./copilot-client.js";
import {
  buildFilterBody,
  buildFilterDimensions,
  getOrderCategoryStats,
  searchOrders,
  slimOrderRow,
} from "./order-search.js";

// ---- pure helpers -----------------------------------------------------------

describe("buildFilterDimensions", () => {
  test("defaults type to 'Outbound Referral' and always sends orderMode", () => {
    expect(buildFilterDimensions({})).toEqual({
      type: "Outbound Referral",
      orderMode: '["orders_only_mode","pcp_notes_mode"]',
    });
  });

  test("caller-provided type overrides the default", () => {
    expect(buildFilterDimensions({ type: "PCP Notes" })).toEqual({
      type: "PCP Notes",
      orderMode: '["orders_only_mode","pcp_notes_mode"]',
    });
  });

  test("JSON-string-encodes non-empty array filters", () => {
    const out = buildFilterDimensions({
      locations: ["OSSM COR", "OSSM RIV"],
      insurances: ["OPTUM CARE NETWORK"],
    });
    expect(out["locations"]).toBe('["OSSM COR","OSSM RIV"]');
    expect(out["insurances"]).toBe('["OPTUM CARE NETWORK"]');
  });

  test("omits an array filter that is undefined or empty", () => {
    const out = buildFilterDimensions({ locations: [], insurances: undefined });
    expect(out).not.toHaveProperty("locations");
    expect(out).not.toHaveProperty("insurances");
  });

  test("passes scalar and boolean filters through untouched, omitting unset ones", () => {
    const out = buildFilterDimensions({
      mrn: "12345",
      hasAuthScreenshot: true,
      sendFax: false,
    });
    expect(out["mrn"]).toBe("12345");
    expect(out["hasAuthScreenshot"]).toBe(true);
    expect(out["sendFax"]).toBe(false);
    expect(out).not.toHaveProperty("missingNotes");
  });
});

describe("buildFilterBody", () => {
  test("adds default paging (pageSize 100, pageNumber 1) on top of the dimensions", () => {
    expect(buildFilterBody({})).toEqual({
      pageSize: 100,
      pageNumber: 1,
      type: "Outbound Referral",
      orderMode: '["orders_only_mode","pcp_notes_mode"]',
    });
  });

  test("honors caller-provided paging", () => {
    const out = buildFilterBody({ pageSize: 50, pageNumber: 3 });
    expect(out.pageSize).toBe(50);
    expect(out.pageNumber).toBe(3);
  });
});

// ---- slimOrderRow -------------------------------------------------------------

describe("slimOrderRow", () => {
  test("maps a facility-referral row, no patient field anywhere", () => {
    const order: BeOrder = {
      orderUid: "u1",
      creationDate: "2026-07-01T00:00:00Z",
      status: "forReview",
      category: "For Review",
      orderType: { name: "Consultation Referral" },
      insurance: { name: "OPTUM CARE NETWORK" },
      speciality: { name: "Cardiology" },
      referredFacility: { name: "Regal Medical Group", NPI: "1234567890", external: true },
      requiredAuthorization: true,
      authStatus: "approved",
      uploadStatusAuth: "completed",
      uploadStatusFax: "not_started",
      patient: { patientName: "DOE, JANE" },
    };
    const row = slimOrderRow(order);
    expect(row).toEqual({
      orderUid: "u1",
      creationDate: "2026-07-01T00:00:00Z",
      status: "forReview",
      category: "For Review",
      orderType: "Consultation Referral",
      insurance: "OPTUM CARE NETWORK",
      speciality: "Cardiology",
      referredTo: { name: "Regal Medical Group", npi: "1234567890", external: true },
      authRequired: true,
      authStatus: "approved",
      uploadStatusAuth: "completed",
      uploadStatusFax: "not_started",
    });
    expect(row).not.toHaveProperty("patient");
  });

  test("falls back to referredProvider when there's no referredFacility (PCP-notes send)", () => {
    const row = slimOrderRow({
      orderUid: "u2",
      referredProvider: { name: "Dr. Smith", NPI: "9999999999" },
    });
    expect(row.referredTo).toEqual({ name: "Dr. Smith", npi: "9999999999", external: undefined });
  });

  test("omits referredTo entirely when neither facility nor provider is present", () => {
    const row = slimOrderRow({ orderUid: "u3" });
    expect(row.referredTo).toBeUndefined();
  });
});

// ---- HTTP wiring (config fixture + fetch stub) -------------------------------

const envCreds = (name: string) => ({
  be: `https://be.${name}.example.com`,
  email: `${name}@example.com`,
  password: "pw",
});

const FIXTURE = {
  copilot: {
    prod: envCreds("prod"),
    pre_prod: envCreds("preprod"),
    profiles: { ossm: { prod: envCreds("prod"), pre_prod: envCreds("preprod") } },
  },
  uipath: {
    orchestratorUrl: "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
    bearer: "test-bearer",
  },
};

let dir: string;
let prevConfig: string | undefined;

const writeConfig = (config: unknown): void => {
  writeFileSync(join(dir, "config.json"), JSON.stringify(config));
  resetConfigCache();
};

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "copilot-mcp-order-search-"));
  prevConfig = process.env["COPILOT_MCP_CONFIG"];
  process.env["COPILOT_MCP_CONFIG"] = join(dir, "config.json");
  writeConfig(FIXTURE);
});

afterAll(() => {
  if (prevConfig === undefined) delete process.env["COPILOT_MCP_CONFIG"];
  else process.env["COPILOT_MCP_CONFIG"] = prevConfig;
  resetConfigCache();
  rmSync(dir, { recursive: true, force: true });
});

let calls: { method: string; url: string; body: unknown }[];
let responses: Response[];
const realFetch = globalThis.fetch;

beforeEach(() => {
  calls = [];
  responses = [];
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    calls.push({
      method: init?.method ?? "GET",
      url: String(input),
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    });
    return responses.shift() ?? new Response("{}", { status: 200 });
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

const json = (obj: unknown, status = 200): Response =>
  new Response(JSON.stringify(obj), { status });

describe("searchOrders", () => {
  test("logs in, POSTs /orders/filter with the built body, and maps rows", async () => {
    responses.push(json({ token: "jwt" })); // login
    responses.push(
      json({
        data: [
          {
            orderUid: "u1",
            status: "forReview",
            insurance: { name: "OPTUM CARE NETWORK" },
            referredFacility: { name: "Regal", NPI: "111" },
          },
        ],
        totalNumberOfElements: 1,
      }),
    );
    const out = await searchOrders({
      env: "prod",
      profile: "ossm",
      insurances: ["OPTUM CARE NETWORK"],
    });
    expect(calls.length).toBe(2);
    expect(calls[1]?.url).toBe("https://be.prod.example.com/api/v1/orders/filter");
    const body = calls[1]?.body as Record<string, unknown>;
    expect(body["insurances"]).toBe('["OPTUM CARE NETWORK"]');
    expect(body["type"]).toBe("Outbound Referral");
    expect(out.count).toBe(1);
    expect(out.totalNumberOfElements).toBe(1);
    expect(out.rows[0]?.referredTo).toEqual({ name: "Regal", npi: "111", external: undefined });
  });
});

describe("getOrderCategoryStats", () => {
  test("logs in and POSTs /orders/category/stats with no paging fields", async () => {
    responses.push(json({ token: "jwt" })); // login
    responses.push(json({ "For Review": { new: 2 } }));
    const out = await getOrderCategoryStats({ env: "pre_prod", profile: "ossm" });
    expect(calls[1]?.url).toBe("https://be.preprod.example.com/api/v1/orders/category/stats");
    const body = calls[1]?.body as Record<string, unknown>;
    expect(body).not.toHaveProperty("pageSize");
    expect(body).not.toHaveProperty("pageNumber");
    expect(out).toEqual({ "For Review": { new: 2 } });
  });
});
