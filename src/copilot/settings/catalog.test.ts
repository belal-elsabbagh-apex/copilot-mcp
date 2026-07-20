import { describe, expect, test } from "bun:test";
import { listSettingSections, SETTINGS_CATALOG, settingTags } from "./catalog.js";

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

  test("every section has a non-empty tags array", () => {
    for (const s of SETTINGS_CATALOG) {
      expect(Array.isArray(s.tags), `${s.key} tags`).toBe(true);
      expect(s.tags.length, `${s.key} tags`).toBeGreaterThan(0);
    }
  });

  test("the provider/facility directory sections share the 'providers' tag", () => {
    for (const key of [
      "rendering-providers",
      "referred-providers",
      "referred-facilities",
      "referring-entities",
    ]) {
      expect(SETTINGS_CATALOG.find((x) => x.key === key)?.tags, key).toContain("providers");
    }
  });
});

describe("settingTags + listSettingSections", () => {
  test("settingTags returns the distinct tags", () => {
    const tags = settingTags();
    expect(new Set(tags).size).toBe(tags.length); // distinct
    expect(tags).toContain("orders");
    expect(tags).toContain("providers");
  });

  test("lists all sections with key/label/tags/kind/derived", () => {
    const { tags, sections } = listSettingSections({});
    expect(sections.length).toBe(SETTINGS_CATALOG.length);
    expect(tags).toEqual(settingTags());
    const spec = sections.find((s) => s.key === "specialties");
    expect(spec).toMatchObject({ label: expect.any(String), tags: ["orders"], derived: true });
    expect(sections.find((s) => s.key === "locations")?.derived).toBe(false);
  });

  test("tag filter narrows the listing", () => {
    const { sections } = listSettingSections({ tag: "providers" });
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
    expect(withEmr.tags).toContain("emr");
  });

  test("unknown tag throws a listing error", () => {
    expect(() => listSettingSections({ tag: "nope" })).toThrow(/unknown tag/);
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
