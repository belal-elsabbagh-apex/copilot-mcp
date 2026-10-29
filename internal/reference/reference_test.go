package reference

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

func TestResolvePortalMatchesKeyAliasAndQueueNameSuffixCaseInsensitively(t *testing.T) {
	for _, name := range []string{"PMG", "pmg", "PMG auth submit queue", "PMG Auth Sync Queue  "} {
		if got := ResolvePortal(name); !got.Matched || got.Key != "PMG" {
			t.Errorf("ResolvePortal(%q) = %+v", name, got)
		}
	}
	if got := ResolvePortal("san diego"); !got.Matched || got.Key != "SDP" {
		t.Errorf("alias lookup = %+v", got)
	}
}

func TestResolvePortalMissIsTotalWithEmptySentinels(t *testing.T) {
	for _, name := range []string{"NO SUCH PORTAL", "", "  auth submit queue"} {
		got := ResolvePortal(name)
		if got.Matched || got.Key != "" || got.Family != FamilyStandalone || got.Aliases == nil {
			t.Errorf("ResolvePortal(%q) = %+v", name, got)
		}
	}
}

func TestResolvePortalByDefIDMatchesSubmitAndSyncIDsButNotZero(t *testing.T) {
	if got := ResolvePortalByDefID(38592); got.Key != "PMG" {
		t.Errorf("submit id = %+v", got)
	}
	if got := ResolvePortalByDefID(39491); got.Key != "PMG" {
		t.Errorf("sync id = %+v", got)
	}
	// 0 means "unknown" on many entries — it must never match one of them.
	if got := ResolvePortalByDefID(0); got.Matched {
		t.Errorf("zero id matched %+v", got)
	}
}

func TestResolvedPortalMarshalsEntryFieldsThenMatched(t *testing.T) {
	b, err := json.Marshal(ResolvePortal("IHP"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"key":"IHP","aliases":["CCI"],"portalDir":"portals/medpoint",` +
		`"buildArtifact":"medpoint-auth.cjs","family":"Medpoint","account":"KAFRI","submitQueueDefId":43449,` +
		`"syncQueueDefId":95893,"volume":"139","matched":true}`
	if string(b) != want {
		t.Errorf("got %s", b)
	}
}

func TestFolderLookups(t *testing.T) {
	if FolderIDFor(config.Prod) != "231517" || FolderNameFor(config.PreProd) != "Authorization Dev Clone" {
		t.Error("folder id/name by env")
	}
	if EnvForFolderID("231517") != config.Prod || EnvForFolderID("434039") != config.PreProd {
		t.Error("known folder ids")
	}
	// unknown ids default to the writable pre-prod folder
	if EnvForFolderID("999") != config.PreProd || FolderNameForID("999") != "" {
		t.Error("unknown folder id")
	}
	if FolderNameForID("231517") != "Authorization" {
		t.Error("folder name for id")
	}
}

func TestUiPathFoldersMarshalProdFirstLikeTheTSRecord(t *testing.T) {
	b, err := json.Marshal(UiPathFolders)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"prod":{"name":"Authorization","organizationUnitId":"231517","folderKey":"9ecc9792-b3f1-4c3b-bae9-51b6f31384a5","realBeCalls":true},` +
		`"pre_prod":{"name":"Authorization Dev Clone","organizationUnitId":"434039","folderKey":"db6a5e70-7d8b-4113-afbc-a425eb81f015","realBeCalls":true}}`
	if string(b) != want {
		t.Errorf("got %s", b)
	}
}

func TestAutomationIDKnownAndUnknownAccounts(t *testing.T) {
	if id, ok := AutomationID("KAFRI"); !ok || id != "c741d791-3e3b-4102-b95c-f94549949c9a" {
		t.Errorf("KAFRI = %q, %v", id, ok)
	}
	if _, ok := AutomationID("NOPE"); ok {
		t.Error("unknown account resolved")
	}
}

func TestEmbeddedDocBlobsAreValidJSON(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"QueueItemSchema": QueueItemSchema, "ResultContract": ResultContract, "OrderLifecycle": OrderLifecycle,
		"SafetyRules": SafetyRules, "ConfigGuide": ConfigGuide,
	} {
		if !json.Valid(raw) {
			t.Errorf("%s is not valid JSON", name)
		}
	}
}

// Queue definition ids were re-checked against the live UiPath prod folder; a duplicated id would
// make ResolvePortalByDefID ambiguous.
func TestPortalQueueDefIDsAreUniqueAcrossSubmitAndSync(t *testing.T) {
	seen := map[int]string{}
	for _, p := range Portals {
		for _, id := range []int{p.SubmitQueueDefID, p.SyncQueueDefID} {
			if id == 0 {
				continue
			}
			if prev, dup := seen[id]; dup {
				t.Errorf("queue def id %d is on both %s and %s", id, prev, p.Key)
			}
			seen[id] = p.Key
		}
	}
}

func TestResolvePortalByDefIDFollowsLiveQueueLayout(t *testing.T) {
	for id, want := range map[int]string{
		88575: "CCIPA", 152540: "CCIPA", // CCIPA has its own queues, distinct from IHP's
		151961: "HEALTHNET", 151971: "CHMP", 155637: "TRICARE", 168205: "CENCAL", 162543: "ALTURA",
	} {
		if got := ResolvePortalByDefID(id); got.Key != want {
			t.Errorf("ResolvePortalByDefID(%d) = %q, want %q", id, got.Key, want)
		}
	}
	if got := ResolvePortal("CCIPA"); got.Key != "CCIPA" {
		t.Errorf("ResolvePortal(CCIPA) = %q", got.Key)
	}
}

func TestPortalDirsAreRepoRelativeAndBuildArtifactsEndInCjs(t *testing.T) {
	for _, p := range Portals {
		if p.BuildArtifact != "" && !strings.HasSuffix(p.BuildArtifact, "-auth.cjs") {
			t.Errorf("%s buildArtifact %q", p.Key, p.BuildArtifact)
		}
		if p.PortalDir == "" && p.BuildArtifact != "" {
			t.Errorf("%s has a build artifact but no directory", p.Key)
		}
	}
}

// The TS-era resources claimed the dev clone skips BE notifications and that caches are
// memory-only / sessions live at a fixed path; none of that is true of the Go server.
func TestEmbeddedDocsCarryNoStaleRuntimeOrSandboxClaims(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"SafetyRules": SafetyRules, "ConfigGuide": ConfigGuide, "ResultContract": ResultContract, "QueueItemSchema": QueueItemSchema,
	} {
		text := string(raw)
		for _, stale := range []string{"skips all BE notifications", "bunx", "dist/server.js", "cached in memory only", "memory only, never on disk"} {
			if strings.Contains(text, stale) {
				t.Errorf("%s still says %q", name, stale)
			}
		}
	}
	if !strings.Contains(string(ConfigGuide), "@belal-elsabbagh-apex/copilot-mcp") || !strings.Contains(string(SafetyRules), "refresh:true") {
		t.Error("config guide must name the npx package and safety rules must mention refresh:true")
	}
}
