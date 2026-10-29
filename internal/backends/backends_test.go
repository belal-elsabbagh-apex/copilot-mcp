package backends

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// fakeBackend answers FindOrderQueueItems only; every other method would panic on the nil
// embedded interface, which these tests never reach.
type fakeBackend struct {
	orchestrator.Backend
	name orchestrator.Orchestrator
	res  orchestrator.FindOrderResult
	err  error
}

func (f fakeBackend) Name() orchestrator.Orchestrator { return f.name }
func (f fakeBackend) FindOrderQueueItems(context.Context, orchestrator.FindOrderArgs) (orchestrator.FindOrderResult, error) {
	return f.res, f.err
}

func item(o orchestrator.Orchestrator, id, created string, jobs ...string) orchestrator.QueueItemCore {
	attempts := []orchestrator.JobAttempt{}
	for _, j := range jobs {
		attempts = append(attempts, orchestrator.JobAttempt{Orchestrator: o, ID: j})
	}
	return orchestrator.QueueItemCore{Orchestrator: o, ItemID: id, CreatedAt: created, Jobs: attempts}
}

func ids(items []orchestrator.QueueItemCore) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ItemID)
	}
	return out
}

func TestSelectBackendsRejectsFolderWithoutOrchestrator(t *testing.T) {
	_, err := SelectBackends("", "Some Folder")
	if err == nil || !shared.IsExpected(err) {
		t.Fatalf("SelectBackends(\"\", folder) error = %v, want an Expected error", err)
	}
}

func TestSelectBackendsOneOrchestratorReturnsOnlyItsBackend(t *testing.T) {
	bs, err := SelectBackends(orchestrator.UiPath, "F")
	if err != nil || len(bs) != 1 || bs[0].Name() != orchestrator.UiPath {
		t.Fatalf("SelectBackends(uipath) = %v, %v; want just the uipath backend", bs, err)
	}
}

func TestFindOrderMergesNewestFirstAndDedupesJobIDsPerBackend(t *testing.T) {
	u := fakeBackend{name: orchestrator.UiPath, res: orchestrator.FindOrderResult{
		Folder: "uf", SearchMode: "m", Scanned: 3, Notes: []string{"n1"},
		Items: []orchestrator.QueueItemCore{
			item(orchestrator.UiPath, "u-new", "2026-05-03T00:00:00Z", "j1"),
			item(orchestrator.UiPath, "u-old", "2026-05-01T00:00:00Z", "j1", "j0"),
		},
	}}
	a := fakeBackend{name: orchestrator.Apex, res: orchestrator.FindOrderResult{
		Folder: "af", SearchMode: "k", Scanned: 1,
		Items: []orchestrator.QueueItemCore{item(orchestrator.Apex, "a-mid", "2026-05-02T00:00:00Z", "ja")},
	}}
	got := FindOrderAcross(t.Context(), orchestrator.FindOrderArgs{}, []orchestrator.Backend{u, a})

	if want := []string{"u-new", "a-mid", "u-old"}; !slices.Equal(ids(got.Items), want) {
		t.Errorf("items = %v, want %v (newest first across backends)", ids(got.Items), want)
	}
	if want := []string{"j1", "j0"}; !slices.Equal(got.JobIDs[orchestrator.UiPath], want) {
		t.Errorf("uipath jobIds = %v, want %v (retried transaction repeats j1)", got.JobIDs[orchestrator.UiPath], want)
	}
	if got.Count != 3 || got.Notes[0] != "uipath: n1" || got.Backends[orchestrator.Apex].Folder != "af" {
		t.Errorf("count/notes/backends = %d %v %v", got.Count, got.Notes, got.Backends)
	}
	if got.SearchErrors != nil {
		t.Errorf("searchErrors = %v, want none", got.SearchErrors)
	}
}

func TestFindOrderIsolatesOneBackendFailure(t *testing.T) {
	u := fakeBackend{name: orchestrator.UiPath, err: errors.New("boom")}
	a := fakeBackend{name: orchestrator.Apex, res: orchestrator.FindOrderResult{
		Items: []orchestrator.QueueItemCore{item(orchestrator.Apex, "a1", "2026-05-02T00:00:00Z")},
	}}
	got := FindOrderAcross(t.Context(), orchestrator.FindOrderArgs{}, []orchestrator.Backend{u, a})

	if got.SearchErrors[orchestrator.UiPath] != "boom" || got.Count != 1 {
		t.Errorf("searchErrors/count = %v/%d, want uipath boom and apex's item kept", got.SearchErrors, got.Count)
	}
	if _, ok := got.Backends[orchestrator.UiPath]; ok {
		t.Error("failed backend listed under backends, want only answering backends")
	}
	if len(got.Searched) != 2 {
		t.Errorf("searched = %v, want both backends", got.Searched)
	}
}

func TestFindOrderKeepsABackendsOwnSearchErrorAndItsItems(t *testing.T) {
	u := fakeBackend{name: orchestrator.UiPath, res: orchestrator.FindOrderResult{
		SearchError: "odata 500", Items: []orchestrator.QueueItemCore{item(orchestrator.UiPath, "u1", "2026-05-02T00:00:00Z")},
	}}
	got := FindOrderAcross(t.Context(), orchestrator.FindOrderArgs{}, []orchestrator.Backend{u})
	if got.SearchErrors[orchestrator.UiPath] != "odata 500" || got.Count != 1 {
		t.Errorf("got %v/%d, want the search error reported next to the surviving item", got.SearchErrors, got.Count)
	}
}

func TestFindOrderSortsUnparseableTimestampsOldest(t *testing.T) {
	u := fakeBackend{name: orchestrator.UiPath, res: orchestrator.FindOrderResult{Items: []orchestrator.QueueItemCore{
		item(orchestrator.UiPath, "bad", "not a date"), item(orchestrator.UiPath, "ok", "2026-05-02T00:00:00Z"),
	}}}
	got := FindOrderAcross(t.Context(), orchestrator.FindOrderArgs{}, []orchestrator.Backend{u})
	if want := []string{"ok", "bad"}; !slices.Equal(ids(got.Items), want) {
		t.Errorf("items = %v, want %v", ids(got.Items), want)
	}
}

func TestAcrossResultOmitsEmptyNotesAndSearchErrors(t *testing.T) {
	got := FindOrderAcross(t.Context(), orchestrator.FindOrderArgs{}, nil)
	b, _ := json.Marshal(got)
	if want := `{"count":0,"jobIds":{},"items":[],"searched":[],"backends":{}}`; string(b) != want {
		t.Errorf("json = %s, want %s", b, want)
	}
}

func useConfig(t *testing.T, apex map[string]any) {
	t.Helper()
	creds := map[string]any{"be": "https://be.example.com", "email": "e@example.com", "password": "pw"}
	cfg := map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": creds, "pre_prod": creds},
		"uipath":  map[string]any{"orchestratorUrl": "https://cloud.uipath.com/myorg/mytenant/orchestrator_", "bearer": "b"},
	}
	if apex != nil {
		cfg["apex"] = apex
	}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", path)
	for _, k := range []string{"APEX_API_URL", "APEX_USER_NAME", "APEX_PASSWORD"} {
		t.Setenv(k, "")
	}
	config.ResetForTest()
	t.Cleanup(config.ResetForTest)
}

func names(bs []orchestrator.Backend) []orchestrator.Orchestrator {
	out := []orchestrator.Orchestrator{}
	for _, b := range bs {
		out = append(out, b.Name())
	}
	return out
}

func TestSearchableBackendsSkipsWhollyUnconfiguredApex(t *testing.T) {
	useConfig(t, nil)
	if got := names(SearchableBackends()); !slices.Equal(got, []orchestrator.Orchestrator{orchestrator.UiPath}) {
		t.Errorf("SearchableBackends() = %v, want uipath only", got)
	}
}

func TestSearchableBackendsKeepsHalfConfiguredApexSoItsErrorSurfaces(t *testing.T) {
	useConfig(t, map[string]any{"apiUrl": "https://apex.example.com"})
	if got := names(SearchableBackends()); len(got) != 2 {
		t.Errorf("SearchableBackends() = %v, want both backends", got)
	}
}

func TestForReturnsBackendPerOrchestrator(t *testing.T) {
	for _, o := range orchestrator.Orchestrators {
		if b := BackendFor(o); b == nil || b.Name() != o {
			t.Errorf("BackendFor(%s) = %v", o, b)
		}
	}
	if BackendFor("nope") != nil {
		t.Error("BackendFor(unknown) != nil")
	}
}
