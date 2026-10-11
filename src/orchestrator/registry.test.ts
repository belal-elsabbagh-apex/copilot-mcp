import { describe, expect, test } from "bun:test";
import { useApexStub } from "../apex/test-support.js";
import { backendFor, searchableBackends } from "./registry.js";

const names = (): string[] => searchableBackends().map((b) => b.name);

describe("searchableBackends: Apex not configured at all", () => {
  useApexStub({ apex: null });

  test("is skipped from search-everything", () => {
    expect(names()).toEqual(["uipath"]);
  });

  test("an explicit backendFor('apex') still resolves", () => {
    expect(backendFor("apex").name).toBe("apex");
  });
});

describe("searchableBackends: Apex configured", () => {
  useApexStub();

  test("is searched", () => {
    expect(names()).toEqual(["uipath", "apex"]);
  });
});

describe("searchableBackends: Apex half-configured", () => {
  useApexStub({ apex: { apiUrl: "https://apex.example.com" } });

  test("is still searched so its ApexNotConfiguredError surfaces", () => {
    expect(names()).toEqual(["uipath", "apex"]);
  });
});
