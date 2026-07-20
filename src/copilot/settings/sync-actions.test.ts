import { describe, expect, test } from "bun:test";
import { ExpectedError } from "../../mcp/feedback.js";
import {
  actionId,
  assignActionIds,
  selectPlannedActions,
  summarizeActionBody,
  toActionSummary,
} from "./sync-actions.js";
import type { SyncAction } from "./types.js";

// Minimal SyncAction fixture; the body is only inspected by summarizeActionBody.
const mkAction = (over: Partial<SyncAction>): SyncAction => ({
  section: "specialties",
  op: "create",
  itemKind: "speciality",
  typeName: "Imaging",
  itemName: "MRI",
  method: "POST",
  path: "/api/v1/settings/orders/outbound/types/pre-t1/specialities",
  body: { name: "MRI", referredFacilities: [{ name: "Fac1" }, { name: "Fac2" }] },
  ...over,
});

describe("action ids", () => {
  test("actionId is stable and distinct per semantic field", () => {
    const a = mkAction({});
    expect(actionId(a)).toBe("specialties:create:Imaging:MRI");
    expect(actionId(a)).toBe(actionId(mkAction({}))); // same fields -> same id
    expect(actionId(mkAction({ op: "merge" }))).not.toBe(actionId(a));
    expect(actionId(mkAction({ itemName: "CT" }))).not.toBe(actionId(a));
    expect(actionId(mkAction({ typeName: "Lab" }))).not.toBe(actionId(a));
    expect(actionId(mkAction({ section: "orders" }))).not.toBe(actionId(a));
  });

  test("assignActionIds keeps plan order and disambiguates duplicates deterministically", () => {
    const planned = assignActionIds([
      mkAction({}),
      mkAction({ itemName: "CT" }),
      mkAction({}), // exact semantic duplicate of the first
    ]);
    expect(planned.map((a) => a.id)).toEqual([
      "specialties:create:Imaging:MRI",
      "specialties:create:Imaging:CT",
      "specialties:create:Imaging:MRI#2",
    ]);
    expect(planned.map((a) => a.itemName)).toEqual(["MRI", "CT", "MRI"]);
  });
});

describe("selectPlannedActions", () => {
  const planned = assignActionIds([mkAction({}), mkAction({ itemName: "CT" })]);

  test("all=true selects everything", () => {
    const r = selectPlannedActions(planned, { all: true });
    expect(r.selected).toHaveLength(2);
    expect(r.notSelected).toHaveLength(0);
    expect(r.unmatchedIds).toEqual([]);
  });

  test("actionIds selects the subset and reports unknown ids", () => {
    const r = selectPlannedActions(planned, {
      actionIds: ["specialties:create:Imaging:CT", "specialties:create:Imaging:GONE"],
    });
    expect(r.selected.map((a) => a.itemName)).toEqual(["CT"]);
    expect(r.notSelected.map((a) => a.itemName)).toEqual(["MRI"]);
    expect(r.unmatchedIds).toEqual(["specialties:create:Imaging:GONE"]);
  });

  test("neither filter is rejected as an ExpectedError", () => {
    expect(() => selectPlannedActions(planned, {})).toThrow(ExpectedError);
    expect(() => selectPlannedActions(planned, { actionIds: [] })).toThrow(/exactly one/);
  });

  test("both filters are rejected", () => {
    expect(() => selectPlannedActions(planned, { actionIds: ["x"], all: true })).toThrow(
      ExpectedError,
    );
  });
});

describe("action summaries", () => {
  test("summarizes create/merge speciality bodies by counts", () => {
    expect(summarizeActionBody(mkAction({}))).toBe(
      "create speciality 'MRI' (2 facilities, 0 providers)",
    );
    expect(
      summarizeActionBody(
        mkAction({
          op: "merge",
          body: { name: "MRI", referredFacilities: [{}], referredProviders: [{}, {}] },
        }),
      ),
    ).toBe("merge speciality 'MRI' -> 1 facilities, 2 providers (existing + prod-only additions)");
  });

  test("summarizes a create-order body by reference counts", () => {
    const order = mkAction({
      section: "orders",
      itemKind: "order",
      itemName: "MRI Brain",
      body: {
        name: "MRI Brain",
        CPTCodes: [{}],
        facilitiesUids: ["f1", "f2"],
        authSubCategoryUids: ["a1"],
        referralSubCategoryUids: [],
        mandatoryICDCodes: [],
      },
    });
    expect(summarizeActionBody(order)).toBe(
      "create order 'MRI Brain' (1 CPTs, 2 facilities, 1 auth + 0 referral sub-categories)",
    );
  });

  test("toActionSummary includes the body only when asked", () => {
    const [planned] = assignActionIds([mkAction({})]);
    if (!planned) throw new Error("expected a planned action");
    const slim = toActionSummary(planned, false);
    expect(slim.id).toBe("specialties:create:Imaging:MRI");
    expect(slim.summary).toMatch(/create speciality 'MRI'/);
    expect(slim).not.toHaveProperty("body");
    expect(toActionSummary(planned, true).body).toEqual(planned.body);
  });
});
