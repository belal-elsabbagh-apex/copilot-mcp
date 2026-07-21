import { afterEach, describe, expect, test } from "bun:test";
import {
  categoryStats,
  fetchOrder,
  filterOrders,
  type HttpClient,
  type HttpResponse,
  makeClient,
  type ReqBody,
  reqWithRefresh,
  submitOrder,
  verify,
} from "./copilot-client.js";

interface RecordedCall {
  method: string;
  path: string;
  body?: ReqBody;
}

// A client that queues canned responses and records every call made against it —
// lets the /orders/filter + /orders/category/stats wrappers be tested without any
// real HTTP or config.
function stubClient(responses: HttpResponse[]): { client: HttpClient; calls: RecordedCall[] } {
  const calls: RecordedCall[] = [];
  const queue = [...responses];
  const client: HttpClient = {
    base: "http://stub.invalid",
    req: async (method: string, path: string, body?: ReqBody) => {
      calls.push({ method, path, ...(body ? { body } : {}) });
      return queue.shift() ?? { status: 200, data: {}, text: "{}" };
    },
  };
  return { client, calls };
}

describe("filterOrders", () => {
  test("POSTs the body as-is and returns rows + total", async () => {
    const { client, calls } = stubClient([
      { status: 200, data: { data: [{ orderUid: "u1" }], totalNumberOfElements: 42 }, text: "" },
    ]);
    const out = await filterOrders(client, { pageSize: 50, pageNumber: 1 });
    expect(calls[0]?.method).toBe("POST");
    expect(calls[0]?.path).toBe("/api/v1/orders/filter");
    expect(calls[0]?.body).toEqual({ json: { pageSize: 50, pageNumber: 1 } });
    expect(out).toEqual({ rows: [{ orderUid: "u1" }], total: 42 });
  });

  test("omits total when the response doesn't carry a numeric totalNumberOfElements", async () => {
    const { client } = stubClient([{ status: 200, data: { data: [] }, text: "" }]);
    expect(await filterOrders(client, { pageSize: 50, pageNumber: 1 })).toEqual({ rows: [] });
  });

  test("throws on a non-2xx response", async () => {
    const { client } = stubClient([{ status: 500, data: {}, text: "boom" }]);
    await expect(filterOrders(client, { pageSize: 50, pageNumber: 1 })).rejects.toThrow(
      /\/orders\/filter failed 500/,
    );
  });
});

describe("categoryStats", () => {
  test("POSTs to /orders/category/stats and passes the response through", async () => {
    const { client, calls } = stubClient([
      { status: 200, data: { "For Review": { new: 3 } }, text: "" },
    ]);
    const out = await categoryStats(client, { type: "Outbound Referral" });
    expect(calls[0]?.method).toBe("POST");
    expect(calls[0]?.path).toBe("/api/v1/orders/category/stats");
    expect(calls[0]?.body).toEqual({ json: { type: "Outbound Referral" } });
    expect(out).toEqual({ "For Review": { new: 3 } });
  });

  test("throws on a non-2xx response", async () => {
    const { client } = stubClient([{ status: 404, data: {}, text: "nope" }]);
    await expect(categoryStats(client, {})).rejects.toThrow(/\/orders\/category\/stats failed 404/);
  });
});

describe("fetchOrder", () => {
  test("returns the first row from the envelope", async () => {
    const { client } = stubClient([
      { status: 200, data: { data: [{ orderUid: "u1" }] }, text: "" },
    ]);
    expect(await fetchOrder(client, "u1")).toEqual({ orderUid: "u1" });
  });

  test("throws when the order isn't found in this env", async () => {
    const { client } = stubClient([{ status: 200, data: { data: [] }, text: "" }]);
    await expect(fetchOrder(client, "missing")).rejects.toThrow(/not found in this env/);
  });

  test("throws on a non-2xx response", async () => {
    const { client } = stubClient([{ status: 500, data: {}, text: "boom" }]);
    await expect(fetchOrder(client, "u1")).rejects.toThrow(/\/orders\/filter failed 500/);
  });
});

describe("verify", () => {
  test("returns normalized detail when the order is found", async () => {
    const { client } = stubClient([
      { status: 200, data: { data: [{ orderUid: "u1", status: "forReview" }] }, text: "" },
    ]);
    expect((await verify(client, "u1"))?.status).toBe("forReview");
  });

  test("returns null when the order isn't found", async () => {
    const { client } = stubClient([{ status: 200, data: { data: [] }, text: "" }]);
    expect(await verify(client, "missing")).toBeNull();
  });

  test("returns null (not a throw) on a non-2xx response", async () => {
    const { client } = stubClient([{ status: 500, data: {}, text: "boom" }]);
    expect(await verify(client, "u1")).toBeNull();
  });
});

describe("reqWithRefresh", () => {
  test("passes a successful response straight through, no refresh call", async () => {
    const { client, calls } = stubClient([{ status: 200, data: { ok: true }, text: "" }]);
    const r = await reqWithRefresh(client, "PUT", "/api/v1/orders/u1", { json: { a: 1 } });
    expect(r.status).toBe(200);
    expect(calls).toHaveLength(1);
    expect(calls[0]).toEqual({
      method: "PUT",
      path: "/api/v1/orders/u1",
      body: { json: { a: 1 } },
    });
  });

  test("on a 403 missing_token, refreshes then retries once and returns the retry's result", async () => {
    const { client, calls } = stubClient([
      { status: 403, data: {}, text: "missing_token" },
      { status: 200, data: { ok: true }, text: "" },
    ]);
    const r = await reqWithRefresh(client, "PUT", "/api/v1/orders/u1", { json: { a: 1 } });
    expect(r.status).toBe(200);
    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      "PUT /api/v1/orders/u1",
      "GET /api/v1/physician/refresh",
      "PUT /api/v1/orders/u1",
    ]);
  });

  test("a 403 for any other reason is not retried", async () => {
    const { client, calls } = stubClient([{ status: 403, data: {}, text: "forbidden" }]);
    const r = await reqWithRefresh(client, "PUT", "/api/v1/orders/u1", { json: { a: 1 } });
    expect(r.status).toBe(403);
    expect(calls).toHaveLength(1);
  });
});

describe("submitOrder", () => {
  test("retries once on 403 missing_token, then verifies", async () => {
    const { client, calls } = stubClient([
      { status: 403, data: {}, text: "missing_token" }, // 1st submit attempt
      { status: 200, data: {}, text: "" }, // GET refresh (response unused)
      { status: 200, data: {}, text: "" }, // retried submit, succeeds
      { status: 200, data: { data: [{ orderUid: "u1", status: "inProgress" }] }, text: "" }, // verify
    ]);
    const clientTagged: HttpClient = { ...client, env: "pre_prod" };
    const result = await submitOrder(clientTagged, "u1");
    expect(result?.status).toBe("inProgress");
    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      "POST /api/v1/orders/u1/submit",
      "GET /api/v1/physician/refresh",
      "POST /api/v1/orders/u1/submit",
      "POST /api/v1/orders/filter",
    ]);
  });
});

describe("makeClient req()", () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  test("parses a JSON body", async () => {
    globalThis.fetch = (async (_input: string | URL | Request, _init?: RequestInit) =>
      new Response(JSON.stringify({ ok: true }), { status: 200 })) as typeof fetch;
    const r = await makeClient("http://stub.invalid").req("GET", "/x");
    expect(r.data).toEqual({ ok: true });
  });

  test("falls back to the raw text for a non-JSON body", async () => {
    globalThis.fetch = (async (_input: string | URL | Request, _init?: RequestInit) =>
      new Response("not json", { status: 200 })) as typeof fetch;
    const r = await makeClient("http://stub.invalid").req("GET", "/x");
    expect(r.data).toBe("not json");
  });

  test("an empty body parses to undefined, same as uipath.ts's client (safeJsonParse)", async () => {
    globalThis.fetch = (async (_input: string | URL | Request, _init?: RequestInit) =>
      new Response("", { status: 204 })) as typeof fetch;
    const r = await makeClient("http://stub.invalid").req("DELETE", "/x");
    expect(r.data).toBeUndefined();
  });
});
