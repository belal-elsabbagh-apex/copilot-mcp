import { describe, expect, test } from "bun:test";
import { listSettingSections, SETTINGS_CATALOG, settingGroups } from "./catalog.js";

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

  test("sections filter narrows to exact keys", () => {
    const { sections } = listSettingSections({ sections: ["locations", "orders-outbound"] });
    expect(sections.map((s) => s.key).sort()).toEqual(["locations", "orders-outbound"]);
  });

  test("unknown section key throws with the known keys listed", () => {
    expect(() => listSettingSections({ sections: ["nope"] })).toThrow(/unknown section/);
  });

  test("list sections expose their matchKey; object sections do not", () => {
    const { sections } = listSettingSections({});
    expect(sections.find((s) => s.key === "locations")?.matchKey).toBe("name");
    expect(sections.find((s) => s.key === "orders-outbound")?.matchKey).toBeUndefined();
  });
});
