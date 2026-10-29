package queueitem

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

const uid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

// in builds an AssembleInput around order; mod tweaks the non-order inputs.
func in(order map[string]any, mod ...func(*AssembleInput)) AssembleInput {
	a := AssembleInput{
		Order: copilot.BeOrder(order), OrderUID: uid, Profile: "ossm", Env: config.PreProd,
		AutomationID: "auto-1", Token: "h.p.s", PhysicianID: 7.0, ProviderMail: "owner@example.com",
	}
	for _, m := range mod {
		m(&a)
	}
	return a
}

func hasNote(notes []string, prefix string) bool {
	for _, n := range notes {
		if strings.Contains(n, prefix) {
			return true
		}
	}
	return false
}

func TestAssembleEmitsNativeTypesAndLeavesBackendURLFieldsOut(t *testing.T) {
	c := Assemble(in(map[string]any{
		"ICDCodes": []any{map[string]any{"code": "I77.9"}},
		"CPTCodes": []any{map[string]any{"code": "99213", "description": "Visit", "units": 2.0, "treatments": []any{"ECG"}}},
	})).Content
	for k, want := range map[string]any{
		"Diagnoses": []any{"I77.9"}, "Units": []any{"2"}, "Treatments": []any{"ECG"},
		"callbackContext": CallbackContext{PhysicianID: 7.0, OrderUID: uid}, "IsApproved": false,
	} {
		if !reflect.DeepEqual(c[k], want) {
			t.Errorf("content[%s] = %#v, want %#v", k, c[k], want)
		}
	}
	for _, k := range []string{"serverURL", "queueUrl"} {
		if _, ok := c[k]; ok {
			t.Errorf("content has backend-owned %s", k)
		}
	}
}

func TestAssembleDOSPrefersEncounterThenAppointmentThenOrderDate(t *testing.T) {
	full := Assemble(in(map[string]any{"encounterDate": "1/2/2026", "appointmentDate": "3/4/2026", "orderDate": "5/6/2026"}))
	if full.Content["DOS"] != "01/02/2026" || hasNote(full.Notes, "DOS:") {
		t.Errorf("with encounterDate: DOS=%v notes=%v, want 01/02/2026 and no DOS note", full.Content["DOS"], full.Notes)
	}
	fb := Assemble(in(map[string]any{"appointmentDate": "3/4/2026", "orderDate": "5/6/2026"}))
	if fb.Content["DOS"] != "03/04/2026" || !hasNote(fb.Notes, "DOS:") {
		t.Errorf("fallback: DOS=%v notes=%v, want 03/04/2026 plus the DOS note", fb.Content["DOS"], fb.Notes)
	}
	if got := Assemble(in(map[string]any{"orderDate": "5/6/2026"})).Content["DOS"]; got != "05/06/2026" {
		t.Errorf("orderDate-only DOS = %v, want 05/06/2026", got)
	}
}

func TestAssembleReferralFormWinsOverFacilityWhichFillsGaps(t *testing.T) {
	c := Assemble(in(map[string]any{
		"referredFacility": map[string]any{"NPI": "FAC-NPI", "address": "Facility Rd", "phone": "555-0001", "type": "facility"},
		"referralFormJSON": map[string]any{"to": map[string]any{"NPI": "FORM-NPI"}},
	})).Content
	for k, want := range map[string]string{
		"ReferredToNPI": "FORM-NPI", "ReferredToAddress": "Facility Rd", "ReferredToPhone": "555-0001", "ReferredToType": "facility",
	} {
		if c[k] != want {
			t.Errorf("content[%s] = %v, want %s", k, c[k], want)
		}
	}
}

func TestAssembleProviderLastNameEmptyWithoutComma(t *testing.T) {
	c := Assemble(in(map[string]any{"referralFormJSON": map[string]any{"from": map[string]any{"name": "Clinic Group"}}})).Content
	if c["ProviderFirstName"] != "Clinic Group" || c["ProviderLastName"] != "" {
		t.Errorf("first/last = %v/%v, want the whole name as first and empty last", c["ProviderFirstName"], c["ProviderLastName"])
	}
}

func TestAssembleUnresolvedAutomationIDBecomesPlaceholderUUIDWithWarning(t *testing.T) {
	un := Assemble(in(map[string]any{}, func(a *AssembleInput) { a.AutomationID = "" }))
	if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(un.Content["automationId"].(string)) || !hasNote(un.Notes, "no automationId was supplied") {
		t.Errorf("unresolved: automationId=%v notes=%v", un.Content["automationId"], un.Notes)
	}
	res := Assemble(in(map[string]any{}))
	if res.Content["automationId"] != "auto-1" || hasNote(res.Notes, "no automationId") {
		t.Errorf("resolved: automationId=%v notes=%v", res.Content["automationId"], res.Notes)
	}
}

func TestAssembleMissingTokenIsFlaggedAndEmpty(t *testing.T) {
	out := Assemble(in(map[string]any{}, func(a *AssembleInput) { a.Token = "" }))
	if out.Content["token"] != "" || !hasNote(out.Notes, "login returned no token") {
		t.Errorf("token=%v notes=%v", out.Content["token"], out.Notes)
	}
}

func TestAssembleSupportModeAddsPhysicianNoteAndMetaCarriesQueueName(t *testing.T) {
	out := Assemble(in(
		map[string]any{"insurance": map[string]any{"name": "Acme Health"}, "status": "inProgress"},
		func(a *AssembleInput) { a.SupportMode = true },
	))
	if !hasNote(out.Notes, "physicianId 7 is the support account") || out.QueueName != "Acme Health auth submit queue" {
		t.Errorf("notes=%v queueName=%q", out.Notes, out.QueueName)
	}
	m := out.Meta
	if len(m) != 5 || m["profile"] != "ossm" || m["env"] != config.PreProd || m["physicianId"] != 7.0 ||
		m["queueName"] != "Acme Health auth submit queue" || m["orderStatus"] != "inProgress" {
		t.Errorf("meta = %v", m)
	}
}

func TestAssembleMetaOrderStatusIsNullWhenTheOrderHasNone(t *testing.T) {
	v, ok := Assemble(in(map[string]any{})).Meta["orderStatus"]
	if !ok || v != nil {
		t.Errorf("orderStatus = %v (present=%v), want a present null", v, ok)
	}
}
