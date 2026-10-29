package orchestrator

import (
	"errors"
	"maps"
	"regexp"
	"slices"
	"testing"
)

var (
	limits = QueueSafetyLimits{
		PreProdServerURL: "https://pre-prod-be-batch.example.com",
		QueueURL:         "https://sqs.example.com/dev-queue",
		ConfigSection:    SectionUiPath,
	}
	noLimits = QueueSafetyLimits{ConfigSection: SectionUiPath}
)

func mustGuard(t *testing.T, sc map[string]any, mode SafetyMode, l QueueSafetyLimits) GuardedContent {
	t.Helper()
	out, err := GuardQueueItemSafety(sc, mode, l)
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	return out
}

func wantRefusal(t *testing.T, sc map[string]any, l QueueSafetyLimits, pattern string) {
	t.Helper()
	_, err := GuardQueueItemSafety(sc, SafetyPreProdPost, l)
	if err == nil {
		t.Fatalf("expected refusal matching %q", pattern)
	}
	if _, ok := errors.AsType[*SafetyError](err); !ok {
		t.Errorf("error is %T, want *SafetyError", err)
	}
	if !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Errorf("error %q does not match %q", err, pattern)
	}
}

func TestForceModeForcesIsApprovedFalseAndReportsIt(t *testing.T) {
	tests := []struct {
		name       string
		sc         map[string]any
		wantForced []string
	}{
		{"true is forced", map[string]any{"IsApproved": true, "orderUid": "u1"}, []string{"IsApproved"}},
		{"missing counts as forced", map[string]any{"orderUid": "u1"}, []string{"IsApproved"}},
		{"non-bool counts as forced", map[string]any{"IsApproved": "false"}, []string{"IsApproved"}},
		{"already false is not reported", map[string]any{"IsApproved": false}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := mustGuard(t, tt.sc, SafetyForce, noLimits)
			if out.SpecificContent["IsApproved"] != false {
				t.Errorf("IsApproved = %v", out.SpecificContent["IsApproved"])
			}
			if out.Forced == nil || !slices.Equal(out.Forced, tt.wantForced) {
				t.Errorf("forced = %#v, want %#v", out.Forced, tt.wantForced)
			}
		})
	}
}

func TestForceModeNeverMutatesTheInputAndOnlyOverridesIsApproved(t *testing.T) {
	sc := map[string]any{"MemberID": "m1", "IsApproved": true, "serverURL": "https://prod.example.com"}
	before := maps.Clone(sc)
	out := mustGuard(t, sc, SafetyForce, noLimits)
	if !maps.Equal(sc, before) {
		t.Errorf("input mutated: %v", sc)
	}
	want := maps.Clone(before)
	want["IsApproved"] = false
	if !maps.Equal(out.SpecificContent, want) {
		t.Errorf("got %v, want %v", out.SpecificContent, want)
	}
}

func TestForceModeDoesNotValidateURLsOrPlaceholders(t *testing.T) {
	mustGuard(t, map[string]any{
		"serverURL": "https://prod.example.com",
		"queueUrl":  "https://sqs.example.com/prod",
		"orderUid":  "<TO-FILL>",
	}, SafetyForce, limits)
}

func TestPreProdPostAcceptsABuildQueueItemShapedPayload(t *testing.T) {
	out := mustGuard(t, map[string]any{
		"IsApproved": false, "orderUid": "abc-123",
		"serverURL": limits.PreProdServerURL, "queueUrl": limits.QueueURL,
	}, SafetyPreProdPost, limits)
	if len(out.Forced) != 0 || out.SpecificContent["IsApproved"] != false {
		t.Errorf("got %+v", out)
	}
}

func TestPreProdPostAcceptsEmptyURLsBecauseSkillFixturesKeepThemBlank(t *testing.T) {
	out := mustGuard(t, map[string]any{"IsApproved": false, "orderUid": "abc", "serverURL": "", "queueUrl": ""}, SafetyPreProdPost, noLimits)
	if out.SpecificContent["orderUid"] != "abc" {
		t.Errorf("got %+v", out)
	}
}

func TestPreProdPostStillForcesIsApproved(t *testing.T) {
	out := mustGuard(t, map[string]any{"IsApproved": true, "orderUid": "abc"}, SafetyPreProdPost, limits)
	if out.SpecificContent["IsApproved"] != false || !slices.Equal(out.Forced, []string{"IsApproved"}) {
		t.Errorf("got %+v", out)
	}
}

func TestPreProdPostRefusals(t *testing.T) {
	tests := []struct {
		name    string
		sc      map[string]any
		limits  QueueSafetyLimits
		pattern string
	}{
		{"non-pre-prod serverURL", map[string]any{"serverURL": "https://prod-be.example.com"}, limits,
			`serverURL 'https://prod-be\.example\.com' is not the configured pre-prod host`},
		{"serverURL with the limit unconfigured fails closed", map[string]any{"serverURL": limits.PreProdServerURL}, noLimits,
			`serverUrlByEnv\.pre_prod is not configured`},
		{"foreign queueUrl", map[string]any{"queueUrl": "https://sqs.example.com/prod"}, limits,
			`queueUrl .* neither empty nor the configured`},
		{"any queueUrl when unconfigured", map[string]any{"queueUrl": "https://sqs.example.com/dev"}, noLimits,
			`queueUrl .* must be empty`},
		{"placeholders name their keys", map[string]any{"orderUid": "<TO-FILL>", "token": "< to_fill after mint >", "MemberID": "m1"}, limits,
			`placeholder .* orderUid, token`},
		{"placeholder nested in an array", map[string]any{"Diagnoses": []any{"<TO-FILL>"}}, limits,
			`placeholder .* Diagnoses`},
		{"placeholder nested in an object", map[string]any{"callbackContext": map[string]any{"physicianId": "<TO-FILL>"}}, limits,
			`placeholder .* callbackContext`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { wantRefusal(t, tt.sc, tt.limits, tt.pattern) })
	}
}

func TestPreProdPostAggregatesAllViolationsIntoOneError(t *testing.T) {
	_, err := GuardQueueItemSafety(map[string]any{"serverURL": "https://prod.example.com", "orderUid": "<TO-FILL>"}, SafetyPreProdPost, limits)
	se, ok := errors.AsType[*SafetyError](err)
	if !ok || len(se.Violations) != 2 {
		t.Fatalf("got %v", err)
	}
	want := "refusing to post queue item — serverURL 'https://prod.example.com' is not the configured pre-prod host " +
		"'https://pre-prod-be-batch.example.com'; placeholder (<TO-FILL>) values in: orderUid — mint/fill them first"
	if err.Error() != want {
		t.Errorf("got %q\nwant %q", err, want)
	}
}

func TestRefusalMessagesNameTheCallingBackendsConfigSection(t *testing.T) {
	apex := QueueSafetyLimits{QueueURL: "https://sqs.example.com/q", ConfigSection: SectionApex}
	wantRefusal(t, map[string]any{"serverURL": "https://x.example.com"}, apex, `apex\.serverUrlByEnv\.pre_prod`)
	wantRefusal(t, map[string]any{"queueUrl": "https://sqs.example.com/other"}, apex, `apex\.queueUrl`)
	// UiPath wording is unchanged
	wantRefusal(t, map[string]any{"serverURL": "https://x.example.com"}, noLimits, `uipath\.serverUrlByEnv\.pre_prod`)
}
