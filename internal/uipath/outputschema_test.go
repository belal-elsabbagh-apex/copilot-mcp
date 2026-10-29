package uipath

import (
	"slices"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

func keysOf(f orchestrator.Fields) []string {
	out := make([]string, len(f))
	for i, kv := range f {
		out[i] = kv.Key
	}
	return out
}

func TestJobOutputFieldsKeepOnlyOutPrefixedPriorityThenAlphabetical(t *testing.T) {
	got := keysOf(normalizeOutputFields(map[string]any{
		"out_Result": "Success", "out_OrderUid": "order-123", "out_Account": "ossm", "out_Zeta": 1, "out_Alpha": 2, "unrelated": "ignored",
	}))
	if want := []string{"out_OrderUid", "out_Result", "out_Account", "out_Alpha", "out_Zeta"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestTransactionItemFieldsHideTokenAndCallbackContext(t *testing.T) {
	got := keysOf(normalizeOutputFields(map[string]any{"transactionItem": map[string]any{
		"SpecificContent": map[string]any{"orderUid": "order-999", "MemberID": "M1", "token": "secret-jwt", "callbackContext": "{}"},
	}}))
	if want := []string{"MemberID", "orderUid"}; !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func TestUnknownOutputShapeFallsBackToTheRawEntries(t *testing.T) {
	got := normalizeOutputFields(map[string]any{"foo": "bar"})
	if len(got) != 1 || got[0].Key != "foo" || got[0].Value != "bar" {
		t.Fatalf("fields = %v", got)
	}
}

func TestFieldsMarshalAsAnOrderedObject(t *testing.T) {
	f := orchestrator.Fields{{Key: "b", Value: 1}, {Key: "a", Value: "x<y"}}
	if got := jsonOf(t, f); got != `{"b":1,"a":"x\u003cy"}` && got != `{"b":1,"a":"x<y"}` {
		t.Fatalf("json = %s", got)
	}
	if got := jsonOf(t, orchestrator.Fields{}); got != `{}` {
		t.Fatalf("empty = %s", got)
	}
}
