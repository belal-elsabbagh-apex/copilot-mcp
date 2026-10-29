package settings

import (
	"slices"
	"strings"
	"testing"
)

var orderPayerMap = map[string]string{"prod-payer-A": "pre-payer-A"}

func TestRemapPayersMapRemapsKeyAndInnerPayerUIDAndDropsUnmatchedByName(t *testing.T) {
	payers, dropped := remapPayersMap(j(t, `{
		"prod-payer-A": {"payerUid":"prod-payer-A","Name":"Anthem","submissionMode":"portal"},
		"prod-payer-X": {"payerUid":"prod-payer-X","Name":"Humana"}}`), orderPayerMap)
	if got, want := asJSON(t, payers), `{"pre-payer-A":{"Name":"Anthem","payerUid":"pre-payer-A","submissionMode":"portal"}}`; got != want {
		t.Errorf("payers = %s, want %s", got, want)
	}
	if !slices.Equal(dropped, []string{"Humana"}) {
		t.Errorf("dropped = %v, want the payer NAME, not the uid", dropped)
	}
}

func TestRemapPayersMapReportsTheUIDWhenTheDroppedPayerHasNoName(t *testing.T) {
	_, dropped := remapPayersMap(j(t, `{"prod-payer-X":{"payerUid":"prod-payer-X"}}`), orderPayerMap)
	if !slices.Equal(dropped, []string{"prod-payer-X"}) {
		t.Errorf("dropped = %v", dropped)
	}
}

func TestRemapNamedRefsResolvesNamesToPreProdUIDsAndReportsMisses(t *testing.T) {
	uids, dropped := remapNamedRefs(j(t, `[{"name":"RadNet"},{"name":"Nowhere Imaging"},{"other":1}]`), "name", map[string]string{"RadNet": "pre-fac-1"})
	if !slices.Equal(uids, []string{"pre-fac-1"}) || !slices.Equal(dropped, []string{"Nowhere Imaging"}) {
		t.Errorf("uids = %v, dropped = %v (a nameless entry is neither)", uids, dropped)
	}
}

var testRefs = preTypeRefs{
	facilityUIDByName:       map[string]string{"RadNet": "pre-fac-1"},
	authSubCatUIDByName:     map[string]string{"Prior Auth": "pre-auth-1"},
	referralSubCatUIDByName: map[string]string{"Consultation Report": "pre-ref-1"},
}

const prodOrderRow = `{
	"nameUid":"prod-name-1","name":"MRI Brain",
	"referredFacilities":[{"referredFacilityUid":"prod-fac-1","name":"RadNet"},
		{"referredFacilityUid":"prod-fac-2","name":"Nowhere Imaging"}],
	"outboundReferralOrderTypeNameCPTCodes":[{"codeUid":"prod-code-1","code":"70551","units":null,"treatments":null,"description":"",
		"payers":{"prod-payer-A":{"payerUid":"prod-payer-A","Name":"Anthem"}}}],
	"authSubCategories":[{"subCategoryName":"Prior Auth","subCategoryUid":"prod-auth-1"}],
	"referralSubCategories":[{"subCategoryName":"Consultation Report","subCategoryUid":"prod-ref-1"}],
	"mandatoryICDCodes":[{"code":"G43.9"}]}`

func TestBuildOrderNameCreateBodyRemapsEveryReferenceByNameAndReportsDrops(t *testing.T) {
	body, droppedPayers, droppedRefs := buildOrderNameCreateBody(j(t, prodOrderRow), testRefs, orderPayerMap)
	want := `{"CPTCodes":[{"code":"70551","description":"","payers":{"pre-payer-A":{"Name":"Anthem","payerUid":"pre-payer-A"}},"treatments":null,"units":null}],` +
		`"authSubCategoryUids":["pre-auth-1"],"facilitiesUids":["pre-fac-1"],"mandatoryICDCodes":[{"code":"G43.9"}],"name":"MRI Brain","referralSubCategoryUids":["pre-ref-1"]}`
	if got := asJSON(t, body); got != want {
		t.Errorf("body = %s\nwant   %s (own codeUid stripped, every uid reference remapped)", got, want)
	}
	if len(droppedPayers) != 0 || !slices.Equal(droppedRefs, []string{"facility 'Nowhere Imaging'"}) {
		t.Errorf("droppedPayers = %v, droppedRefs = %v", droppedPayers, droppedRefs)
	}
}

func TestBuildOrderNameCreateBodyAlwaysSendsEmptyArraysNotNull(t *testing.T) {
	body, _, _ := buildOrderNameCreateBody(j(t, `{"name":"Bare"}`), preTypeRefs{}, nil)
	want := `{"CPTCodes":[],"authSubCategoryUids":[],"facilitiesUids":[],"mandatoryICDCodes":[],"name":"Bare","referralSubCategoryUids":[]}`
	if got := asJSON(t, body); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestPlanOrderNameSyncCreatesProdOnlyNamesAndSkipsExistingAndMissingTypes(t *testing.T) {
	prod := orderNameTree{types: []orderNameType{
		{"prod-t1", "Imaging", []map[string]any{j(t, prodOrderRow).(map[string]any), {"name": "Existing"}}},
		{"prod-t2", "Lab", []map[string]any{{"name": "CBC"}}},
	}}
	pre := orderNameTree{types: []orderNameType{{"pre-t1", "Imaging", []map[string]any{{"name": "Existing"}}}}}
	actions, skipped := planOrderNameSync(prod, pre, map[string]preTypeRefs{"Imaging": testRefs}, orderPayerMap)

	if len(actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(actions))
	}
	a := actions[0]
	if a.Op != "create" || a.ItemKind != "order" || a.ItemName != "MRI Brain" || a.Method != "POST" ||
		a.Path != "/api/v1/settings/orders/outbound/types/pre-t1/names" || a.Section != "orders" {
		t.Errorf("action = %+v", a)
	}
	if len(a.Warnings) != 1 || !strings.Contains(a.Warnings[0], "Nowhere Imaging") {
		t.Errorf("warnings = %v", a.Warnings)
	}
	// "Existing" is in both -> no action; "Lab" type missing in pre-prod -> skipped.
	if len(skipped) != 1 || skipped[0].TypeName != "Lab" || !strings.Contains(skipped[0].Reason, "missing in pre-prod") {
		t.Errorf("skipped = %+v", skipped)
	}
}

func TestPlanOrderNameSyncDropsEveryReferenceWhenThePreTypeHasNoRefMaps(t *testing.T) {
	prod := orderNameTree{types: []orderNameType{{"prod-t1", "Imaging", []map[string]any{j(t, prodOrderRow).(map[string]any)}}}}
	pre := orderNameTree{types: []orderNameType{{"pre-t1", "Imaging", nil}}}
	actions, _ := planOrderNameSync(prod, pre, nil, nil)
	if len(actions) != 1 || asJSON(t, actions[0].Body["facilitiesUids"]) != "[]" {
		t.Fatalf("actions = %+v", actions)
	}
	w := actions[0].Warnings
	if len(w) != 2 || !strings.Contains(w[0], "payer link(s) dropped") || !strings.Contains(w[1], "4 reference(s) dropped") {
		t.Errorf("warnings = %v, want a payer drop and the 4 unresolved references (never a prod uid copied through)", w)
	}
}
