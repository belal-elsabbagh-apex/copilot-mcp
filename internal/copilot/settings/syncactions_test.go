package settings

import (
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// mkAction is a minimal SyncAction fixture; the body is only inspected by summarizeActionBody.
func mkAction(mut func(*SyncAction)) SyncAction {
	a := SyncAction{
		Section: "specialties", Op: "create", ItemKind: "speciality", TypeName: "Imaging", ItemName: "MRI",
		Method: "POST", Path: "/api/v1/settings/orders/outbound/types/pre-t1/specialities",
		Body: map[string]any{"name": "MRI", "referredFacilities": []any{map[string]any{"name": "Fac1"}, map[string]any{"name": "Fac2"}}},
	}
	if mut != nil {
		mut(&a)
	}
	return a
}

func TestActionIDIsStableAndDistinctPerSemanticField(t *testing.T) {
	a := mkAction(nil)
	if actionID(a) != "specialties:create:Imaging:MRI" || actionID(a) != actionID(mkAction(nil)) {
		t.Errorf("actionID = %q", actionID(a))
	}
	for name, mut := range map[string]func(*SyncAction){
		"op":      func(a *SyncAction) { a.Op = "merge" },
		"item":    func(a *SyncAction) { a.ItemName = "CT" },
		"type":    func(a *SyncAction) { a.TypeName = "Lab" },
		"section": func(a *SyncAction) { a.Section = "orders" },
	} {
		if actionID(mkAction(mut)) == actionID(a) {
			t.Errorf("changing the %s must change the id", name)
		}
	}
}

func TestAssignActionIDsKeepsPlanOrderAndDisambiguatesDuplicates(t *testing.T) {
	planned := assignActionIDs([]SyncAction{mkAction(nil), mkAction(func(a *SyncAction) { a.ItemName = "CT" }), mkAction(nil)})
	ids := []string{planned[0].ID, planned[1].ID, planned[2].ID}
	want := []string{"specialties:create:Imaging:MRI", "specialties:create:Imaging:CT", "specialties:create:Imaging:MRI#2"}
	if !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func twoPlanned() []PlannedSyncAction {
	return assignActionIDs([]SyncAction{mkAction(nil), mkAction(func(a *SyncAction) { a.ItemName = "CT" })})
}

func TestSelectPlannedActionsAllSelectsEverything(t *testing.T) {
	sel, not, unmatched, err := selectPlannedActions(twoPlanned(), ApplyFilter{All: true})
	if err != nil || len(sel) != 2 || len(not) != 0 || unmatched == nil || len(unmatched) != 0 {
		t.Errorf("got %d/%d/%v, %v", len(sel), len(not), unmatched, err)
	}
}

func TestSelectPlannedActionsActionIDsSelectTheSubsetAndReportUnknownIDs(t *testing.T) {
	sel, not, unmatched, err := selectPlannedActions(twoPlanned(), ApplyFilter{ActionIDs: []string{
		"specialties:create:Imaging:CT", "specialties:create:Imaging:GONE", "specialties:create:Imaging:GONE"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel) != 1 || sel[0].ItemName != "CT" || len(not) != 1 || not[0].ItemName != "MRI" {
		t.Errorf("selected %v, notSelected %v", sel, not)
	}
	if !slices.Equal(unmatched, []string{"specialties:create:Imaging:GONE"}) {
		t.Errorf("unmatched = %v, want the stale id once (state drifted since planning)", unmatched)
	}
}

func TestSelectPlannedActionsRejectsNeitherAndBothFiltersAsExpectedErrors(t *testing.T) {
	for name, f := range map[string]ApplyFilter{
		"neither":   {},
		"empty ids": {ActionIDs: []string{}},
		"both":      {ActionIDs: []string{"x"}, All: true},
	} {
		_, _, _, err := selectPlannedActions(twoPlanned(), f)
		if err == nil || !shared.IsExpected(err) || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("%s: err = %v, want an Expected 'exactly one' error", name, err)
		}
	}
	for name, f := range map[string]ApplyFilter{"all": {All: true}, "ids": {ActionIDs: []string{"x"}}} {
		if err := assertApplyFilter(f); err != nil {
			t.Errorf("%s alone is valid: %v", name, err)
		}
	}
}

func TestSummarizeActionBodyCountsSpecialityAndOrderBodies(t *testing.T) {
	if got, want := summarizeActionBody(mkAction(nil)), "create speciality 'MRI' (2 facilities, 0 providers)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	merge := mkAction(func(a *SyncAction) {
		a.Op = "merge"
		a.Body = map[string]any{"name": "MRI", "referredFacilities": []any{map[string]any{}}, "referredProviders": []any{map[string]any{}, map[string]any{}}}
	})
	if got, want := summarizeActionBody(merge), "merge speciality 'MRI' -> 1 facilities, 2 providers (existing + prod-only additions)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	order := mkAction(func(a *SyncAction) {
		a.Section, a.ItemKind, a.ItemName = "orders", "order", "MRI Brain"
		a.Body = map[string]any{
			"name": "MRI Brain", "CPTCodes": []any{map[string]any{}}, "facilitiesUids": []any{"f1", "f2"},
			"authSubCategoryUids": []any{"a1"}, "referralSubCategoryUids": []any{}, "mandatoryICDCodes": []any{},
		}
	})
	if got, want := summarizeActionBody(order), "create order 'MRI Brain' (1 CPTs, 2 facilities, 1 auth + 0 referral sub-categories)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestToActionSummaryIncludesTheBodyOnlyWhenAsked(t *testing.T) {
	planned := assignActionIDs([]SyncAction{mkAction(nil)})[0]
	slim := toActionSummary(planned, false)
	if slim.ID != "specialties:create:Imaging:MRI" || !strings.Contains(slim.Summary, "create speciality 'MRI'") || slim.Body != nil {
		t.Errorf("slim = %+v", slim)
	}
	if got := asJSON(t, slim); strings.Contains(got, `"body"`) || strings.Contains(got, "warnings") {
		t.Errorf("slim JSON = %s, want no body/warnings keys", got)
	}
	if full := toActionSummary(planned, true); len(full.Body) != len(planned.Body) {
		t.Errorf("full body = %v", full.Body)
	}
}
