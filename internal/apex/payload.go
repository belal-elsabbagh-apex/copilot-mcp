package apex

// Apex queue-item serializer. Unlike UiPath's (which stringifies the array/object fields), Apex
// stores specific_content as native JSON — BE-built items carry real arrays, booleans, numbers and
// a callbackContext object — so the canonical content from internal/queueitem is kept as-is and
// checked against the native-type map (docs/superpowers/specs/2026-10-11-apex-orchestrator-integration-design.md §9) instead of converted. A mismatch is a builder bug,
// never user input, so it is a plain (non-Expected) error.

import (
	"fmt"
	"maps"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

type kind string

const (
	kindArray   kind = "array"
	kindBoolean kind = "boolean"
	kindNumber  kind = "number"
	kindObject  kind = "object"
)

// nativeFields: OpenReferralInfo, eligibilityOtherPatientName and ReferredToReferralDistribution
// may be null, so they are not constrained here. Fields absent from the content are not required.
var nativeFields = []struct {
	key  string
	kind kind
}{
	{"Units", kindArray}, {"Diagnoses", kindArray}, {"Modifiers", kindArray}, {"Treatments", kindArray},
	{"ServiceCode", kindArray}, {"DescriptionOfService", kindArray},
	{"IsRetro", kindBoolean}, {"IsUrgent", kindBoolean}, {"IsApproved", kindBoolean},
	{"isSpeciality", kindBoolean}, {"addManualFacility", kindBoolean}, {"IsSameAsRequestingProvider", kindBoolean},
	{"retryCount", kindNumber}, {"ReferredToCompleteReferralsCount", kindNumber},
	{"ReferredToSuccessfulReferralsCount", kindNumber},
	{"callbackContext", kindObject},
}

// kindOf is the native kind of v, or the JS-style type name when it is none of the four.
func kindOf(v any) kind {
	switch v.(type) {
	case nil:
		return "null"
	case []any, []string, []map[string]any:
		return kindArray
	case bool:
		return kindBoolean
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return kindNumber
	case map[string]any:
		return kindObject
	case string:
		return "string"
	}
	return "object"
}

// serializeContent checks content against the native-type map and returns a shallow copy.
func serializeContent(content map[string]any) (map[string]any, error) {
	var wrong []string
	for _, f := range nativeFields {
		v, ok := content[f.key]
		if ok && kindOf(v) != f.kind {
			wrong = append(wrong, fmt.Sprintf("%s (expected %s, got %s)", f.key, f.kind, kindOf(v)))
		}
	}
	if len(wrong) > 0 {
		return nil, fmt.Errorf("Apex queue-item content breaks the native-type map: %s — "+ //nolint:staticcheck // user-visible wording kept from the TS
			"the canonical builder must emit native arrays/booleans/numbers/objects, not UiPath-style strings", strings.Join(wrong, ", "))
	}
	return maps.Clone(content), nil
}

// matchValueOf: Apex routes an item by match_value; BE-built items use match_key "automationId"
// with match_value == specific_content.automationId (verified live). Shared by build + add so both
// derive it identically.
func matchValueOf(specificContent map[string]any) (string, error) {
	id, ok := specificContent["automationId"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return "", shared.Expectedf("specific_content.automationId is required — Apex routes the item by match_value = " +
			"automationId (build_queue_item sets it; pass automationId to override)")
	}
	return id, nil
}
