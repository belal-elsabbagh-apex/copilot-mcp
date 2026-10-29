package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOrchestratorReferenceDescribesDevCloneAsRealBECallsAndPersistentRefreshableCaches(t *testing.T) {
	b, err := json.Marshal(orchestratorReference())
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, "skips all BE notifications") || strings.Contains(text, "dry-run gate of its own") {
		t.Error("orchestrators resource still describes the dev clone as a BE sandbox")
	}
	if !strings.Contains(text, `"pre_prod":{"name":"Authorization Dev Clone","organizationUnitId":"434039","folderKey":"db6a5e70-7d8b-4113-afbc-a425eb81f015","realBeCalls":true}`) {
		t.Error("UiPath pre_prod folder must report realBeCalls:true — the dev clone does not gate BE calls")
	}
	if !strings.Contains(text, "refresh:true") {
		t.Error("orchestrators resource lost the cache/refresh note")
	}
}
