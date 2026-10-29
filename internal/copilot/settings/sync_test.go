package settings

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	specPath  = typesPath + "/t1/specialities"
	namesPath = typesPath + "/t1/names"
)

// syncFixture is a prod with one speciality + one order that pre-prod lacks (the order references
// a facility and a payer that DO exist in pre-prod by name, plus a payer that does not).
func syncFixture(t *testing.T) (prod, pre *testBE) {
	t.Helper()
	types := j(t, `[{"typeUid":"t1","name":"Imaging"}]`)
	prod = newBE(t, map[string]any{
		"GET " + typesPath: types,
		"GET " + specPath: j(t, `{"data":[{"name":"MRI","specialityUid":"prod-sp-mri","referredFacilities":[
			{"referredFacilityUid":"prod-f1","name":"RadNet","NPI":"111","payersProviderId":[{"payerUid":"prod-A"},{"payerUid":"prod-X"}]}]}]}`),
		"GET " + namesPath: j(t, `{"data":[{"nameUid":"n1","name":"MRI Brain","referredFacilities":[{"name":"RadNet"}],
			"outboundReferralOrderTypeNameCPTCodes":[{"codeUid":"c1","code":"70551","payers":{"prod-A":{"payerUid":"prod-A","Name":"Anthem"}}}],
			"authSubCategories":[],"referralSubCategories":[],"mandatoryICDCodes":[]}]}`),
		"GET /api/v1/settings/clinic-payers": j(t, `{"data":[{"Name":"Anthem","payerUid":"prod-A"},{"Name":"Humana","payerUid":"prod-X"}]}`),
	})
	pre = newBE(t, map[string]any{
		"GET " + typesPath: types,
		"GET " + specPath:  j(t, `{"data":[]}`),
		"GET " + namesPath: j(t, `{"data":[]}`),
		"GET /api/v1/settings/specialities/facilities": j(t, `[{"name":"RadNet","referredFacilityUid":"pre-f1"}]`),
		"GET /api/v1/settings/clinic-payers":           j(t, `{"data":[{"Name":"Anthem","payerUid":"pre-A"}]}`),
		"POST " + specPath:                             map[string]any{"ok": true},
		"POST " + namesPath:                            map[string]any{"ok": true},
	})
	useBEs(t, prod, pre)
	return prod, pre
}

func TestPlanSettingsSyncPlansCreatesWithoutWritingAnywhere(t *testing.T) {
	prod, pre := syncFixture(t)
	var progress []string
	res, err := PlanSettingsSync(t.Context(), PlanSettingsSyncOpts{Profile: "ossm", Sections: []string{"specialties", "orders"},
		OnProgress: func(done, total int, label string) {
			progress = append(progress, fmt.Sprintf("%d/%d %s", done, total, label))
		}})
	if err != nil {
		t.Fatal(err)
	}
	if len(prod.writes())+len(pre.writes()) != 0 {
		t.Fatalf("plan wrote: prod %v, pre %v", prod.writes(), pre.writes())
	}
	if res.Account != "ossm" || res.ProdBase != prod.URL || res.PreProdBase != pre.URL || res.ActionCount != 2 {
		t.Errorf("result header = %+v", res)
	}
	ids := []string{res.Actions[0].ID, res.Actions[1].ID}
	if !slices.Equal(ids, []string{"specialties:create:Imaging:MRI", "orders:create:Imaging:MRI Brain"}) {
		t.Errorf("ids = %v, want the specialities domain first (catalog order), then orders", ids)
	}
	if got := res.Actions[0].Summary; got != "create speciality 'MRI' (1 facilities, 0 providers)" {
		t.Errorf("summary = %q", got)
	}
	if w := res.Actions[0].Warnings; len(w) != 1 || !strings.Contains(w[0], "prod-X") {
		t.Errorf("warnings = %v, want the unmatched Humana payer link reported", w)
	}
	if res.Actions[0].Body != nil {
		t.Error("bodies are summarized unless includeBodies")
	}
	if res.Skipped == nil || res.SkippedSections == nil || res.PayerLinkFindings == nil {
		t.Errorf("arrays must marshal as [] not null: %+v", res)
	}
	slices.Sort(progress)
	if len(progress) != 2 || !strings.HasPrefix(progress[0], "1/2 planned") && !strings.HasPrefix(progress[0], "2/2 planned") {
		t.Errorf("progress = %v", progress)
	}
	if got := asJSON(t, res.Actions[1]); !strings.Contains(got, `"summary":"create order 'MRI Brain' (1 CPTs, 1 facilities, 0 auth + 0 referral sub-categories)"`) {
		t.Errorf("order action = %s", got)
	}
}

func TestPlanSettingsSyncRemapsOrderReferencesByName(t *testing.T) {
	syncFixture(t)
	res, err := PlanSettingsSync(t.Context(), PlanSettingsSyncOpts{Profile: "ossm", Sections: []string{"orders"}, IncludeBodies: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"CPTCodes":[{"code":"70551","payers":{"pre-A":{"Name":"Anthem","payerUid":"pre-A"}}}],"authSubCategoryUids":[],"facilitiesUids":["pre-f1"],"mandatoryICDCodes":[],"name":"MRI Brain","referralSubCategoryUids":[]}`
	if got := asJSON(t, res.Actions[0].Body); got != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
}

func TestPlanSettingsSyncReportsSectionsWithNoWriteMappingAndDoesNotLogIn(t *testing.T) {
	prod, pre := syncFixture(t)
	res, err := PlanSettingsSync(t.Context(), PlanSettingsSyncOpts{Profile: "ossm", Sections: []string{"locations", "orders-outbound"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ActionCount != 0 || len(res.Actions) != 0 || len(res.SkippedSections) != 2 || res.SkippedSections[0].Key != "orders-outbound" ||
		res.SkippedSections[0].Reason != "no write mapping yet — additive sync not implemented" {
		t.Errorf("result = %+v", res)
	}
	if len(prod.requests())+len(pre.requests()) != 0 {
		t.Error("nothing syncable was selected, so no tenant should have been touched")
	}
	if res.ProdBase != prod.URL {
		t.Errorf("prodBase = %q (comes from config, no login needed)", res.ProdBase)
	}
}

func TestPlanSettingsSyncRunsAnUnknownSelectionAndUnknownProfileIntoErrors(t *testing.T) {
	syncFixture(t)
	if _, err := PlanSettingsSync(t.Context(), PlanSettingsSyncOpts{Profile: "ossm", Sections: []string{"nope"}}); err == nil || !strings.Contains(err.Error(), "unknown section(s): nope") {
		t.Errorf("err = %v", err)
	}
	if _, err := PlanSettingsSync(t.Context(), PlanSettingsSyncOpts{Profile: "ghost", Sections: []string{"orders"}}); err == nil || !strings.Contains(err.Error(), "unknown profile 'ghost'") {
		t.Errorf("err = %v", err)
	}
}

func TestPlanSettingsSyncFailsWhenAPayerListCannotBeRead(t *testing.T) {
	prod, _ := syncFixture(t)
	prod.mu.Lock()
	base := prod.handle
	prod.handle = func(m, p string, b any) (int, any) {
		if p == "/api/v1/settings/clinic-payers" {
			return 500, nil
		}
		return base(m, p, b)
	}
	prod.mu.Unlock()
	_, err := PlanSettingsSync(t.Context(), PlanSettingsSyncOpts{Profile: "ossm", Sections: []string{"orders"}})
	if err == nil || !strings.Contains(err.Error(), "GET /settings/clinic-payers -> 500") {
		t.Errorf("err = %v", err)
	}
}

func TestApplySettingsSyncAllWritesOnlyToPreProdInPlanOrder(t *testing.T) {
	prod, pre := syncFixture(t)
	var audit []string
	var progress int
	res, err := ApplySettingsSync(t.Context(), ApplySettingsSyncOpts{
		ApplyFilter: ApplyFilter{All: true}, Profile: "ossm", Sections: []string{"specialties", "orders"},
		OnWrite: func(msg string, data map[string]any) {
			audit = append(audit, fmt.Sprintf("%s %v", msg, data["status"]))
		},
		OnProgress: func(done, total int, _ string) { progress = max(progress, done) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prod.writes()) != 0 {
		t.Fatalf("prod was written to: %v", prod.writes())
	}
	w := pre.writes()
	if len(w) != 2 || w[0].Method != "POST" || w[0].Path != specPath || w[1].Path != namesPath {
		t.Fatalf("pre-prod writes = %+v, want POST specialities then POST names (sequential, plan order)", w)
	}
	if shared.Str(w[1].Body, "name") != "MRI Brain" {
		t.Errorf("order body = %v", w[1].Body)
	}
	if res.PlannedCount != 2 || len(res.Executed) != 2 || !res.Executed[0].OK || res.Executed[0].Status != 200 || len(res.NotSelected) != 0 {
		t.Errorf("result = %+v", res)
	}
	if res.UnmatchedIDs == nil || res.Skipped == nil || res.SkippedSections == nil || res.NotSelected == nil {
		t.Errorf("arrays must marshal as [] not null: %s", asJSON(t, res))
	}
	if len(audit) != 2 || audit[0] != "apply_settings_sync create speciality 'MRI' (type 'Imaging') 200" || progress != 2 {
		t.Errorf("audit = %v, progress = %d", audit, progress)
	}
}

func TestApplySettingsSyncActionIDsExecutesOnlyTheSelectionAndReportsStaleIDs(t *testing.T) {
	_, pre := syncFixture(t)
	res, err := ApplySettingsSync(t.Context(), ApplySettingsSyncOpts{
		ApplyFilter: ApplyFilter{ActionIDs: []string{"orders:create:Imaging:MRI Brain", "orders:create:Imaging:Drifted Away"}},
		Profile:     "ossm", Sections: []string{"specialties", "orders"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := pre.writes(); len(w) != 1 || w[0].Path != namesPath {
		t.Errorf("writes = %+v, want only the selected order", w)
	}
	if len(res.Executed) != 1 || len(res.NotSelected) != 1 || res.NotSelected[0].ID != "specialties:create:Imaging:MRI" {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(res.UnmatchedIDs, []string{"orders:create:Imaging:Drifted Away"}) {
		t.Errorf("unmatched = %v, want the stale id reported instead of executed", res.UnmatchedIDs)
	}
}

func TestApplySettingsSyncRejectsAnAmbiguousSelectionBeforeAnyRequest(t *testing.T) {
	prod, pre := syncFixture(t)
	for name, f := range map[string]ApplyFilter{"neither": {}, "both": {All: true, ActionIDs: []string{"x"}}} {
		_, err := ApplySettingsSync(t.Context(), ApplySettingsSyncOpts{ApplyFilter: f, Profile: "ossm", Sections: []string{"orders"}})
		if err == nil || !shared.IsExpected(err) || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(prod.requests())+len(pre.requests()) != 0 {
		t.Error("a bad selection must be rejected for free, before any login or read")
	}
}

func TestApplySettingsSyncRePlansAgainstLiveStateSoAnAlreadyAppliedActionIsGone(t *testing.T) {
	_, pre := syncFixture(t)
	run := func() ApplySettingsSyncResult {
		res, err := ApplySettingsSync(t.Context(), ApplySettingsSyncOpts{ApplyFilter: ApplyFilter{All: true}, Profile: "ossm", Sections: []string{"orders"}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if first := run(); len(first.Executed) != 1 {
		t.Fatalf("first apply = %+v", first)
	}
	// Pre-prod now has the order: the second apply re-plans from live state and finds nothing.
	pre.mu.Lock()
	base := pre.handle
	pre.handle = func(m, p string, b any) (int, any) {
		if m == "GET" && p == namesPath {
			return 200, j(t, `{"data":[{"name":"MRI Brain"}]}`)
		}
		return base(m, p, b)
	}
	pre.mu.Unlock()
	if second := run(); len(second.Executed) != 0 || second.PlannedCount != 0 {
		t.Errorf("second apply = %+v, want a no-op (nothing is cached across plan and apply)", second)
	}
}

func TestApplySettingsSyncRecordsAFailedWriteAndKeepsGoing(t *testing.T) {
	_, pre := syncFixture(t)
	pre.mu.Lock()
	base := pre.handle
	pre.handle = func(m, p string, b any) (int, any) {
		if m == "POST" && p == specPath {
			return 400, map[string]any{"message": "referredFacilities[0].source is not allowed"}
		}
		return base(m, p, b)
	}
	pre.mu.Unlock()
	res, err := ApplySettingsSync(t.Context(), ApplySettingsSyncOpts{ApplyFilter: ApplyFilter{All: true}, Profile: "ossm", Sections: []string{"specialties", "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Executed) != 2 || res.Executed[0].OK || res.Executed[0].Status != 400 || !res.Executed[1].OK {
		t.Errorf("executed = %+v, want the 400 recorded and the next write still attempted", res.Executed)
	}
}

func TestExecuteActionsRefusesAnyClientThatIsNotPreProd(t *testing.T) {
	planned := assignActionIDs([]SyncAction{mkAction(nil)})
	for _, env := range []config.Env{config.Prod, ""} {
		be := &fakeBE{env: env, handle: func(string, string, any) copilot.Response { return respond(200, nil) }}
		_, err := executeActions(t.Context(), be, planned, ApplySettingsSyncOpts{})
		if err == nil || !strings.Contains(err.Error(), "apply_settings_sync writes are pre_prod-only") {
			t.Errorf("env %q: err = %v", env, err)
		}
		if len(be.writes()) != 0 {
			t.Errorf("env %q: wrote %v", env, be.writes())
		}
	}
}

func TestExecuteActionsWithNothingSelectedNeedsNoClient(t *testing.T) {
	got, err := executeActions(t.Context(), nil, nil, ApplySettingsSyncOpts{})
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("got %#v, %v", got, err)
	}
}

// failingSyncer is a SectionSyncer whose crawl fails.
type failingSyncer struct{ err error }

func (failingSyncer) Domain() string { return "failing" }
func (f failingSyncer) Crawl(context.Context, *Source, *Source) (Planner, error) {
	return nil, f.err
}

func TestRunSyncersSurfacesACrawlFailureAndPlansEveryDomain(t *testing.T) {
	be := func(env config.Env) *fakeBE {
		return &fakeBE{env: env, handle: func(_, path string, _ any) copilot.Response {
			if path == typesPath {
				return respond(200, j(t, `[{"typeUid":"t1","name":"Imaging"}]`))
			}
			return respond(200, j(t, `{"data":[]}`))
		}}
	}
	boom := errors.New("boom")
	if _, err := runSyncers(t.Context(), be(config.Prod), be(config.PreProd), []SectionSyncer{ordersSyncer{}, failingSyncer{boom}}, nil); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the crawl failure", err)
	}
	results, err := runSyncers(t.Context(), be(config.Prod), be(config.PreProd), []SectionSyncer{specialitiesSyncer{}, ordersSyncer{}}, nil)
	if err != nil || len(results) != 2 {
		t.Fatalf("results = %v, %v", results, err)
	}
}

// Both syncers start from the order-type list; one Source memoizes it, so a plan reads it once per
// env, not once per domain.
func TestRunSyncersReadsTheOrderTypeListOncePerEnv(t *testing.T) {
	var mu sync.Mutex
	var typeReads int
	be := func(env config.Env) *fakeBE {
		return &fakeBE{env: env, handle: func(_, path string, _ any) copilot.Response {
			if path == typesPath {
				mu.Lock()
				typeReads++
				mu.Unlock()
				return respond(200, j(t, `[{"typeUid":"t1","name":"Imaging"}]`))
			}
			return respond(200, j(t, `{"data":[]}`))
		}}
	}
	if _, err := runSyncers(t.Context(), be(config.Prod), be(config.PreProd), []SectionSyncer{specialitiesSyncer{}, ordersSyncer{}}, nil); err != nil {
		t.Fatal(err)
	}
	if typeReads != 2 {
		t.Errorf("order-type list fetched %d times, want 2 (once per env)", typeReads)
	}
}

func TestExecuteActionsReturnsTheRequestErrorUnchanged(t *testing.T) {
	boom := errors.New("dial tcp: connection refused")
	be := &fakeBE{env: config.PreProd, reqErr: boom}
	_, err := executeActions(t.Context(), be, assignActionIDs([]SyncAction{mkAction(nil)}), ApplySettingsSyncOpts{})
	if err != boom {
		t.Errorf("err = %v, want the request error itself, no method/path prefix", err)
	}
}
