import { describe, expect, test } from "bun:test";
import {
  buildOrderNameCreateBody,
  type OrderNameTree,
  type PreTypeRefs,
  planOrderNameSync,
  remapNamedRefs,
  remapPayersMap,
} from "./orders.js";

describe("sync_settings orders domain (pure)", () => {
  const payerMap = new Map([["prod-payer-A", "pre-payer-A"]]);

  test("remapPayersMap remaps map key + inner payerUid, drops unmatched by name", () => {
    const { payers, dropped } = remapPayersMap(
      {
        "prod-payer-A": { payerUid: "prod-payer-A", Name: "Anthem", submissionMode: "portal" },
        "prod-payer-X": { payerUid: "prod-payer-X", Name: "Humana" },
      },
      payerMap,
    );
    expect(payers).toEqual({
      "pre-payer-A": { payerUid: "pre-payer-A", Name: "Anthem", submissionMode: "portal" },
    });
    expect(dropped).toEqual(["Humana"]); // reported by payer name, not UID
  });

  test("remapNamedRefs resolves names to pre-prod UIDs and reports misses", () => {
    const byName = new Map([["RadNet", "pre-fac-1"]]);
    const { uids, dropped } = remapNamedRefs(
      [{ name: "RadNet" }, { name: "Nowhere Imaging" }],
      "name",
      byName,
    );
    expect(uids).toEqual(["pre-fac-1"]);
    expect(dropped).toEqual(["Nowhere Imaging"]);
  });

  const refs: PreTypeRefs = {
    facilityUidByName: new Map([["RadNet", "pre-fac-1"]]),
    authSubCatUidByName: new Map([["Prior Auth", "pre-auth-1"]]),
    referralSubCatUidByName: new Map([["Consultation Report", "pre-ref-1"]]),
  };

  const prodRow = {
    nameUid: "prod-name-1",
    name: "MRI Brain",
    referredFacilities: [
      { referredFacilityUid: "prod-fac-1", name: "RadNet" },
      { referredFacilityUid: "prod-fac-2", name: "Nowhere Imaging" }, // no pre match -> dropped
    ],
    outboundReferralOrderTypeNameCPTCodes: [
      {
        codeUid: "prod-code-1",
        code: "70551",
        units: null,
        treatments: null,
        description: "",
        payers: { "prod-payer-A": { payerUid: "prod-payer-A", Name: "Anthem" } },
      },
    ],
    authSubCategories: [{ subCategoryName: "Prior Auth", subCategoryUid: "prod-auth-1" }],
    referralSubCategories: [
      { subCategoryName: "Consultation Report", subCategoryUid: "prod-ref-1" },
    ],
    mandatoryICDCodes: [{ code: "G43.9" }],
  };

  test("buildOrderNameCreateBody remaps every reference by name and reports drops", () => {
    const { body, droppedPayers, droppedRefs } = buildOrderNameCreateBody(prodRow, refs, payerMap);
    expect(body["name"]).toBe("MRI Brain");
    expect(body["facilitiesUids"]).toEqual(["pre-fac-1"]);
    expect(body["authSubCategoryUids"]).toEqual(["pre-auth-1"]);
    expect(body["referralSubCategoryUids"]).toEqual(["pre-ref-1"]);
    expect(body["mandatoryICDCodes"]).toEqual([{ code: "G43.9" }]);
    // CPT: own codeUid stripped, payers map remapped
    const cpts = body["CPTCodes"] as Record<string, unknown>[];
    expect(cpts[0]).not.toHaveProperty("codeUid");
    expect(cpts[0]?.["code"]).toBe("70551");
    expect(cpts[0]?.["payers"]).toEqual({
      "pre-payer-A": { payerUid: "pre-payer-A", Name: "Anthem" },
    });
    expect(droppedPayers).toEqual([]);
    expect(droppedRefs).toEqual(["facility 'Nowhere Imaging'"]);
  });

  test("planOrderNameSync creates prod-only names, skips existing + missing types", () => {
    const prod: OrderNameTree = {
      types: [
        { typeUid: "prod-t1", name: "Imaging", names: [prodRow, { name: "Existing" }] },
        { typeUid: "prod-t2", name: "Lab", names: [{ name: "CBC" }] },
      ],
    };
    const pre: OrderNameTree = {
      types: [{ typeUid: "pre-t1", name: "Imaging", names: [{ name: "Existing" }] }],
    };
    const preRefs = new Map([["Imaging", refs]]);
    const { actions, skipped } = planOrderNameSync(prod, pre, preRefs, payerMap);

    expect(actions).toHaveLength(1);
    const a = actions[0];
    expect(a?.op).toBe("create");
    expect(a?.itemKind).toBe("order");
    expect(a?.itemName).toBe("MRI Brain");
    expect(a?.method).toBe("POST");
    expect(a?.path).toBe("/api/v1/settings/orders/outbound/types/pre-t1/names");
    expect(a?.warnings?.[0]).toMatch(/Nowhere Imaging/);

    // "Existing" is in both -> no action; "Lab" type missing in pre-prod -> skipped
    expect(skipped).toHaveLength(1);
    expect(skipped[0]?.typeName).toBe("Lab");
    expect(skipped[0]?.reason).toMatch(/missing in pre-prod/);
  });
});
