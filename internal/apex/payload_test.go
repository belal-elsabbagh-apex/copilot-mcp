package apex

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func nativeContent() map[string]any {
	return map[string]any{
		"Units": []any{"1"}, "Diagnoses": []any{"M54.5"}, "Modifiers": []any{}, "Treatments": []any{"T1"},
		"ServiceCode": []any{"99213"}, "DescriptionOfService": []any{"Office visit"},
		"IsRetro": false, "IsUrgent": true, "IsApproved": false, "isSpeciality": false,
		"addManualFacility": false, "IsSameAsRequestingProvider": false,
		"retryCount": float64(0), "ReferredToCompleteReferralsCount": float64(3), "ReferredToSuccessfulReferralsCount": float64(2),
		"callbackContext":  map[string]any{"physicianId": float64(7), "orderUid": "ord-1"},
		"OpenReferralInfo": nil, "eligibilityOtherPatientName": nil, "ReferredToReferralDistribution": nil,
		"automationId": "auto-1", "orderUid": "ord-1",
	}
}

func TestSerializeContentKeepsNativeTypesAndCopies(t *testing.T) {
	in := nativeContent()
	out, err := serializeContent(in)
	if err != nil || !reflect.DeepEqual(out, in) {
		t.Fatalf("serializeContent = %v, %v, want an equal copy", out, err)
	}
	out["x"] = 1
	if _, leaked := in["x"]; leaked {
		t.Error("result aliases the input map")
	}
	if _, ok := out["Units"].([]any); !ok || out["OpenReferralInfo"] != nil {
		t.Errorf("native values changed: %v", out)
	}
}

func TestSerializeContentAcceptsPartialContent(t *testing.T) {
	out, err := serializeContent(map[string]any{"orderUid": "ord-1"})
	if err != nil || !maps.Equal(out, map[string]any{"orderUid": "ord-1"}) {
		t.Errorf("serializeContent = %v, %v", out, err)
	}
}

func TestSerializeContentRejectsUiPathStyleStringifiedValues(t *testing.T) {
	for field, tc := range map[string]struct {
		value any
		want  string
	}{
		"Diagnoses":       {`["M54.5"]`, "Diagnoses (expected array, got string)"},
		"callbackContext": {`{"physicianId":7}`, "callbackContext (expected object, got string)"},
		"IsUrgent":        {"false", "IsUrgent (expected boolean, got string)"},
		"retryCount":      {"0", "retryCount (expected number, got string)"},
		"Units":           {nil, "Units (expected array, got null)"},
	} {
		c := nativeContent()
		c[field] = tc.value
		if _, err := serializeContent(c); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", field, err, tc.want)
		} else if shared.IsExpected(err) {
			t.Errorf("%s: error is Expected, want a plain builder-bug error", field)
		}
	}
}

func TestSerializeContentReportsEveryViolationInOneError(t *testing.T) {
	c := nativeContent()
	c["Units"], c["IsRetro"] = "1", "no"
	if _, err := serializeContent(c); err == nil || !strings.Contains(err.Error(), "Units") || strings.Index(err.Error(), "Units") > strings.Index(err.Error(), "IsRetro") {
		t.Errorf("error = %v, want both violations, Units first", err)
	}
}

func TestMatchValueOf(t *testing.T) {
	if got, err := matchValueOf(map[string]any{"automationId": "auto-1"}); err != nil || got != "auto-1" {
		t.Errorf("matchValueOf = %q, %v", got, err)
	}
	for _, sc := range []map[string]any{{}, {"automationId": ""}, {"automationId": "   "}, {"automationId": 5}} {
		if _, err := matchValueOf(sc); err == nil || !shared.IsExpected(err) {
			t.Errorf("matchValueOf(%v) error = %v, want an expected refusal", sc, err)
		}
	}
}
