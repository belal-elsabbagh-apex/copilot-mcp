import { describe, expect, test } from "bun:test";
import {
  categoryStats,
  fetchOrder,
  filterOrders,
  type HttpClient,
  type HttpResponse,
  type ReqBody,
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
