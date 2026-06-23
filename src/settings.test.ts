import { describe, expect, test } from "bun:test";
import {
  diffList,
  diffObjects,
  type FieldDiff,
  listSettingSections,
  SETTINGS_CATALOG,
  settingGroups,
  stripNoise,
} from "./settings.js";

const NO_IGNORE = new Set<string>();
const has = (diffs: FieldDiff[], path: string) => diffs.some((d) => d.path === path);

describe("stripNoise", () => {
  test("drops *Uid and timestamp keys recursively", () => {
    const cleaned = stripNoise(
      {
        ruleUid: "abc",
        name: "Consultation Referral",
        createdAt: "2026-03-31T19:27:40.691Z",
        updatedAt: "2026-03-31T19:27:40.691Z",
        nested: { specialityUid: "x", value: 3 },
      },
      NO_IGNORE,
    );
    expect(cleaned).toEqual({ name: "Consultation Referral", nested: { value: 3 } });
  });

  test("applies per-section ignore fields", () => {
    const cleaned = stripNoise(
      { location: "OSSM COR", clinicLogo: "https://cdn.prod.example/x.png", faxNumber: "1" },
      new Set(["clinicLogo"]),
    );
    expect(cleaned).toEqual({ location: "OSSM COR", faxNumber: "1" });
  });

  test("sorts arrays so order does not matter", () => {
    const a = stripNoise({ cats: ["Orders", "Billing", "Other"] }, NO_IGNORE);
    const b = stripNoise({ cats: ["Other", "Orders", "Billing"] }, NO_IGNORE);
    expect(JSON.stringify(a)).toBe(JSON.stringify(b));
  });
});

describe("diffObjects", () => {
  test("finds a flipped flag and an added key (on normalized inputs)", () => {
    const prod = stripNoise({ sendFaxAfterAuth: true, extractAllICDCodes: false }, NO_IGNORE);
    const pre = stripNoise(
      { sendFaxAfterAuth: false, extractAllICDCodes: false, newFlag: true },
      NO_IGNORE,
    );
    const diffs = diffObjects(prod, pre);
    expect(has(diffs, "sendFaxAfterAuth")).toBe(true);
    expect(has(diffs, "newFlag")).toBe(true);
    expect(has(diffs, "extractAllICDCodes")).toBe(false);
    const flag = diffs.find((d) => d.path === "sendFaxAfterAuth");
    expect(flag?.prod).toBe(true);
    expect(flag?.preprod).toBe(false);
  });

  test("identical objects produce no diffs", () => {
    const v = stripNoise({ a: 1, b: { c: [3, 1, 2] } }, NO_IGNORE);
    expect(diffObjects(v, v)).toEqual([]);
  });
});

describe("diffList with a matchKey", () => {
  const section = { matchKey: "name" as const };

  test("matches by name and ignores differing UIDs (cross-env noise fix)", () => {
    const prod = [{ typeUid: "prod-uid-1", name: "Follow-up Visit", autoSubmit: false }];
    const pre = [{ typeUid: "pre-uid-9", name: "Follow-up Visit", autoSubmit: true }];
    const d = diffList(prod, pre, section);
    expect(d.onlyInProd).toEqual([]);
    expect(d.onlyInPreProd).toEqual([]);
    expect(d.changed).toHaveLength(1);
    expect(d.changed[0]?.key).toBe("Follow-up Visit");
    expect(has(d.changed[0]?.diffs ?? [], "autoSubmit")).toBe(true);
    // the UID difference must NOT surface as a diff
    expect(has(d.changed[0]?.diffs ?? [], "typeUid")).toBe(false);
  });

  test("reports items present in only one env", () => {
    const prod = [{ name: "A" }, { name: "B" }];
    const pre = [{ name: "A" }, { name: "C" }];
    const d = diffList(prod, pre, section);
    expect(d.onlyInProd).toEqual([{ name: "B" }]);
    expect(d.onlyInPreProd).toEqual([{ name: "C" }]);
    expect(d.changed).toEqual([]);
  });
});

describe("diffList without a matchKey (content-set)", () => {
  test("reports added/removed normalized entries, ignoring UID noise", () => {
    const prod = [
      { ruleUid: "p1", category: "Clearance" },
      { ruleUid: "p2", category: "Orders" },
    ];
    const pre = [
      { ruleUid: "q1", category: "Clearance" },
      { ruleUid: "q9", category: "Billing" },
    ];
    const d = diffList(prod, pre, {});
    // "Clearance" exists in both once UIDs are stripped -> not reported.
    expect(d.onlyInProd).toEqual([{ category: "Orders" }]);
    expect(d.onlyInPreProd).toEqual([{ category: "Billing" }]);
    expect(d.changed).toEqual([]);
  });
});

describe("SETTINGS_CATALOG", () => {
  test("section keys are unique", () => {
    const keys = SETTINGS_CATALOG.map((s) => s.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  test("every list section has a matchKey or is intentionally content-set", () => {
    for (const s of SETTINGS_CATALOG) {
      if (s.kind === "list" && !s.matchKey) {
        expect(s.key.startsWith("document-")).toBe(true);
      }
    }
  });

  test("crawled sections (specialties / referred-*) carry a derive fn", () => {
    for (const key of ["specialties", "referred-providers", "referred-facilities"]) {
      const s = SETTINGS_CATALOG.find((x) => x.key === key);
      expect(typeof s?.derive).toBe("function");
    }
    // rendering-providers is a plain endpoint, not crawled
    expect(SETTINGS_CATALOG.find((x) => x.key === "rendering-providers")?.derive).toBeUndefined();
  });

  test("every section has a non-empty group", () => {
    for (const s of SETTINGS_CATALOG) {
      expect(typeof s.group, `${s.key} group`).toBe("string");
      expect(s.group.length, `${s.key} group`).toBeGreaterThan(0);
    }
  });

  test("the provider/facility directory sections share the 'providers' group", () => {
    for (const key of [
      "rendering-providers",
      "referred-providers",
      "referred-facilities",
      "referring-entities",
    ]) {
      expect(SETTINGS_CATALOG.find((x) => x.key === key)?.group, key).toBe("providers");
    }
  });
});

describe("settingGroups + listSettingSections", () => {
  test("settingGroups returns the distinct groups", () => {
    const groups = settingGroups();
    expect(new Set(groups).size).toBe(groups.length); // distinct
    expect(groups).toContain("orders");
    expect(groups).toContain("providers");
  });

  test("lists all sections with key/label/group/kind/derived", () => {
    const { groups, sections } = listSettingSections({});
    expect(sections.length).toBe(SETTINGS_CATALOG.length);
    expect(groups).toEqual(settingGroups());
    const spec = sections.find((s) => s.key === "specialties");
    expect(spec).toMatchObject({ label: expect.any(String), group: "orders", derived: true });
    expect(sections.find((s) => s.key === "locations")?.derived).toBe(false);
  });

  test("group filter narrows the listing", () => {
    const { sections } = listSettingSections({ group: "providers" });
    expect(sections.map((s) => s.key).sort()).toEqual(
      [
        "referred-facilities",
        "referred-providers",
        "referring-entities",
        "rendering-providers",
      ].sort(),
    );
  });

  test("emr opt-in section appears only when emr is given", () => {
    expect(listSettingSections({}).sections.some((s) => s.key === "emr-details")).toBe(false);
    const withEmr = listSettingSections({ emr: "NEXTGEN" });
    expect(withEmr.sections.some((s) => s.key === "emr-details")).toBe(true);
    expect(withEmr.groups).toContain("emr");
  });

  test("unknown group throws a listing error", () => {
    expect(() => listSettingSections({ group: "nope" })).toThrow(/unknown group/);
  });
});
