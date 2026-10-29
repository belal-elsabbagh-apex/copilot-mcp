package orchestrator

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// The single test-safety enforcement point for queue-item SpecificContent. Every path that hands
// a payload to (or toward) a robot goes through GuardQueueItemSafety:
//   - SafetyForce (env-agnostic): pull_queue_item / build_queue_item — force IsApproved=false so a
//     test run can never submit a real auth.
//   - SafetyPreProdPost (add_queue_item): force, PLUS hard asserts that the item cannot point a
//     dev-clone robot at prod — serverURL/queueUrl must match the configured pre-prod values, and
//     no `<TO-FILL>` placeholders may remain.
//
// Pure and config-free: the caller injects the configured limits.

// ConfigSection names the config block a refusal message points the operator at.
type ConfigSection string

// The config sections that carry write-guard limits.
const (
	SectionUiPath ConfigSection = "uipath"
	SectionApex   ConfigSection = "apex"
)

// QueueSafetyLimits are the configured write-guard limits.
type QueueSafetyLimits struct {
	PreProdServerURL string        // config <section>.serverUrlByEnv.pre_prod, "" when unset
	QueueURL         string        // config <section>.queueUrl, "" when unset
	ConfigSection    ConfigSection // names the caller's config keys in refusal messages; "" reads as uipath
}

// SafetyMode selects how strict the guard is.
type SafetyMode string

// The guard modes.
const (
	SafetyForce       SafetyMode = "force"
	SafetyPreProdPost SafetyMode = "preProdPost"
)

// GuardedContent is the guard's result.
type GuardedContent struct {
	SpecificContent map[string]any // copy of the input with IsApproved forced false
	Forced          []string       // fields the guard overrode, e.g. ["IsApproved"]; never nil
}

// SafetyError is the refusal of a SafetyPreProdPost guard; Violations are ALL the problems found.
type SafetyError struct {
	Violations []string
}

func (e *SafetyError) Error() string {
	return "refusing to post queue item — " + strings.Join(e.Violations, "; ")
}

// placeholderRE matches `<TO-FILL>`-style placeholders left over from a fixture that was never
// minted. The class is JS `\s`, matching what the TS guard accepted.
var placeholderRE = regexp.MustCompile(`(?i)<[` + jsSpace + `]*TO[-_ ]?FILL`)

// GuardQueueItemSafety validates + forces test-safety on a SpecificContent record. Never mutates
// the input. In SafetyPreProdPost mode it returns one *SafetyError aggregating ALL violations.
// limits is only read by SafetyPreProdPost, where the zero value fails closed on any non-empty
// pointer field.
func GuardQueueItemSafety(sc map[string]any, mode SafetyMode, limits QueueSafetyLimits) (GuardedContent, error) {
	forced := []string{}
	if approved, ok := sc["IsApproved"].(bool); !ok || approved {
		forced = append(forced, "IsApproved")
	}
	specificContent := maps.Clone(sc)
	if specificContent == nil {
		specificContent = map[string]any{}
	}
	specificContent["IsApproved"] = false
	guarded := GuardedContent{SpecificContent: specificContent, Forced: forced}
	if mode == SafetyForce {
		return guarded, nil
	}

	section := limits.ConfigSection
	if section == "" {
		section = SectionUiPath
	}
	var violations []string
	if serverURL := shared.Str(sc, "serverURL"); serverURL != "" {
		switch {
		case limits.PreProdServerURL == "":
			violations = append(violations, fmt.Sprintf(
				"item carries a serverURL but %s.serverUrlByEnv.pre_prod is not configured — "+
					"set it so the guard can verify the callback host is pre-prod", section))
		case serverURL != limits.PreProdServerURL:
			violations = append(violations, fmt.Sprintf(
				"serverURL '%s' is not the configured pre-prod host '%s'", serverURL, limits.PreProdServerURL))
		}
	}
	if queueURL := shared.Str(sc, "queueUrl"); queueURL != "" && queueURL != limits.QueueURL {
		if limits.QueueURL == "" {
			violations = append(violations, fmt.Sprintf(
				"queueUrl '%s' must be empty (%s.queueUrl is not configured)", queueURL, section))
		} else {
			violations = append(violations, fmt.Sprintf(
				"queueUrl '%s' is neither empty nor the configured %s.queueUrl", queueURL, section))
		}
	}
	// Sorted, not insertion order: Go maps carry none, and the message must be deterministic.
	var placeholderKeys []string
	for _, k := range slices.Sorted(maps.Keys(sc)) {
		if hasPlaceholder(sc[k]) {
			placeholderKeys = append(placeholderKeys, k)
		}
	}
	if len(placeholderKeys) > 0 {
		violations = append(violations, fmt.Sprintf(
			"placeholder (<TO-FILL>) values in: %s — mint/fill them first", strings.Join(placeholderKeys, ", ")))
	}
	if len(violations) > 0 {
		return GuardedContent{}, &SafetyError{Violations: violations}
	}
	return guarded, nil
}

// hasPlaceholder scans v's JSON form (nested arrays/objects included). HTML escaping is off or
// the encoder would turn "<" into \u003c and the guard would never match.
func hasPlaceholder(v any) bool {
	b, err := shared.MarshalJSON(v)
	if err != nil {
		// Fail closed on an unmarshalable value: scan its printed form instead of skipping it.
		return placeholderRE.MatchString(fmt.Sprint(v))
	}
	return placeholderRE.Match(b)
}
