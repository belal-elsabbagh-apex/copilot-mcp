package queueitem_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/queueitem"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/uipath"
)

const orderUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func readJSON(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func useUipathConfig(t *testing.T) {
	t.Helper()
	creds := func(n string) map[string]any {
		return map[string]any{"be": "https://be." + n + ".example.com", "email": n + "@example.com", "password": "pw"}
	}
	b, _ := json.Marshal(map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": creds("prod"), "pre_prod": creds("preprod")},
		"uipath": map[string]any{
			"orchestratorUrl":  "https://cloud.uipath.com/myorg/mytenant/orchestrator_/",
			"bearer":           "test-bearer",
			"queueUrl":         "https://sqs.example.com/queue",
			"addQueueItemPath": "/odata/Queues/UiPathODataSvc.AddQueueItem",
			"folderPath":       "Authorization Dev Clone",
			"serverUrlByEnv":   map[string]any{"pre_prod": "https://pre-prod-be.example.com"},
		},
	})
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", path)
	config.ResetForTest()
	t.Cleanup(config.ResetForTest)
}

// The v1.33.1 build_queue_item output for the fixture order, captured before any reshaping:
// content builder + UiPath serializer must keep reproducing it (notes/meta merged in that order).
func TestContentBuilderPlusUipathSerializerReproduceShippedV1331Output(t *testing.T) {
	useUipathConfig(t)
	built := queueitem.Assemble(queueitem.AssembleInput{
		Order: copilot.BeOrder(readJSON(t, "order.json")), OrderUID: orderUID, Profile: "ossm", Env: config.PreProd,
		AutomationID: "21a5ab32-817c-4a4a-95a0-bff16605ca6b",
		Token:        "eyJhbGciOiJub25lIn0.eyJpZCI6NDJ9.sig", PhysicianID: 42.0, SupportMode: true, ProviderMail: "owner@example.com",
	})
	p, err := uipath.Backend.BuildQueueItemPayload(t.Context(), orchestrator.QueueItemPayloadArgs{
		Env: config.PreProd, QueueName: built.QueueName, OrderUID: orderUID, Content: built.Content, BeBase: "https://be.preprod.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	meta := map[string]any{}
	for _, m := range []map[string]any{built.Meta, p.Meta} {
		for k, v := range m {
			meta[k] = v
		}
	}
	got := toJSON(t, map[string]any{"payload": p.Payload, "notes": append(built.Notes, p.Notes...), "meta": meta})

	// The Reference suffix is the build-time unix second (the TS test pinned Date.now).
	ref := regexp.MustCompile(`^` + orderUID + `-\d+$`)
	item := got["payload"].(map[string]any)["itemData"].(map[string]any)
	if r, _ := item["Reference"].(string); !ref.MatchString(r) {
		t.Fatalf("Reference = %q, want %s-<unix seconds>", r, orderUID)
	}
	item["Reference"] = orderUID + "-1780000000"

	if want := readJSON(t, "expected_v1_33_1.json"); !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		wb, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("v1.33.1 parity broken\n got: %s\nwant: %s", gb, wb)
	}
}

func toJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUipathServerURLFallsBackToTheBEOrderWasReadFromWhenEnvPinsNone(t *testing.T) {
	useUipathConfig(t)
	built := queueitem.Assemble(queueitem.AssembleInput{
		Order: copilot.BeOrder(readJSON(t, "order.json")), OrderUID: orderUID, Env: config.Prod, AutomationID: "a", Token: "t",
	})
	p, err := uipath.Backend.BuildQueueItemPayload(t.Context(), orchestrator.QueueItemPayloadArgs{
		Env: config.Prod, QueueName: built.QueueName, OrderUID: orderUID, Content: built.Content, BeBase: "https://be.preprod.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	sc := toJSON(t, p.Payload)["itemData"].(map[string]any)["SpecificContent"].(map[string]any)
	if sc["serverURL"] != "https://be.preprod.example.com" {
		t.Errorf("serverURL = %v, want the order's BE base", sc["serverURL"])
	}
}
