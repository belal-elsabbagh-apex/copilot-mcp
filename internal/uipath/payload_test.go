package uipath

import (
	"strings"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

const (
	payloadNow  = 1780000000
	payloadUID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	payloadPath = "/odata/Queues/UiPathODataSvc.AddQueueItem"
)

func payloadSetup(t *testing.T, over map[string]any) {
	t.Helper()
	f := newFake(t, okEmpty)
	cfg := map[string]any{
		"orchestratorUrl":  testOrchestratorURL + "/",
		"queueUrl":         "https://sqs.example.com/queue",
		"addQueueItemPath": payloadPath,
		"folderPath":       "Authorization Dev Clone",
		"serverUrlByEnv":   map[string]any{"pre_prod": "https://pre-prod-be.example.com"},
	}
	for k, v := range over {
		if v == nil {
			delete(cfg, k)
			continue
		}
		cfg[k] = v
	}
	f.useConfig(t, cfg)
	prev := now
	now = func() time.Time { return time.Unix(payloadNow, 0) }
	t.Cleanup(func() { now = prev })
}

func payloadFor(t *testing.T, env config.Env, content map[string]any) orchestrator.QueueItemPayload {
	t.Helper()
	p, err := buildQueueItemPayload(t.Context(), orchestrator.QueueItemPayloadArgs{
		Env: env, QueueName: "Acme Health auth submit queue", OrderUID: payloadUID, Content: content, BeBase: "https://be.preprod.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func specificOf(t *testing.T, p orchestrator.QueueItemPayload) map[string]any {
	t.Helper()
	return p.Payload.(map[string]any)["itemData"].(map[string]any)["SpecificContent"].(map[string]any)
}

func TestSerializeContentStringifiesExactlyTheArrayFieldsAndCallbackContext(t *testing.T) {
	src := map[string]any{
		"Diagnoses": []any{"I77.9"}, "Units": []any{}, "callbackContext": map[string]any{"physicianId": 7, "orderUid": "u"},
		"IsRetro": true, "retryCount": 0, "OpenReferralInfo": nil, "MemberID": "M1",
	}
	out, err := serializeContent(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"Diagnoses": `["I77.9"]`, "Units": `[]`, "callbackContext": `{"orderUid":"u","physicianId":7}`,
		"IsRetro": true, "retryCount": 0, "OpenReferralInfo": nil, "MemberID": "M1",
	}
	if jsonOf(t, out) != jsonOf(t, want) {
		t.Fatalf("out = %s", jsonOf(t, out))
	}
	if _, ok := src["Diagnoses"].([]any); !ok {
		t.Error("serialization mutated its input")
	}
	lacking, _ := serializeContent(map[string]any{"MemberID": "M1"})
	if len(lacking) != 1 {
		t.Errorf("invented a stringified field: %v", lacking)
	}
}

func TestBuildQueueItemPayloadShape(t *testing.T) {
	payloadSetup(t, nil)
	p := payloadFor(t, config.PreProd, map[string]any{"MemberID": "M1", "Diagnoses": []any{"I77.9"}, "IsApproved": true})
	item := p.Payload.(map[string]any)["itemData"].(map[string]any)
	if item["Name"] != "Acme Health auth submit queue" || item["Priority"] != "Normal" || item["Reference"] != payloadUID+"-1780000000" {
		t.Fatalf("itemData = %v", item)
	}
	sc := specificOf(t, p)
	if sc["queueUrl"] != "https://sqs.example.com/queue" || sc["serverURL"] != "https://pre-prod-be.example.com" || sc["Diagnoses"] != `["I77.9"]` {
		t.Fatalf("specificContent = %v", sc)
	}
	if sc["IsApproved"] != false {
		t.Errorf("IsApproved must be forced false, got %v", sc["IsApproved"])
	}
	wantNote := "To submit: POST payload.itemData as JSON to meta.postUrl with headers 'Authorization: Bearer <your uipath.bearer>' and 'X-UIPATH-FolderPath: Authorization Dev Clone'. Never done automatically by this tool."
	if len(p.Notes) != 1 || p.Notes[0] != wantNote {
		t.Errorf("notes = %q", p.Notes)
	}
	if p.Meta["postUrl"] != testOrchestratorURL+payloadPath || p.Meta["folderPath"] != "Authorization Dev Clone" {
		t.Errorf("meta = %v", p.Meta)
	}
}

func TestBuildQueueItemPayloadServerURLFallsBackToTheOrdersBE(t *testing.T) {
	payloadSetup(t, nil)
	if got := specificOf(t, payloadFor(t, config.Prod, map[string]any{}))["serverURL"]; got != "https://be.preprod.example.com" {
		t.Fatalf("serverURL = %v", got)
	}
}

func TestBuildQueueItemPayloadNamesEveryMissingConfigField(t *testing.T) {
	payloadSetup(t, map[string]any{"queueUrl": nil, "addQueueItemPath": nil, "folderPath": nil})
	_, err := buildQueueItemPayload(t.Context(), orchestrator.QueueItemPayloadArgs{Env: config.PreProd, Content: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "missing fields required for build_queue_item: queueUrl, addQueueItemPath, folderPath") {
		t.Fatalf("err = %v", err)
	}
}
