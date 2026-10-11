import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../shared/expected-error.js";
import {
  _resetApexClient,
  apexDashboardLink,
  apexDownload,
  apexPages,
  apexRequest,
  resolveFolderId,
} from "./client.js";
import { ApexApiError } from "./errors.js";
import { type ApexCall, FOLDER_IDS, fakeJwt, json, useApexStub } from "./test-support.js";

const bearerOf = (c: ApexCall | undefined): string | undefined => c?.headers["authorization"];
const params = (c: ApexCall | undefined): URLSearchParams =>
  new URL(`https://x${c?.path ?? ""}`).searchParams;
const caught = async (p: Promise<unknown>): Promise<unknown> =>
  p.then(
    () => undefined,
    (e) => e,
  );
const loginReturning = (...jwts: string[]) => {
  let n = 0;
  return () =>
    json({}, 200, {
      "Set-Cookie": `access_token=${jwts[Math.min(n++, jwts.length - 1)]}; HttpOnly`,
    });
};

describe("login and token", () => {
  const apex = useApexStub();

  test("logs in once with the configured account and sends the cookie JWT as Bearer", async () => {
    const jwt = fakeJwt();
    apex.route("POST", "/auth/login", loginReturning(jwt));
    apex.route("GET", "/jobs/", () => json({ id: "j1" }));
    await apexRequest("prod", "GET", "/jobs/j1");
    await apexRequest("prod", "GET", "/jobs/j2");
    const logins = apex.callsTo("POST", "/auth/login");
    expect(logins.length).toBe(1);
    expect(logins[0]?.body).toEqual({ username: "tester", password: "pw" });
    expect(apex.callsTo("GET", "/jobs/").map(bearerOf)).toEqual([`Bearer ${jwt}`, `Bearer ${jwt}`]);
  });

  test("a login with no access_token cookie is an ApexApiError, not a silent unauthenticated call", async () => {
    apex.route("POST", "/auth/login", () => json({ user: {} }, 200, { "Set-Cookie": "other=1" }));
    const err = await caught(apexRequest("prod", "GET", "/jobs/j1"));
    expect(err).toBeInstanceOf(ApexApiError);
    expect((err as ApexApiError).path).toBe("/auth/login");
    expect((err as ApexApiError).message).toMatch(/no access_token cookie/);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(0);
  });

  test("a rejected login surfaces the server's detail", async () => {
    apex.route("POST", "/auth/login", () => json({ detail: "Invalid credentials" }, 401));
    const err = await caught(apexRequest("prod", "GET", "/jobs/j1"));
    expect((err as ApexApiError).status).toBe(401);
    expect((err as ApexApiError).message).toContain("Invalid credentials");
    expect(apex.callsTo("POST", "/auth/login").length).toBe(1);
  });

  test("concurrent first calls share one login and one folder lookup", async () => {
    apex.route("GET", "/jobs/", () => json({}));
    await Promise.all(Array.from({ length: 6 }, () => apexRequest("prod", "GET", "/jobs/j")));
    expect(apex.callsTo("POST", "/auth/login").length).toBe(1);
    expect(apex.callsTo("GET", "/folders").length).toBe(1);
  });

  test("a JWT expiring inside the 60s skew is not reused; a long-lived one is", async () => {
    apex.route("GET", "/jobs/", () => json({}));
    const soon = Math.floor(Date.now() / 1000) + 30;
    apex.route("POST", "/auth/login", loginReturning(fakeJwt(soon)));
    await apexRequest("prod", "GET", "/jobs/a");
    await apexRequest("prod", "GET", "/jobs/b");
    // never reused: every authenticated request (folders lookup + 2 reads) logged in afresh
    const logins = apex.callsTo("POST", "/auth/login").length;
    expect(logins).toBe(3);
    expect(apex.calls.length - logins).toBe(3);

    _resetApexClient();
    apex.calls.length = 0;
    apex.route("POST", "/auth/login", loginReturning(fakeJwt(soon + 3_600)));
    await apexRequest("prod", "GET", "/jobs/a");
    await apexRequest("prod", "GET", "/jobs/b");
    expect(apex.callsTo("POST", "/auth/login").length).toBe(1);
  });

  test("a 401 re-logs in exactly once and retries with the fresh token", async () => {
    const [old, fresh] = [fakeJwt(), fakeJwt(Math.floor(Date.now() / 1000) + 90_000)];
    apex.route("POST", "/auth/login", loginReturning(old, fresh));
    apex.route("GET", "/jobs/", (c) =>
      bearerOf(c) === `Bearer ${fresh}` ? json({ id: "j1" }) : json({ detail: "expired" }, 401),
    );
    expect(await apexRequest("prod", "GET", "/jobs/j1")).toEqual({ id: "j1" });
    expect(apex.callsTo("POST", "/auth/login").length).toBe(2);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(2);
  });

  test("a second consecutive 401 is reported, not retried forever", async () => {
    apex.route("GET", "/jobs/", () => json({ detail: "nope" }, 401));
    const err = await caught(apexRequest("prod", "GET", "/jobs/j1"));
    expect((err as ApexApiError).status).toBe(401);
    expect(apex.callsTo("POST", "/auth/login").length).toBe(2);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(2);
  });
});

describe("folder scoping", () => {
  const apex = useApexStub();
  const ok = () => json({});

  test("every request carries the env's folder id (prod default, pre_prod test folder)", async () => {
    apex.route("GET", "/jobs/", ok);
    apex.route("POST", "/queues/", ok);
    await apexRequest("prod", "GET", "/jobs/a");
    await apexRequest("pre_prod", "GET", "/jobs/a");
    await apexRequest("pre_prod", "POST", "/queues/q/items", { body: { reference: "r" } });
    const ids = [...apex.callsTo("GET", "/jobs/"), ...apex.callsTo("POST", "/queues/")].map(
      (c) => c.headers["x-folder-id"],
    );
    expect(ids).toEqual([
      FOLDER_IDS["Authorization"],
      FOLDER_IDS["Authorization Test"],
      FOLDER_IDS["Authorization Test"],
    ]);
  });

  test("a read-side folder override wins over the env default", async () => {
    apex.route("GET", "/jobs/", ok);
    await apexRequest("prod", "GET", "/jobs/a", { folder: "Eligibility Check" });
    expect(apex.callsTo("GET", "/jobs/")[0]?.headers["x-folder-id"]).toBe(
      FOLDER_IDS["Eligibility Check"],
    );
  });

  test("an unknown folder name lists the available ones and sends nothing", async () => {
    apex.route("GET", "/jobs/", ok);
    const err = await caught(apexRequest("prod", "GET", "/jobs/a", { folder: "Nope" }));
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toContain("'Nope' not found");
    expect((err as Error).message).toContain(
      "Authorization, Authorization Test, Eligibility Check",
    );
    expect(apex.callsTo("GET", "/jobs/").length).toBe(0);
  });

  test("the folder listing is fetched once and a failed fetch is not cached", async () => {
    let fail = true;
    apex.route("GET", "/folders", () =>
      fail
        ? json({ detail: "boom" }, 500)
        : json({ items: [{ id: FOLDER_IDS["Authorization"], name: "Authorization" }] }),
    );
    expect(await caught(resolveFolderId("prod"))).toBeInstanceOf(ApexApiError);
    fail = false;
    expect(await resolveFolderId("prod")).toEqual({
      id: FOLDER_IDS["Authorization"] as string,
      name: "Authorization",
    });
    await resolveFolderId("prod");
    expect(apex.callsTo("GET", "/folders").length).toBe(2);
  });
});

describe("write gate", () => {
  const apex = useApexStub();

  test.each([
    "POST",
    "PATCH",
    "DELETE",
  ] as const)("%s on prod is refused before any fetch", async (m) => {
    const err = await caught(apexRequest("prod", m, "/queues/q/items", { body: {} }));
    expect(err).toBeInstanceOf(ExpectedError);
    expect((err as Error).message).toMatch(/pre_prod-only/);
    expect(apex.calls.length).toBe(0);
  });

  test("a write with a folder override is refused before any fetch", async () => {
    const err = await caught(
      apexRequest("pre_prod", "POST", "/jobs", { body: {}, folder: "Authorization" }),
    );
    expect((err as Error).message).toMatch(/never take a folder override/);
    expect(apex.calls.length).toBe(0);
  });

  test("a pre_prod write posts the JSON body to the test folder", async () => {
    apex.route("POST", "/queues/", () => json({ id: "item-1" }, 201));
    expect(
      await apexRequest("pre_prod", "POST", "/queues/q/items", { body: { reference: "r" } }),
    ).toEqual({
      id: "item-1",
    });
    const post = apex.callsTo("POST", "/queues/")[0];
    expect(post?.body).toEqual({ reference: "r" });
    expect(post?.headers["content-type"]).toBe("application/json");
    expect(post?.headers["x-folder-id"]).toBe(FOLDER_IDS["Authorization Test"]);
  });

  test("an empty 2xx body (e.g. a 204) is null", async () => {
    apex.route("POST", "/jobs/", () => new Response(null, { status: 204 }));
    expect(await apexRequest("pre_prod", "POST", "/jobs/j1/cancel", { body: {} })).toBeNull();
  });
});

describe("retries", () => {
  const apex = useApexStub();
  const reset = (): never => {
    throw new TypeError("fetch failed");
  };

  test("a GET survives two connection resets (3 attempts)", async () => {
    let n = 0;
    apex.route("GET", "/jobs/", () => (++n < 3 ? reset() : json({ id: "j1" })));
    expect(await apexRequest("prod", "GET", "/jobs/j1")).toEqual({ id: "j1" });
    expect(n).toBe(3);
  });

  test("a GET that resets every time gives up after 3 attempts with the network error", async () => {
    apex.route("GET", "/jobs/", reset);
    expect(await caught(apexRequest("prod", "GET", "/jobs/j1"))).toBeInstanceOf(TypeError);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(3);
  });

  test("a POST is never retried on a reset", async () => {
    apex.route("POST", "/queues/", reset);
    expect(
      await caught(apexRequest("pre_prod", "POST", "/queues/q/items", { body: {} })),
    ).toBeInstanceOf(TypeError);
    expect(apex.callsTo("POST", "/queues/").length).toBe(1);
  });

  test("a GET 429 waits Retry-After and retries; gives up after 3 retries", async () => {
    let n = 0;
    apex.route("GET", "/jobs/", () =>
      ++n === 1 ? json({ detail: "slow" }, 429, { "Retry-After": "0" }) : json({}),
    );
    await apexRequest("prod", "GET", "/jobs/j1");
    expect(n).toBe(2);

    apex.route("GET", "/jobs/", () => json({ detail: "slow" }, 429, { "Retry-After": "0" }));
    const err = await caught(apexRequest("prod", "GET", "/jobs/j1"));
    expect((err as ApexApiError).status).toBe(429);
    expect(apex.callsTo("GET", "/jobs/").length).toBe(2 + 4);
  });

  test("a write 429 is not retried", async () => {
    apex.route("POST", "/queues/", () => json({ detail: "slow" }, 429, { "Retry-After": "0" }));
    const err = await caught(apexRequest("pre_prod", "POST", "/queues/q/items", { body: {} }));
    expect((err as ApexApiError).status).toBe(429);
    expect(apex.callsTo("POST", "/queues/").length).toBe(1);
  });
});

describe("error messages", () => {
  const apex = useApexStub();
  const failWith = async (status: number, body: unknown): Promise<ApexApiError> => {
    apex.route("GET", "/jobs/", () =>
      typeof body === "string" ? new Response(body, { status }) : json(body, status),
    );
    return (await caught(apexRequest("prod", "GET", "/jobs/j1"))) as ApexApiError;
  };

  test("a string detail becomes the message, with status/method/path on the error", async () => {
    const e = await failWith(404, { detail: "Job not found" });
    expect(e).toBeInstanceOf(ExpectedError);
    expect([e.status, e.method, e.path]).toEqual([404, "GET", "/jobs/j1"]);
    expect(e.message).toBe("Apex GET /jobs/j1 -> 404: Job not found");
  });

  test("a pydantic detail list is flattened to loc: msg pairs", async () => {
    const e = await failWith(422, {
      detail: [
        {
          loc: ["query", "page_size"],
          msg: "ensure this value is less than or equal to 200",
          type: "x",
        },
        { loc: ["body", "reference"], msg: "field required", type: "y" },
      ],
    });
    expect(e.message).toContain(
      "query.page_size: ensure this value is less than or equal to 200; body.reference: field required",
    );
  });

  test("a long detail is capped at 300 characters", async () => {
    const e = await failWith(500, { detail: "x".repeat(1_000) });
    expect(e.message).toBe(`Apex GET /jobs/j1 -> 500: ${"x".repeat(300)}…`);
  });

  test("a 403 adds the permission hint; a non-JSON error body is still reported", async () => {
    expect((await failWith(403, { detail: "Forbidden" })).message).toMatch(
      /Forbidden — the Apex account lacks the role/,
    );
    expect((await failWith(502, "bad gateway")).message).toContain("bad gateway");
  });
});

describe("apexPages", () => {
  const apex = useApexStub();
  const page = (n: number, totalPages: number, size: number, rows: number) =>
    json({
      items: Array.from({ length: rows }, (_, i) => ({ id: `p${n}-${i}` })),
      total: totalPages * size,
      page: n,
      page_size: size,
      total_pages: totalPages,
    });

  test("clamps page_size to 200 and walks pages until total_pages", async () => {
    apex.route("GET", "/jobs", (c) => page(Number(params(c).get("page")), 3, 200, 200));
    const rows = await apexPages<{ id: string }>(
      "prod",
      "/jobs",
      { page_size: "500", status: "failed" },
      10_000,
    );
    expect(rows.length).toBe(600);
    const calls = apex.callsTo("GET", "/jobs");
    expect(calls.map((c) => params(c).get("page"))).toEqual(["1", "2", "3"]);
    expect(calls.every((c) => params(c).get("page_size") === "200")).toBe(true);
    expect(calls.every((c) => params(c).get("status") === "failed")).toBe(true);
  });

  test("stops at max mid-page and asks for no more rows than it needs", async () => {
    apex.route("GET", "/jobs", (c) => page(Number(params(c).get("page")), 9, 7, 7));
    const rows = await apexPages("prod", "/jobs", {}, 7);
    expect(rows.length).toBe(7);
    expect(apex.callsTo("GET", "/jobs").length).toBe(1);
    expect(params(apex.callsTo("GET", "/jobs")[0]).get("page_size")).toBe("7");
  });

  test("returns the rows of a bare-array route, truncated to max", async () => {
    apex.route("GET", "/jobs/j1/artifacts", () => json([{ id: "a" }, { id: "b" }, { id: "c" }]));
    expect(await apexPages("prod", "/jobs/j1/artifacts", {}, 2)).toEqual([
      { id: "a" },
      { id: "b" },
    ]);
    expect(apex.callsTo("GET", "/jobs/j1/artifacts").length).toBe(1);
  });

  test("an empty page ends the walk even if total_pages says more", async () => {
    apex.route("GET", "/jobs", (c) => page(Number(params(c).get("page")), 5, 200, 0));
    expect(await apexPages("prod", "/jobs", {}, 1_000)).toEqual([]);
    expect(apex.callsTo("GET", "/jobs").length).toBe(1);
  });
});

describe("apexDownload", () => {
  const apex = useApexStub();
  const URL_PATH = "/api/v1/jobs/j1/artifacts/a1";

  test("forwards Range, returns bytes + content-range, sends Bearer but no folder header", async () => {
    apex.route(
      "GET",
      "/jobs/j1/artifacts/a1",
      () =>
        new Response(new Uint8Array([1, 2, 3]), {
          status: 206,
          headers: { "content-range": "bytes 0-2/10" },
        }),
    );
    const r = await apexDownload("prod", URL_PATH, { range: "bytes=0-2" });
    expect(r).toEqual({
      status: 206,
      bytes: new Uint8Array([1, 2, 3]),
      contentRange: "bytes 0-2/10",
    });
    const call = apex.callsTo("GET", "/jobs/j1/artifacts/a1")[0];
    expect(call?.headers["range"]).toBe("bytes=0-2");
    expect(call?.headers["authorization"]).toMatch(/^Bearer /);
    expect(call?.headers["x-folder-id"]).toBeUndefined();
    expect(apex.callsTo("GET", "/folders").length).toBe(0);
  });

  test("a full 200 has no content-range; a non-2xx throws ApexApiError with a relative path", async () => {
    apex.route("GET", "/jobs/j1/artifacts/a1", () => new Response("hello", { status: 200 }));
    expect((await apexDownload("prod", URL_PATH)).contentRange).toBeNull();
    apex.route("GET", "/jobs/j1/artifacts/a1", () => json({ detail: "gone" }, 404));
    const err = await caught(apexDownload("prod", URL_PATH));
    expect((err as ApexApiError).path).toBe("/jobs/j1/artifacts/a1");
    expect((err as ApexApiError).status).toBe(404);
  });

  test.each([
    "https://evil.example.com/api/v1/x",
    "/jobs/j1/artifacts/a1",
    "/api/v1/../admin",
  ])("refuses %s without sending the Bearer token anywhere", async (url) => {
    expect(await caught(apexDownload("prod", url))).toBeInstanceOf(ExpectedError);
    expect(apex.calls.length).toBe(0);
  });
});

describe("apexDashboardLink", () => {
  const apex = useApexStub();
  test("joins the configured base URL and the path, making no request", () => {
    expect(apexDashboardLink("/jobs/j1")).toBe("https://apex.example.com/jobs/j1");
    expect(apex.calls.length).toBe(0);
  });
});

describe("apexDashboardLink when unconfigured", () => {
  useApexStub({ apex: null });
  test("is the empty string", () => {
    expect(apexDashboardLink("/jobs/j1")).toBe("");
  });
});

describe("bodyless error responses", () => {
  const apex = useApexStub();
  test("a bodyless 502 still surfaces as an ApexApiError with its status", async () => {
    apex.route("GET", "/jobs/", () => new Response(null, { status: 502 }));
    const err = await caught(apexRequest("prod", "GET", "/jobs/"));
    expect(err).toBeInstanceOf(ApexApiError);
    expect((err as ApexApiError).status).toBe(502);
    expect((err as ApexApiError).message).toContain("HTTP 502");
  });
  test("a bodyless 403 keeps the permission hint", async () => {
    apex.route("GET", "/jobs/", () => new Response(null, { status: 403 }));
    const err = await caught(apexRequest("prod", "GET", "/jobs/"));
    expect(err).toBeInstanceOf(ApexApiError);
    expect((err as ApexApiError).status).toBe(403);
    expect((err as ApexApiError).message).toContain("the Apex account lacks the role");
  });
});
