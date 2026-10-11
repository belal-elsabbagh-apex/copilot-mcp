import { describe, expect, test } from "bun:test";
import { FOLDER_IDS, json, useApexStub } from "../apex/test-support.js";
import { apexProbes, type DoctorCheck } from "./doctor.js";

const runApexProbes = (): Promise<DoctorCheck[]> => Promise.all(apexProbes().map((run) => run()));
const byName = (checks: DoctorCheck[], name: string): DoctorCheck | undefined =>
  checks.find((c) => c.name === name);

describe("apexProbes: configured from the file", () => {
  const apex = useApexStub();

  test("reports the source, a working login, and each env's folder id", async () => {
    const checks = await runApexProbes();
    expect(checks.map((c) => [c.name, c.ok])).toEqual([
      ["apex config", true],
      ["apex login", true],
      ["apex prod folder", true],
      ["apex pre_prod folder", true],
    ]);
    expect(byName(checks, "apex config")?.detail).toBe(
      "apiUrl from config, username from config, password from config",
    );
    expect(byName(checks, "apex prod folder")?.detail).toBe(
      `folder 'Authorization' -> ${FOLDER_IDS["Authorization"]}`,
    );
    expect(byName(checks, "apex pre_prod folder")?.detail).toBe(
      `folder 'Authorization Test' -> ${FOLDER_IDS["Authorization Test"]}`,
    );
    expect(apex.callsTo("POST", "/auth/login").length).toBe(1); // checks share one session
  });

  test("a rejected login fails the login check (and the folder checks that need it), never throws", async () => {
    apex.route("POST", "/auth/login", () => json({ detail: "Invalid credentials" }, 401));
    const checks = await runApexProbes();
    expect(byName(checks, "apex config")?.ok).toBe(true);
    expect(byName(checks, "apex login")?.ok).toBe(false);
    expect(byName(checks, "apex login")?.detail).toContain("Invalid credentials");
    expect(byName(checks, "apex prod folder")?.ok).toBe(false);
  });
});

describe("apexProbes: a folder that does not exist", () => {
  useApexStub({
    apex: {
      apiUrl: "https://apex.example.com",
      username: "tester",
      password: "pw",
      folderByEnv: { pre_prod: "Missing Folder" },
    },
  });

  test("fails only that env, naming the folders that do exist", async () => {
    const checks = await runApexProbes();
    expect(byName(checks, "apex prod folder")?.ok).toBe(true);
    const bad = byName(checks, "apex pre_prod folder");
    expect(bad?.ok).toBe(false);
    expect(bad?.detail).toContain("'Missing Folder' not found");
    expect(bad?.detail).toContain("Authorization, Authorization Test, Eligibility Check");
  });
});

describe("apexProbes: configured from the environment", () => {
  useApexStub({ apex: { username: "file-user" } });

  test("says which fields came from the environment", async () => {
    process.env["APEX_API_URL"] = "https://apex.example.com";
    process.env["APEX_PASSWORD"] = "pw";
    try {
      const checks = await runApexProbes();
      expect(byName(checks, "apex config")?.detail).toBe(
        "apiUrl from env, username from config, password from env",
      );
      expect(byName(checks, "apex login")?.ok).toBe(true);
    } finally {
      delete process.env["APEX_API_URL"];
      delete process.env["APEX_PASSWORD"];
    }
  });
});

describe("apexProbes: not configured at all", () => {
  const apex = useApexStub({ apex: null });

  test("is one passing 'optional' check and makes no request", async () => {
    const checks = await runApexProbes();
    expect(checks).toEqual([
      {
        name: "apex config",
        target: "apex",
        ok: true,
        detail: expect.stringContaining("not configured (optional)"),
      },
    ]);
    expect(apex.calls.length).toBe(0);
  });
});

describe("apexProbes: half configured", () => {
  const apex = useApexStub({ apex: { username: "only-a-user" } });

  test("fails, naming what is missing, and makes no request", async () => {
    const checks = await runApexProbes();
    expect(checks.length).toBe(1);
    expect(checks[0]?.ok).toBe(false);
    expect(checks[0]?.detail).toContain("missing apiUrl, password");
    expect(apex.calls.length).toBe(0);
  });
});
