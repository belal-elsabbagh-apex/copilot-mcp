import { describe, expect, test } from "bun:test";
import {
  auditPayerLinks,
  buildMergedSpecialityBody,
  buildSpecialityCreateBody,
  cleanReferralForWrite,
  planSpecialitySync,
  remapPayerLinks,
  type SpecialityTree,
} from "./specialities.js";

describe("sync_settings (pure)", () => {
  const payerMap = new Map([
    ["prod-payer-A", "pre-payer-A"],
    ["prod-payer-B", "pre-payer-B"],
  ]);

  test("remapPayerLinks rewrites matched UIDs, drops unmatched, keeps ref-less links", () => {
    const { links, dropped } = remapPayerLinks(
      [
        { payerUid: "prod-payer-A", providerId: "1", linked: true },
        { payerUid: "prod-payer-X", providerId: "2", linked: true }, // no pre match
        { providerId: "3" }, // no payer ref
      ],
      payerMap,
    );
    expect(links).toEqual([
      { payerUid: "pre-payer-A", providerId: "1", linked: true },
      { providerId: "3" },
    ]);
    expect(dropped).toEqual(["prod-payer-X"]);
  });

  test("buildSpecialityCreateBody strips own UIDs, remaps payer links, preserves fields", () => {
    const { body, droppedPayers } = buildSpecialityCreateBody(
      {
        name: "MRI",
        specialityUid: "prod-sp-1",
        referredFacilities: [
          {
            referredFacilityUid: "prod-f-1",
            name: "Fac A",
            placeOfService: "11 - Office",
            payersProviderId: [{ payerUid: "prod-payer-A", providerId: "1", linked: true }],
          },
        ],
      },
      payerMap,
    );
    expect(droppedPayers).toEqual([]);
    expect(body["name"]).toBe("MRI");
    expect(body).not.toHaveProperty("specialityUid");
    const fac = (body["referredFacilities"] as Record<string, unknown>[])[0] ?? {};
    expect(fac).not.toHaveProperty("referredFacilityUid"); // server assigns
    expect(fac["placeOfService"]).toBe("11 - Office"); // meaningful field kept
    expect(fac["payersProviderId"]).toEqual([
      { payerUid: "pre-payer-A", providerId: "1", linked: true },
    ]);
  });

  test("buildSpecialityCreateBody reports dropped payer links", () => {
    const { body, droppedPayers } = buildSpecialityCreateBody(
      {
        name: "MRI",
        referredFacilities: [
          {
            name: "Fac A",
            payersProviderId: [
              { payerUid: "prod-payer-A", providerId: "1" },
              { payerUid: "prod-payer-UNMAPPED", providerId: "9" },
            ],
          },
        ],
      },
      payerMap,
    );
    expect(droppedPayers).toEqual(["prod-payer-UNMAPPED"]);
    const fac = (body["referredFacilities"] as Record<string, unknown>[])[0] ?? {};
    expect(fac["payersProviderId"]).toEqual([{ payerUid: "pre-payer-A", providerId: "1" }]);
  });

  test("buildMergedSpecialityBody keeps existing pre-prod facilities and appends only new ones", () => {
    const { body } = buildMergedSpecialityBody(
      {
        name: "CT",
        specialityUid: "pre-sp-ct",
        referredFacilities: [
          {
            referredFacilityUid: "pre-f-1",
            name: "Existing Fac",
            payersProviderId: [{ payerUid: "pre-payer-A", providerId: "9", linked: true }],
          },
        ],
      },
      [
        {
          referredFacilityUid: "prod-f-2",
          name: "New Fac",
          payersProviderId: [{ payerUid: "prod-payer-B", providerId: "1", linked: true }],
        },
      ],
      [],
      payerMap,
    );
    const facs = body["referredFacilities"] as Record<string, unknown>[];
    expect(facs).toHaveLength(2);
    // existing kept (uid + already-valid pre-prod payer link preserved)
    expect(facs[0]).toEqual({
      referredFacilityUid: "pre-f-1",
      name: "Existing Fac",
      payersProviderId: [{ payerUid: "pre-payer-A", providerId: "9", linked: true }],
    });
    // appended one is cleaned: own uid dropped, payer remapped prod->pre
    expect(facs[1]).toEqual({
      name: "New Fac",
      payersProviderId: [{ payerUid: "pre-payer-B", providerId: "1", linked: true }],
    });
  });

  // Regression: a captured HAR of the Settings UI's "edit specialty" PUT showed the real
  // request drops specialityUid/specialityName/createdAt/updatedAt from EXISTING facility rows
  // (while keeping their own referredFacilityUid) — fields the GET crawl echoes onto every row.
  // apply_settings_sync was resending them verbatim, which the BE 400'd on every merge action.
  test("buildMergedSpecialityBody strips echoed parent/timestamp fields from existing facilities", () => {
    const { body } = buildMergedSpecialityBody(
      {
        name: "Orthopedics",
        specialityUid: "pre-sp-ortho",
        referredFacilities: [
          {
            referredFacilityUid: "pre-f-1",
            name: "Existing Fac",
            specialityUid: "pre-sp-ortho", // echoed parent uid — must be stripped
            specialityName: "Orthopedics", // echoed parent name — must be stripped
            createdAt: "2026-01-01T00:00:00.000Z", // noise — must be stripped
            updatedAt: "2026-01-02T00:00:00.000Z", // noise — must be stripped
            payersProviderId: [{ payerUid: "pre-payer-A", providerId: "9", linked: true }],
          },
        ],
      },
      [],
      [],
      payerMap,
    );
    const facs = body["referredFacilities"] as Record<string, unknown>[];
    expect(facs[0]).toEqual({
      referredFacilityUid: "pre-f-1",
      name: "Existing Fac",
      payersProviderId: [{ payerUid: "pre-payer-A", providerId: "9", linked: true }],
    });
  });

  // Full field set verified two ways: a captured HAR of the Settings UI's "edit referred
  // facility" PUT (specialities/{uid}, full-replace body) and a live GET .../specialities
  // read. Field NAMES/shape below are real; values are synthetic (no real
  // provider/facility PII belongs in a committed fixture). This pins that
  // cleanReferralForWrite's generic "copy everything except uid-suffixed keys +
  // createdAt/updatedAt/updatedBy/specialityName/source" strategy actually preserves every
  // other field — not just the couple the smaller tests above use. `source` is included
  // because a live POST .../specialities with it present 400'd with "referredFacilities[0].
  // source is not allowed" (see issue #2) — the write schema rejects it outright.
  const FULL_FACILITY = {
    referredFacilityUid: "00000000-0000-0000-0000-000000000f01", // own uid — must be stripped
    name: "Synthetic Rehab Center",
    NPI: "1000000001",
    faxNumber: "(555) 010-0001",
    isFaxVerified: true,
    phone: "(555) 010-0002",
    address: "1 Test Way, Sampletown, CA 90000",
    zipCode: "90000",
    placeOfService: "11 - Office",
    comment: "",
    external: false,
    isSpeciality: false,
    includeReferralForm: true,
    isAdditionAllowed: false,
    milesThreshold: null,
    source: null, // BE-rejected echo field — must be stripped
    createdAt: "2026-06-28T16:12:37.801Z", // noise — must be stripped
    updatedAt: "2026-06-28T16:16:07.011Z", // noise — must be stripped
    specialityUid: "00000000-0000-0000-0000-000000000s01", // own uid — must be stripped
    specialityName: null,
    payersProviderId: [{ payerUid: "prod-payer-A", providerId: "42", linked: true }],
  };

  test("cleanReferralForWrite preserves every referred-facility field except uid/timestamp/echo noise", () => {
    const { item, droppedPayers } = cleanReferralForWrite(FULL_FACILITY, payerMap);
    expect(droppedPayers).toEqual([]);
    const {
      referredFacilityUid,
      createdAt,
      updatedAt,
      specialityUid,
      specialityName,
      source,
      ...rest
    } = FULL_FACILITY;
    expect(item).toEqual({
      ...rest,
      payersProviderId: [{ payerUid: "pre-payer-A", providerId: "42", linked: true }],
    });
  });

  // referredProviders carry one extra field (emrProviderId) and no `comment`; the create
  // body builder must preserve those just as generically as it does for facilities.
  // Values are synthetic, matching FULL_FACILITY's approach above.
  const FULL_PROVIDER = {
    referredProviderUid: "00000000-0000-0000-0000-000000000p01", // own uid — must be stripped
    name: "Synthetic Provider",
    NPI: null,
    faxNumber: null,
    isFaxVerified: false,
    phone: null,
    address: null,
    zipCode: null,
    placeOfService: "11 - Office",
    emrProviderId: null,
    external: true,
    isSpeciality: false,
    includeReferralForm: false,
    isAdditionAllowed: false,
    milesThreshold: null,
    source: null, // BE-rejected echo field — must be stripped
    createdAt: "2026-02-07T08:07:46.754Z", // noise — must be stripped
    updatedAt: "2026-02-07T08:07:46.754Z", // noise — must be stripped
    specialityUid: null,
    specialityName: null,
    payersProviderId: [],
  };

  test("buildSpecialityCreateBody preserves the full referredProviders field set (emrProviderId included)", () => {
    const { body } = buildSpecialityCreateBody(
      { name: "PCP", referredFacilities: [], referredProviders: [FULL_PROVIDER] },
      payerMap,
    );
    const prov = (body["referredProviders"] as Record<string, unknown>[])[0] ?? {};
    const {
      referredProviderUid,
      createdAt,
      updatedAt,
      specialityUid,
      specialityName,
      source,
      ...rest
    } = FULL_PROVIDER;
    expect(prov).toEqual(rest);
  });

  test("planSpecialitySync emits create + merge, skips unmatched types and in-sync specialties", () => {
    const prod: SpecialityTree = {
      types: [
        {
          typeUid: "prod-t1",
          name: "Imaging",
          specialities: [
            {
              name: "MRI", // not in pre -> create
              specialityUid: "prod-sp-mri",
              referredFacilities: [
                {
                  name: "Fac1",
                  payersProviderId: [
                    { payerUid: "prod-payer-A" },
                    { payerUid: "prod-payer-UNMAPPED" },
                  ],
                },
              ],
            },
            {
              name: "CT", // in pre, has a prod-only facility -> merge
              specialityUid: "prod-sp-ct",
              referredFacilities: [{ name: "Shared" }, { name: "ProdOnlyFac" }],
            },
            {
              name: "US", // identical in pre -> no action
              specialityUid: "prod-sp-us",
              referredFacilities: [{ name: "OnlyShared" }],
            },
          ],
        },
        {
          typeUid: "prod-t2",
          name: "Lab", // type missing in pre -> skipped
          specialities: [{ name: "Blood" }],
        },
      ],
    };
    const pre: SpecialityTree = {
      types: [
        {
          typeUid: "pre-t1",
          name: "Imaging",
          specialities: [
            { name: "CT", specialityUid: "pre-sp-ct", referredFacilities: [{ name: "Shared" }] },
            {
              name: "US",
              specialityUid: "pre-sp-us",
              referredFacilities: [{ name: "OnlyShared" }],
            },
          ],
        },
      ],
    };

    const { actions, skipped } = planSpecialitySync(prod, pre, payerMap);

    const create = actions.find((a) => a.op === "create");
    expect(create?.itemName).toBe("MRI");
    expect(create?.method).toBe("POST");
    expect(create?.path).toBe("/api/v1/settings/orders/outbound/types/pre-t1/specialities");
    expect(create?.warnings?.[0]).toMatch(/prod-payer-UNMAPPED/);

    const merge = actions.find((a) => a.op === "merge");
    expect(merge?.itemName).toBe("CT");
    expect(merge?.method).toBe("PUT");
    expect(merge?.path).toBe("/api/v1/settings/specialities/pre-sp-ct");
    const mergedFacilities = (merge?.body["referredFacilities"] ?? []) as unknown[];
    expect(mergedFacilities.map((f) => (f as { name: string }).name)).toEqual([
      "Shared",
      "ProdOnlyFac",
    ]);

    // US is identical -> no action for it
    expect(actions.filter((a) => a.itemName === "US")).toHaveLength(0);
    expect(actions).toHaveLength(2);

    expect(skipped).toHaveLength(1);
    expect(skipped[0]?.typeName).toBe("Lab");
    expect(skipped[0]?.reason).toMatch(/missing in pre-prod/);
  });

  // Regression (issue #3): exact-string name matching alone missed a pre-prod duplicate that
  // only differs by casing, so the merge body appended the SAME NPI twice under two
  // name-casings — the BE 400s on the duplicate NPI within the speciality.
  test("planSpecialitySync drops a same-NPI facility that only differs by name casing, with a warning", () => {
    const prod: SpecialityTree = {
      types: [
        {
          typeUid: "prod-t1",
          name: "Medical Supplies",
          specialities: [
            {
              name: "DME",
              specialityUid: "prod-sp-dme",
              referredFacilities: [{ name: "Verio Healthcare Inc - DME", NPI: "1821467804" }],
            },
          ],
        },
      ],
    };
    const pre: SpecialityTree = {
      types: [
        {
          typeUid: "pre-t1",
          name: "Medical Supplies",
          specialities: [
            {
              name: "DME",
              specialityUid: "pre-sp-dme",
              referredFacilities: [
                {
                  referredFacilityUid: "pre-f-verio",
                  name: "VERIO HEALTHCARE INC - DME",
                  NPI: "1821467804",
                },
              ],
            },
          ],
        },
      ],
    };

    const { actions, skipped } = planSpecialitySync(prod, pre, payerMap);

    // nothing genuinely new to merge -> no write action, just a reported skip
    expect(actions).toHaveLength(0);
    expect(skipped).toHaveLength(1);
    expect(skipped[0]?.specialityName).toBe("DME");
    expect(skipped[0]?.reason).toMatch(/1821467804/);
    expect(skipped[0]?.reason).toMatch(/VERIO HEALTHCARE INC - DME/);
  });

  // Regression (issue #3): two "new" facilities collide by NPI with pre-prod records that
  // already exist under a DIFFERENT speciality entirely; a third facility is genuinely new.
  // The collisions must be dropped (with a warning) without blocking the genuinely-new one.
  test("planSpecialitySync drops cross-speciality NPI collisions but keeps the genuinely-new facility", () => {
    const prod: SpecialityTree = {
      types: [
        {
          typeUid: "prod-t1",
          name: "Follow-up Visit",
          specialities: [
            {
              name: "Orthopedics",
              specialityUid: "prod-sp-ortho",
              referredProviders: [
                { name: "Loren Tholcke", NPI: "1588191944" },
                { name: "Reza Roghani", NPI: "1386941219" },
                { name: "Omar Kadri", NPI: "1999999999" },
              ],
            },
          ],
        },
      ],
    };
    const pre: SpecialityTree = {
      types: [
        {
          typeUid: "pre-t1",
          name: "Follow-up Visit",
          specialities: [
            { name: "Orthopedics", specialityUid: "pre-sp-ortho", referredProviders: [] },
            {
              name: "Podiatry", // a DIFFERENT speciality, same tenant
              specialityUid: "pre-sp-podiatry",
              referredProviders: [
                { referredProviderUid: "pre-p-loren", name: "LOREN THOLCKE DO", NPI: "1588191944" },
              ],
            },
          ],
        },
        {
          typeUid: "pre-t2",
          name: "Consultation", // a DIFFERENT order type entirely
          specialities: [
            {
              name: "General",
              specialityUid: "pre-sp-general",
              referredProviders: [
                { referredProviderUid: "pre-p-reza", name: "REZA ROGHANI MD", NPI: "1386941219" },
              ],
            },
          ],
        },
      ],
    };

    const { actions } = planSpecialitySync(prod, pre, payerMap);

    expect(actions).toHaveLength(1);
    const merge = actions[0];
    expect(merge?.itemName).toBe("Orthopedics");
    const provs = (merge?.body["referredProviders"] ?? []) as Record<string, unknown>[];
    // only the genuinely-new provider survives -- collateral damage avoided
    expect(provs.map((p) => p["name"])).toEqual(["Omar Kadri"]);
    expect(merge?.warnings?.some((w) => w.includes("1588191944") && w.includes("Podiatry"))).toBe(
      true,
    );
    expect(
      merge?.warnings?.some((w) => w.includes("1386941219") && w.includes("Consultation")),
    ).toBe(true);
  });
});

describe("auditPayerLinks (pure)", () => {
  // Regression (issue #4): a facility that exists in both envs (matched by name) is never
  // touched by planSpecialitySync's additive create/merge logic, so payer-link drift on it was
  // invisible. Matching must be by payer NAME (uids are per-env and never expected to match),
  // never by payerUid.
  test("flags a payer link present in pre-prod but not prod, on a facility that matches by name", () => {
    const prod: SpecialityTree = {
      types: [
        {
          typeUid: "prod-t1",
          name: "Medical Supplies",
          specialities: [
            {
              name: "DME",
              specialityUid: "prod-sp-dme",
              referredFacilities: [{ name: "Body Basics Physical Therapy", payersProviderId: [] }],
            },
          ],
        },
      ],
    };
    const pre: SpecialityTree = {
      types: [
        {
          typeUid: "pre-t1",
          name: "Medical Supplies",
          specialities: [
            {
              name: "DME",
              specialityUid: "pre-sp-dme",
              referredFacilities: [
                {
                  name: "Body Basics Physical Therapy",
                  payersProviderId: [{ payerUid: "pre-payer-optum" }],
                },
              ],
            },
          ],
        },
      ],
    };
    const prodNameByUid = new Map<string, string>(); // prod has no links to resolve here
    const preNameByUid = new Map([["pre-payer-optum", "OPTUM CARE NETWORK"]]);

    const findings = auditPayerLinks(prod, pre, prodNameByUid, preNameByUid);

    expect(findings).toHaveLength(1);
    expect(findings[0]).toMatchObject({
      typeName: "Medical Supplies",
      specialityName: "DME",
      itemKind: "facility",
      itemName: "Body Basics Physical Therapy",
      extraInPreProd: ["OPTUM CARE NETWORK"],
      missingInPreProd: [],
      orphanedProdPayerUids: [],
      orphanedPreProdPayerUids: [],
    });
  });

  // Regression (issue #4): a payerUid on a shared facility/provider that resolves to no payer
  // in that env's own clinic-payers list is a dangling reference — invisible to diff_settings
  // (which strips uids) and to sync (additive only). Must be flagged even with no name-level
  // link drift otherwise.
  test("flags a payerUid that resolves to no payer name (orphaned reference)", () => {
    const prod: SpecialityTree = {
      types: [
        {
          typeUid: "prod-t1",
          name: "Follow-up Visit",
          specialities: [
            {
              name: "Orthopedics",
              specialityUid: "prod-sp-ortho",
              referredProviders: [{ name: "Steven Kelley", payersProviderId: [] }],
            },
          ],
        },
      ],
    };
    const pre: SpecialityTree = {
      types: [
        {
          typeUid: "pre-t1",
          name: "Follow-up Visit",
          specialities: [
            {
              name: "Orthopedics",
              specialityUid: "pre-sp-ortho",
              referredProviders: [
                {
                  name: "Steven Kelley",
                  payersProviderId: [{ payerUid: "pre-payer-dangling" }],
                },
              ],
            },
          ],
        },
      ],
    };
    // pre-payer-dangling deliberately absent from preNameByUid -- it resolves to no payer
    const findings = auditPayerLinks(prod, pre, new Map(), new Map());

    expect(findings).toHaveLength(1);
    expect(findings[0]).toMatchObject({
      itemKind: "provider",
      itemName: "Steven Kelley",
      extraInPreProd: [],
      missingInPreProd: [],
      orphanedProdPayerUids: [],
      orphanedPreProdPayerUids: ["pre-payer-dangling"],
    });
  });

  test("reports nothing when a shared facility's payer links match by name on both sides", () => {
    const prod: SpecialityTree = {
      types: [
        {
          typeUid: "prod-t1",
          name: "Imaging",
          specialities: [
            {
              name: "MRI",
              specialityUid: "prod-sp-mri",
              referredFacilities: [
                { name: "RadNet", payersProviderId: [{ payerUid: "prod-payer-A" }] },
              ],
            },
          ],
        },
      ],
    };
    const pre: SpecialityTree = {
      types: [
        {
          typeUid: "pre-t1",
          name: "Imaging",
          specialities: [
            {
              name: "MRI",
              specialityUid: "pre-sp-mri",
              referredFacilities: [
                { name: "RadNet", payersProviderId: [{ payerUid: "pre-payer-A" }] },
              ],
            },
          ],
        },
      ],
    };
    const prodNameByUid = new Map([["prod-payer-A", "Anthem"]]);
    const preNameByUid = new Map([["pre-payer-A", "Anthem"]]);

    expect(auditPayerLinks(prod, pre, prodNameByUid, preNameByUid)).toEqual([]);
  });

  test("skips types/specialities not present in both envs (already reported by planSpecialitySync)", () => {
    const prod: SpecialityTree = {
      types: [{ typeUid: "prod-t1", name: "Lab", specialities: [{ name: "Blood" }] }],
    };
    const pre: SpecialityTree = { types: [] };
    expect(auditPayerLinks(prod, pre, new Map(), new Map())).toEqual([]);
  });
});
