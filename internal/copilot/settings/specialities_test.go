package settings

import (
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

var specPayerMap = map[string]string{"prod-payer-A": "pre-payer-A", "prod-payer-B": "pre-payer-B"}

// tree builds a speciality tree from `[{typeUid,name,specialities:[...]}]`.
func tree(t *testing.T, src string) specialityTree {
	t.Helper()
	var out specialityTree
	for _, ty := range j(t, src).([]any) {
		c := crawledType{typeUID: shared.Str(ty, "typeUid"), name: shared.Str(ty, "name")}
		c.specialities = records(ty.(map[string]any)["specialities"])
		out.types = append(out.types, c)
	}
	return out
}

func TestRemapPayerLinksRewritesMatchedDropsUnmatchedKeepsRefLessLinks(t *testing.T) {
	links, dropped := remapPayerLinks(j(t, `[{"payerUid":"prod-payer-A","providerId":"1","linked":true},
		{"payerUid":"prod-payer-X","providerId":"2","linked":true},{"providerId":"3"}]`), specPayerMap)
	if got, want := asJSON(t, links), `[{"linked":true,"payerUid":"pre-payer-A","providerId":"1"},{"providerId":"3"}]`; got != want {
		t.Errorf("links = %s, want %s", got, want)
	}
	if !slices.Equal(dropped, []string{"prod-payer-X"}) {
		t.Errorf("dropped = %v", dropped)
	}
}

func TestRemapPayerLinksDoesNotMutateItsInput(t *testing.T) {
	in := j(t, `[{"payerUid":"prod-payer-A"}]`)
	remapPayerLinks(in, specPayerMap)
	if asJSON(t, in) != `[{"payerUid":"prod-payer-A"}]` {
		t.Errorf("input mutated: %s", asJSON(t, in))
	}
}

func TestBuildSpecialityCreateBodyStripsOwnUIDsRemapsPayerLinksPreservesFields(t *testing.T) {
	body, dropped := buildSpecialityCreateBody(j(t, `{"name":"MRI","specialityUid":"prod-sp-1","referredFacilities":[{
		"referredFacilityUid":"prod-f-1","name":"Fac A","placeOfService":"11 - Office",
		"payersProviderId":[{"payerUid":"prod-payer-A","providerId":"1","linked":true}]}]}`), specPayerMap)
	if len(dropped) != 0 {
		t.Errorf("dropped = %v", dropped)
	}
	want := `{"name":"MRI","referredFacilities":[{"name":"Fac A","payersProviderId":[{"linked":true,"payerUid":"pre-payer-A","providerId":"1"}],"placeOfService":"11 - Office"}]}`
	if got := asJSON(t, body); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
}

func TestBuildSpecialityCreateBodyReportsDroppedPayerLinks(t *testing.T) {
	body, dropped := buildSpecialityCreateBody(j(t, `{"name":"MRI","referredFacilities":[{"name":"Fac A","payersProviderId":[
		{"payerUid":"prod-payer-A","providerId":"1"},{"payerUid":"prod-payer-UNMAPPED","providerId":"9"}]}]}`), specPayerMap)
	if !slices.Equal(dropped, []string{"prod-payer-UNMAPPED"}) {
		t.Errorf("dropped = %v", dropped)
	}
	fac := asJSON(t, body["referredFacilities"])
	if fac != `[{"name":"Fac A","payersProviderId":[{"payerUid":"pre-payer-A","providerId":"1"}]}]` {
		t.Errorf("facilities = %s", fac)
	}
	if _, has := body["referredProviders"]; has {
		t.Error("referredProviders must be omitted when the source speciality has none")
	}
}

func TestBuildMergedSpecialityBodyKeepsExistingFacilitiesAndAppendsOnlyNewOnes(t *testing.T) {
	body, _ := buildMergedSpecialityBody(
		j(t, `{"name":"CT","specialityUid":"pre-sp-ct","referredFacilities":[{"referredFacilityUid":"pre-f-1","name":"Existing Fac",
			"payersProviderId":[{"payerUid":"pre-payer-A","providerId":"9","linked":true}]}]}`),
		j(t, `[{"referredFacilityUid":"prod-f-2","name":"New Fac","payersProviderId":[{"payerUid":"prod-payer-B","providerId":"1","linked":true}]}]`).([]any),
		nil, specPayerMap)
	facs := body["referredFacilities"].([]any)
	if len(facs) != 2 {
		t.Fatalf("facilities = %d, want 2", len(facs))
	}
	// existing kept (uid + already-valid pre-prod payer link preserved)
	if got, want := asJSON(t, facs[0]), `{"name":"Existing Fac","payersProviderId":[{"linked":true,"payerUid":"pre-payer-A","providerId":"9"}],"referredFacilityUid":"pre-f-1"}`; got != want {
		t.Errorf("existing = %s\nwant     %s", got, want)
	}
	// appended one is cleaned: own uid dropped, payer remapped prod->pre
	if got, want := asJSON(t, facs[1]), `{"name":"New Fac","payersProviderId":[{"linked":true,"payerUid":"pre-payer-B","providerId":"1"}]}`; got != want {
		t.Errorf("new = %s\nwant %s", got, want)
	}
}

// Regression: a captured HAR of the Settings UI's "edit specialty" PUT showed the real request
// drops specialityUid/specialityName/createdAt/updatedAt from EXISTING facility rows (while
// keeping their own referredFacilityUid) — fields the GET crawl echoes onto every row.
// apply_settings_sync was resending them verbatim, which the BE 400'd on every merge action.
func TestBuildMergedSpecialityBodyStripsEchoedParentAndTimestampFieldsFromExistingRows(t *testing.T) {
	body, _ := buildMergedSpecialityBody(j(t, `{"name":"Orthopedics","specialityUid":"pre-sp-ortho","referredFacilities":[{
		"referredFacilityUid":"pre-f-1","name":"Existing Fac","specialityUid":"pre-sp-ortho","specialityName":"Orthopedics",
		"createdAt":"2026-01-01T00:00:00.000Z","updatedAt":"2026-01-02T00:00:00.000Z",
		"payersProviderId":[{"payerUid":"pre-payer-A","providerId":"9","linked":true}]}]}`), nil, nil, specPayerMap)
	want := `[{"name":"Existing Fac","payersProviderId":[{"linked":true,"payerUid":"pre-payer-A","providerId":"9"}],"referredFacilityUid":"pre-f-1"}]`
	if got := asJSON(t, body["referredFacilities"]); got != want {
		t.Errorf("facilities = %s\nwant       %s", got, want)
	}
}

// Full field set verified two ways: a captured HAR of the Settings UI's "edit referred facility"
// PUT (specialities/{uid}, full-replace body) and a live GET .../specialities read. Field NAMES/
// shape below are real; values are synthetic (no real provider/facility PII belongs in a committed
// fixture). Pins that cleanReferralForWrite's generic "copy everything except uid-suffixed keys +
// createdAt/updatedAt/updatedBy/specialityName/source" strategy preserves every other field.
// `source` is included because a live POST .../specialities with it present 400'd with
// "referredFacilities[0].source is not allowed" (issue #2).
const fullFacility = `{
	"referredFacilityUid":"00000000-0000-0000-0000-000000000f01","name":"Synthetic Rehab Center","NPI":"1000000001",
	"faxNumber":"(555) 010-0001","isFaxVerified":true,"phone":"(555) 010-0002","address":"1 Test Way, Sampletown, CA 90000",
	"zipCode":"90000","placeOfService":"11 - Office","comment":"","external":false,"isSpeciality":false,
	"includeReferralForm":true,"isAdditionAllowed":false,"milesThreshold":null,"source":null,
	"createdAt":"2026-06-28T16:12:37.801Z","updatedAt":"2026-06-28T16:16:07.011Z",
	"specialityUid":"00000000-0000-0000-0000-000000000s01","specialityName":null,
	"payersProviderId":[{"payerUid":"prod-payer-A","providerId":"42","linked":true}]}`

func TestCleanReferralForWritePreservesEveryFacilityFieldExceptUIDTimestampEchoNoise(t *testing.T) {
	item, dropped := cleanReferralForWrite(j(t, fullFacility), specPayerMap)
	if len(dropped) != 0 {
		t.Errorf("dropped = %v", dropped)
	}
	want := j(t, fullFacility).(map[string]any)
	for _, k := range []string{"referredFacilityUid", "createdAt", "updatedAt", "specialityUid", "specialityName", "source"} {
		delete(want, k)
	}
	want["payersProviderId"] = j(t, `[{"payerUid":"pre-payer-A","providerId":"42","linked":true}]`)
	if asJSON(t, item) != asJSON(t, want) {
		t.Errorf("item = %s\nwant %s", asJSON(t, item), asJSON(t, want))
	}
}

// referredProviders carry one extra field (emrProviderId) and no `comment`; the create body
// builder must preserve those just as generically as it does for facilities.
const fullProvider = `{
	"referredProviderUid":"00000000-0000-0000-0000-000000000p01","name":"Synthetic Provider","NPI":null,"faxNumber":null,
	"isFaxVerified":false,"phone":null,"address":null,"zipCode":null,"placeOfService":"11 - Office","emrProviderId":null,
	"external":true,"isSpeciality":false,"includeReferralForm":false,"isAdditionAllowed":false,"milesThreshold":null,
	"source":null,"createdAt":"2026-02-07T08:07:46.754Z","updatedAt":"2026-02-07T08:07:46.754Z",
	"specialityUid":null,"specialityName":null,"payersProviderId":[]}`

func TestBuildSpecialityCreateBodyPreservesTheFullProviderFieldSet(t *testing.T) {
	prov := j(t, fullProvider).(map[string]any)
	body, _ := buildSpecialityCreateBody(map[string]any{"name": "PCP", "referredFacilities": []any{}, "referredProviders": []any{prov}}, specPayerMap)
	want := j(t, fullProvider).(map[string]any)
	for _, k := range []string{"referredProviderUid", "createdAt", "updatedAt", "specialityUid", "specialityName", "source"} {
		delete(want, k)
	}
	got := body["referredProviders"].([]any)[0]
	if asJSON(t, got) != asJSON(t, want) {
		t.Errorf("provider = %s\nwant     %s", asJSON(t, got), asJSON(t, want))
	}
}

func TestPlanSpecialitySyncEmitsCreateAndMergeAndSkipsUnmatchedTypesAndInSyncSpecialties(t *testing.T) {
	prod := tree(t, `[
		{"typeUid":"prod-t1","name":"Imaging","specialities":[
			{"name":"MRI","specialityUid":"prod-sp-mri","referredFacilities":[{"name":"Fac1","payersProviderId":[{"payerUid":"prod-payer-A"},{"payerUid":"prod-payer-UNMAPPED"}]}]},
			{"name":"CT","specialityUid":"prod-sp-ct","referredFacilities":[{"name":"Shared"},{"name":"ProdOnlyFac"}]},
			{"name":"US","specialityUid":"prod-sp-us","referredFacilities":[{"name":"OnlyShared"}]}]},
		{"typeUid":"prod-t2","name":"Lab","specialities":[{"name":"Blood"}]}]`)
	pre := tree(t, `[{"typeUid":"pre-t1","name":"Imaging","specialities":[
		{"name":"CT","specialityUid":"pre-sp-ct","referredFacilities":[{"name":"Shared"}]},
		{"name":"US","specialityUid":"pre-sp-us","referredFacilities":[{"name":"OnlyShared"}]}]}]`)

	actions, skipped := planSpecialitySync(prod, pre, specPayerMap)

	if len(actions) != 2 {
		t.Fatalf("actions = %d, want 2 (US is identical -> none)", len(actions))
	}
	create, merge := actions[0], actions[1]
	if create.Op != "create" || create.ItemName != "MRI" || create.Method != "POST" ||
		create.Path != "/api/v1/settings/orders/outbound/types/pre-t1/specialities" ||
		len(create.Warnings) != 1 || !strings.Contains(create.Warnings[0], "prod-payer-UNMAPPED") {
		t.Errorf("create = %+v", create)
	}
	if merge.Op != "merge" || merge.ItemName != "CT" || merge.Method != "PUT" || merge.Path != "/api/v1/settings/specialities/pre-sp-ct" {
		t.Errorf("merge = %+v", merge)
	}
	var names []string
	for _, f := range merge.Body["referredFacilities"].([]any) {
		names = append(names, shared.Str(f, "name"))
	}
	if !slices.Equal(names, []string{"Shared", "ProdOnlyFac"}) {
		t.Errorf("merged facilities = %v", names)
	}
	if len(skipped) != 1 || skipped[0].TypeName != "Lab" || !strings.Contains(skipped[0].Reason, "missing in pre-prod") {
		t.Errorf("skipped = %+v", skipped)
	}
}

func TestPlanSpecialitySyncSkipsAMergeWhenTheCounterpartHasNoSpecialityUID(t *testing.T) {
	prod := tree(t, `[{"typeUid":"p","name":"Imaging","specialities":[{"name":"CT","referredFacilities":[{"name":"New"}]}]}]`)
	pre := tree(t, `[{"typeUid":"q","name":"Imaging","specialities":[{"name":"CT","referredFacilities":[]}]}]`)
	actions, skipped := planSpecialitySync(prod, pre, nil)
	if len(actions) != 0 || len(skipped) != 1 || skipped[0].SpecialityName != "CT" || !strings.Contains(skipped[0].Reason, "no specialityUid") {
		t.Errorf("actions = %d, skipped = %+v", len(actions), skipped)
	}
}

// Regression (issue #3): exact-string name matching alone missed a pre-prod duplicate that only
// differs by casing, so the merge body appended the SAME NPI twice under two name-casings — the BE
// 400s on the duplicate NPI within the speciality.
func TestPlanSpecialitySyncDropsASameNPIFacilityThatOnlyDiffersByNameCasingWithAWarning(t *testing.T) {
	prod := tree(t, `[{"typeUid":"prod-t1","name":"Medical Supplies","specialities":[{"name":"DME","specialityUid":"prod-sp-dme",
		"referredFacilities":[{"name":"Verio Healthcare Inc - DME","NPI":"1821467804"}]}]}]`)
	pre := tree(t, `[{"typeUid":"pre-t1","name":"Medical Supplies","specialities":[{"name":"DME","specialityUid":"pre-sp-dme",
		"referredFacilities":[{"referredFacilityUid":"pre-f-verio","name":"VERIO HEALTHCARE INC - DME","NPI":"1821467804"}]}]}]`)

	actions, skipped := planSpecialitySync(prod, pre, specPayerMap)

	// nothing genuinely new to merge -> no write action, just a reported skip
	if len(actions) != 0 || len(skipped) != 1 || skipped[0].SpecialityName != "DME" ||
		!strings.Contains(skipped[0].Reason, "1821467804") || !strings.Contains(skipped[0].Reason, "VERIO HEALTHCARE INC - DME") {
		t.Errorf("actions = %d, skipped = %+v", len(actions), skipped)
	}
}

// Regression (issue #3): two "new" providers collide by NPI with pre-prod records that already
// exist under a DIFFERENT speciality / order type entirely; a third is genuinely new. The
// collisions must be dropped (with a warning) without blocking the genuinely-new one.
func TestPlanSpecialitySyncDropsCrossSpecialityNPICollisionsButKeepsTheGenuinelyNewProvider(t *testing.T) {
	prod := tree(t, `[{"typeUid":"prod-t1","name":"Follow-up Visit","specialities":[{"name":"Orthopedics","specialityUid":"prod-sp-ortho","referredProviders":[
		{"name":"Loren Tholcke","NPI":"1588191944"},{"name":"Reza Roghani","NPI":"1386941219"},{"name":"Omar Kadri","NPI":"1999999999"}]}]}]`)
	pre := tree(t, `[
		{"typeUid":"pre-t1","name":"Follow-up Visit","specialities":[
			{"name":"Orthopedics","specialityUid":"pre-sp-ortho","referredProviders":[]},
			{"name":"Podiatry","specialityUid":"pre-sp-podiatry","referredProviders":[{"referredProviderUid":"pre-p-loren","name":"LOREN THOLCKE DO","NPI":"1588191944"}]}]},
		{"typeUid":"pre-t2","name":"Consultation","specialities":[
			{"name":"General","specialityUid":"pre-sp-general","referredProviders":[{"referredProviderUid":"pre-p-reza","name":"REZA ROGHANI MD","NPI":"1386941219"}]}]}]`)

	actions, _ := planSpecialitySync(prod, pre, specPayerMap)

	if len(actions) != 1 || actions[0].ItemName != "Orthopedics" {
		t.Fatalf("actions = %+v", actions)
	}
	var names []string
	for _, p := range actions[0].Body["referredProviders"].([]any) {
		names = append(names, shared.Str(p, "name"))
	}
	if !slices.Equal(names, []string{"Omar Kadri"}) {
		t.Errorf("providers = %v, want only the genuinely-new one (no collateral damage)", names)
	}
	w := actions[0].Warnings
	if !slices.ContainsFunc(w, func(s string) bool { return strings.Contains(s, "1588191944") && strings.Contains(s, "Podiatry") }) ||
		!slices.ContainsFunc(w, func(s string) bool { return strings.Contains(s, "1386941219") && strings.Contains(s, "Consultation") }) {
		t.Errorf("warnings = %v", w)
	}
}

func TestPlanSpecialitySyncAlsoMatchesTheLowercaseNPIField(t *testing.T) {
	prod := tree(t, `[{"typeUid":"p","name":"T","specialities":[{"name":"S","referredFacilities":[{"name":"New Name","npi":"555"}]}]}]`)
	pre := tree(t, `[{"typeUid":"q","name":"T","specialities":[{"name":"S","specialityUid":"u","referredFacilities":[{"name":"OLD NAME","NPI":"555"}]}]}]`)
	if actions, _ := planSpecialitySync(prod, pre, nil); len(actions) != 0 {
		t.Errorf("actions = %+v, want the npi/NPI casings to collide", actions)
	}
}

func finding(t *testing.T, prod, pre string, prodNames, preNames map[string]string) []PayerLinkFinding {
	t.Helper()
	return auditPayerLinks(tree(t, prod), tree(t, pre), prodNames, preNames)
}

// Regression (issue #4): a facility that exists in both envs (matched by name) is never touched by
// planSpecialitySync's additive create/merge logic, so payer-link drift on it was invisible.
// Matching must be by payer NAME (uids are per-env and never expected to match), never by payerUid.
func TestAuditPayerLinksFlagsAPayerLinkInPreProdButNotProdOnAFacilityMatchedByName(t *testing.T) {
	got := finding(t,
		`[{"typeUid":"prod-t1","name":"Medical Supplies","specialities":[{"name":"DME","referredFacilities":[{"name":"Body Basics Physical Therapy","payersProviderId":[]}]}]}]`,
		`[{"typeUid":"pre-t1","name":"Medical Supplies","specialities":[{"name":"DME","referredFacilities":[{"name":"Body Basics Physical Therapy","payersProviderId":[{"payerUid":"pre-payer-optum"}]}]}]}]`,
		map[string]string{}, map[string]string{"pre-payer-optum": "OPTUM CARE NETWORK"})
	want := `[{"typeName":"Medical Supplies","specialityName":"DME","itemKind":"facility","itemName":"Body Basics Physical Therapy",` +
		`"extraInPreProd":["OPTUM CARE NETWORK"],"missingInPreProd":[],"orphanedProdPayerUids":[],"orphanedPreProdPayerUids":[]}]`
	if g := asJSON(t, got); g != want {
		t.Errorf("findings = %s\nwant      %s", g, want)
	}
}

// Regression (issue #4): a payerUid on a shared facility/provider that resolves to no payer in
// that env's own clinic-payers list is a dangling reference — invisible to diff_settings (which
// strips uids) and to sync (additive only). Must be flagged even with no name-level drift.
func TestAuditPayerLinksFlagsAPayerUIDThatResolvesToNoPayerName(t *testing.T) {
	got := finding(t,
		`[{"typeUid":"p","name":"Follow-up Visit","specialities":[{"name":"Orthopedics","referredProviders":[{"name":"Steven Kelley","payersProviderId":[]}]}]}]`,
		`[{"typeUid":"q","name":"Follow-up Visit","specialities":[{"name":"Orthopedics","referredProviders":[{"name":"Steven Kelley","payersProviderId":[{"payerUid":"pre-payer-dangling"}]}]}]}]`,
		nil, nil)
	if len(got) != 1 || got[0].ItemKind != "provider" || got[0].ItemName != "Steven Kelley" ||
		len(got[0].ExtraInPreProd) != 0 || len(got[0].MissingInPreProd) != 0 ||
		!slices.Equal(got[0].OrphanedPreProdPayerUIDs, []string{"pre-payer-dangling"}) || len(got[0].OrphanedProdPayerUIDs) != 0 {
		t.Errorf("findings = %+v", got)
	}
}

func TestAuditPayerLinksFlagsALinkInProdButNotPreProd(t *testing.T) {
	got := finding(t,
		`[{"typeUid":"p","name":"T","specialities":[{"name":"S","referredFacilities":[{"name":"F","payersProviderId":[{"payerUid":"prod-payer-A"}]}]}]}]`,
		`[{"typeUid":"q","name":"T","specialities":[{"name":"S","referredFacilities":[{"name":"F","payersProviderId":[]}]}]}]`,
		map[string]string{"prod-payer-A": "Anthem"}, nil)
	if len(got) != 1 || !slices.Equal(got[0].MissingInPreProd, []string{"Anthem"}) || len(got[0].ExtraInPreProd) != 0 {
		t.Errorf("findings = %+v", got)
	}
}

func TestAuditPayerLinksReportsNothingWhenLinksMatchByNameOnBothSides(t *testing.T) {
	got := finding(t,
		`[{"typeUid":"p","name":"Imaging","specialities":[{"name":"MRI","referredFacilities":[{"name":"RadNet","payersProviderId":[{"payerUid":"prod-payer-A"}]}]}]}]`,
		`[{"typeUid":"q","name":"Imaging","specialities":[{"name":"MRI","referredFacilities":[{"name":"RadNet","payersProviderId":[{"payerUid":"pre-payer-A"}]}]}]}]`,
		map[string]string{"prod-payer-A": "Anthem"}, map[string]string{"pre-payer-A": "Anthem"})
	if got == nil || len(got) != 0 {
		t.Errorf("findings = %#v, want an empty non-nil slice", got)
	}
}

func TestAuditPayerLinksSkipsTypesAndSpecialitiesNotPresentInBothEnvs(t *testing.T) {
	got := finding(t, `[{"typeUid":"p","name":"Lab","specialities":[{"name":"Blood"}]}]`, `[]`, nil, nil)
	if len(got) != 0 {
		t.Errorf("findings = %+v (planSpecialitySync already reports those via skipped)", got)
	}
}
