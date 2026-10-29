package uipath

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

func rawLog(raw string) Log {
	l := logRow("Info", "Transaction Ended")
	l.RawMessage = raw
	return l
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseRawMessageIsNilWhenAbsentOrUnparseable(t *testing.T) {
	for _, raw := range []string{"", "not json", `"just a json string, not an object"`, "[1]"} {
		if got := parseRawMessage(rawLog(raw)); got != nil {
			t.Errorf("parseRawMessage(%q) = %+v, want nil", raw, got)
		}
	}
}

// Shape confirmed live against real Transaction Ended rows (2026-09-07).
func TestParseRawMessageExtractsTheTransactionEndedFields(t *testing.T) {
	raw := jsonOf(t, map[string]any{
		"message": "Transaction Ended", "level": "Information", "transactionState": "Ended",
		"transactionExecutionTime": 49.93, "transactionId": "d211b063-20eb-45a9-801a-5634cd13568a",
		"queueItemPriority": "Normal", "processingExceptionReason": "", "queueName": "CenCal auth sync queue",
		"processingExceptionType": "BusinessException", "queueItemReviewStatus": "None",
		// Confirmed broken live: UiPath logs the unresolved InArgument binding here, never the
		// actual status — must never surface as a real field.
		"transactionStatus": "System.Activities.InArgument`1[UiPath.Core.ProcessingStatus]",
	})
	p := parseRawMessage(rawLog(raw))
	if p == nil {
		t.Fatal("parseRawMessage = nil")
	}
	checks := map[string]struct{ got, want string }{
		"transactionId":           {deref(p.TransactionID), "d211b063-20eb-45a9-801a-5634cd13568a"},
		"transactionState":        {deref(p.TransactionState), "Ended"},
		"queueName":               {deref(p.QueueName), "CenCal auth sync queue"},
		"processingExceptionType": {deref(p.ProcessingExceptionType), "BusinessException"},
		"queueItemPriority":       {deref(p.QueueItemPriority), "Normal"},
		"queueItemReviewStatus":   {deref(p.QueueItemReviewStatus), "None"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", name, c.got, c.want)
		}
	}
	if p.TransactionExecutionTimeSec == nil || *p.TransactionExecutionTimeSec != 49.93 {
		t.Errorf("transactionExecutionTimeSec = %v", p.TransactionExecutionTimeSec)
	}
	if p.ProcessingExceptionReason != nil {
		t.Error("a present-but-blank field collapses to null")
	}
	if strings.Contains(jsonOf(t, p), "transactionStatus") {
		t.Error("transactionStatus must never surface")
	}
	if len(p.Custom) != 0 {
		t.Errorf("known fields leaked into custom: %v", p.Custom)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestParseRawMessageExtractsExecutionEndedTotals(t *testing.T) {
	p := parseRawMessage(rawLog(`{"message":"X execution ended","totalExecutionTimeInSeconds":52,"totalExecutionTime":"00:00:52"}`))
	if p == nil || p.TotalExecutionTimeInSeconds == nil || *p.TotalExecutionTimeInSeconds != 52 {
		t.Fatalf("parsed = %+v", p)
	}
}

// Confirmed live: Studio's "Add Log Fields" activity output shows up as an ordinary top-level
// RawMessage key under whatever name the process author chose.
func TestParseRawMessageCollectsUnrecognizedFieldsIntoCustomVerbatim(t *testing.T) {
	p := parseRawMessage(rawLog(`{"message":"Healing agent configuration.","healingAgentConfig":{"orchestratorEnableHeal":false}}`))
	want := map[string]any{"healingAgentConfig": map[string]any{"orchestratorEnableHeal": false}}
	if p == nil || !maps.EqualFunc(p.Custom, want, func(a, b any) bool { return jsonOf(t, a) == jsonOf(t, b) }) {
		t.Fatalf("custom = %v", p)
	}
}

func TestParseRawMessageOrdinaryRowHasAllFieldsNull(t *testing.T) {
	p := parseRawMessage(rawLog(`{"message":"Login successful.","level":"Information"}`))
	if p == nil || p.TransactionID != nil || p.ProcessingExceptionType != nil || p.TotalExecutionTimeInSeconds != nil || len(p.Custom) != 0 {
		t.Fatalf("parsed = %+v", p)
	}
	if got := jsonOf(t, p); !strings.Contains(got, `"activityInfo":null`) || !strings.Contains(got, `"custom":{}`) {
		t.Fatalf("json = %s", got)
	}
}
