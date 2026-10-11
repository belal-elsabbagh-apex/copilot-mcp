import { describe, expect, test } from "bun:test";
import { resolvePortal } from "../mcp/reference.js";
import { memberFilename, toPortalRef } from "./portal.js";

describe("memberFilename", () => {
  test("first-last, lowercased and trimmed", () => {
    expect(memberFilename({ MemberFirstName: " Jane ", MemberLastName: "DOE" })).toBe(
      "jane-doe.yaml",
    );
  });

  test("one missing name part leaves no dangling dash", () => {
    expect(memberFilename({ MemberLastName: "Doe" })).toBe("doe.yaml");
    expect(memberFilename({ MemberFirstName: "Jane" })).toBe("jane.yaml");
  });

  test("falls back to the full name (commas dropped, spaces dashed), then to 'unknown'", () => {
    expect(memberFilename({ MemberFullName: "Doe, Jane Q" })).toBe("doe-jane-q.yaml");
    expect(memberFilename({})).toBe("unknown.yaml");
    expect(memberFilename({ MemberFirstName: 5, MemberLastName: null })).toBe("unknown.yaml");
  });
});

describe("toPortalRef", () => {
  test("projects exactly the five portal fields for a hit", () => {
    const ref = toPortalRef(resolvePortal("PMG"));
    expect(Object.keys(ref).sort()).toEqual([
      "buildArtifact",
      "family",
      "key",
      "matched",
      "portalDir",
    ]);
    expect(ref).toMatchObject({ matched: true, key: "PMG", buildArtifact: "pmg-auth.cjs" });
  });

  test("a miss carries the empty sentinels", () => {
    expect(toPortalRef(resolvePortal("NO SUCH PORTAL"))).toEqual({
      matched: false,
      key: "",
      portalDir: "",
      buildArtifact: "",
      family: "Standalone",
    });
  });
});
