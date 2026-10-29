package queueitem

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// QueueItemContent is the canonical native-typed SpecificContent (arrays stay arrays,
// callbackContext is an object) WITHOUT serverURL/queueUrl: each backend's serializer fills
// those from its own config and shapes the wire format.
type QueueItemContent struct {
	Content   map[string]any `json:"content"`
	QueueName string         `json:"queueName"`
	Notes     []string       `json:"notes"`
	Meta      map[string]any `json:"meta"`
}

// BuiltQueueItem adds BeBase, the Copilot BE the order was read from: the serverURL fallback for
// backends whose config pins no server URL for this env.
type BuiltQueueItem struct {
	QueueItemContent
	BeBase string
}

// AssembleInput is everything Build reads from the outside world, so assembly stays pure.
type AssembleInput struct {
	Order        copilot.BeOrder
	OrderUID     string
	Profile      string // "" = none
	Env          config.Env
	AutomationID string // already resolved: explicit > account table; "" = unknown
	Token        string
	PhysicianID  any // string | float64 | nil (JWT id claim)
	SupportMode  bool
	ProviderMail string
}

// CallbackContext is the object the bot's BE callbacks read. A struct, not a map, because UiPath
// receives it as a JSON string whose key order is wire-visible (the v1.33.1 output pins it).
type CallbackContext struct {
	PhysicianID any    `json:"physicianId"`
	OrderUID    string `json:"orderUid"`
}

// Assemble maps an order onto SpecificContent. IsApproved is ALWAYS false (repo CLAUDE.md
// rule); backends re-assert it through orchestrator.GuardQueueItemSafety.
func Assemble(in AssembleInput) QueueItemContent {
	o := in.Order
	notes := []string{}

	form := o.Get("referralFormJSON")
	from, to := shared.Prop(form, "from"), shared.Prop(form, "to")
	fac := o.Get("referredFacility")
	cpts, icds := list(o.Get("CPTCodes")), list(o.Get("ICDCodes"))
	memberLast, memberFirst := SplitName(o.Str("patient", "patientName"))
	fromName := str(shared.Prop(from, "name"))
	fromLast, fromFirst := SplitName(fromName)
	// facStr reads a string off the referred facility ("" when absent).
	facStr := func(k string) string { s, _ := shared.Prop(fac, k).(string); return s }

	dos := cmp.Or(ToMDY(o.Str("encounterDate")), ToMDY(o.Str("appointmentDate")), ToMDY(o.Str("orderDate")))
	if o.Str("encounterDate") == "" {
		notes = append(notes, "DOS: order had no encounterDate; fell back to appointment/order date (may be empty).")
	}
	if slices.ContainsFunc(cpts, func(c any) bool { return strings.ContainsAny(str(shared.Prop(c, "description")), ":;") }) {
		notes = append(notes, "DescriptionOfService uses the order's full CPT descriptions (the BE's short-form descriptions are not in the order JSON).")
	}
	if in.Token == "" {
		notes = append(notes, "WARNING: login returned no token — SpecificContent.token is empty; the bot's BE callbacks will fail.")
	}
	if in.AutomationID == "" {
		notes = append(notes, fmt.Sprintf("WARNING: no automationId was supplied and none is known for account '%s' "+
			"— SpecificContent.automationId is a placeholder random UUID, not the account's real "+
			"automation id. Pass the correct value via the automationId argument before submitting.", cmp.Or(in.Profile, "(none)")))
	}
	if in.SupportMode {
		notes = append(notes, fmt.Sprintf("physicianId %s is the support account's user id — under support-account auth the JWT 'id' claim is the support user in both pre- and post-switch tokens; callbackContext carries it as-is.", physicianIDText(in.PhysicianID)))
	}

	strs := func(f func(c any) any) []any {
		out := make([]any, 0, len(cpts))
		for _, c := range cpts {
			out = append(out, f(c))
		}
		return out
	}
	treatments := []any{}
	for _, c := range cpts {
		treatments = append(treatments, list(shared.Prop(c, "treatments"))...)
	}
	diagnoses := make([]any, 0, len(icds))
	for _, i := range icds {
		diagnoses = append(diagnoses, shared.Prop(i, "code"))
	}

	meta := map[string]any{
		"profile": cmp.Or(in.Profile, "(default)"), "env": in.Env, "physicianId": in.PhysicianID,
		"queueName": o.Str("insurance", "name") + " auth submit queue",
	}
	meta["orderStatus"] = o.Get("status") // null when absent, as the TS emits

	content := map[string]any{
		"ClinicNPI":                   str(shared.Prop(from, "NPI")),
		"DOS":                         dos,
		"DescriptionOfService":        strs(func(c any) any { return str(shared.Prop(c, "description")) }),
		"Diagnoses":                   diagnoses,
		"Facility":                    cmp.Or(facStr("name"), str(shared.Prop(to, "name"))),
		"IsApproved":                  false, // HARD RULE — never true
		"IsRetro":                     truthy(o.Get("retro")),
		"IsSameAsRequestingProvider":  false,
		"IsUrgent":                    strings.EqualFold(str(o.Get("urgency")), "urgent"),
		"Location":                    o.Str("location"),
		"MemberDOB":                   ToMDY(o.Str("patient", "patientBirthDate")),
		"MemberFax":                   "",
		"MemberFirstName":             memberFirst,
		"MemberFullName":              o.Str("patient", "patientName"),
		"MemberID":                    o.Str("insurance", "memberId"),
		"MemberLastName":              memberLast,
		"MemberPhone":                 cmp.Or(str(shared.Prop(shared.Prop(form, "patient"), "phoneNumber")), o.Str("patient", "patientPhoneNumber")),
		"OfficeComments":              o.Str("comments"),
		"ProviderAddress":             str(shared.Prop(from, "address")),
		"ProviderCity":                "",
		"ProviderFax":                 str(shared.Prop(from, "faxNumber")),
		"ProviderFirstName":           cmp.Or(fromFirst, fromName),
		"ProviderFullName":            fromName,
		"ProviderID":                  "",
		"ProviderLastName":            "",
		"ProviderMail":                in.ProviderMail,
		"ProviderNPI":                 str(shared.Prop(from, "NPI")),
		"ProviderPhone":               str(shared.Prop(from, "phoneNumber")),
		"ProviderZipCode":             "",
		"ReferredToAddress":           cmp.Or(str(shared.Prop(to, "address")), facStr("address")),
		"ReferredToFax":               cmp.Or(str(shared.Prop(to, "faxNumber")), facStr("faxNumber")),
		"ReferredToNPI":               cmp.Or(str(shared.Prop(to, "NPI")), facStr("NPI")),
		"ReferredToOtherName":         "",
		"ReferredToPhone":             cmp.Or(str(shared.Prop(to, "phoneNumber")), facStr("phone")),
		"ReferredToProviderID":        "",
		"ReferredToType":              cmp.Or(facStr("type"), "provider"),
		"ServiceCode":                 strs(func(c any) any { return shared.Prop(c, "code") }),
		"Specialty":                   cmp.Or(o.Str("speciality", "name"), str(shared.Prop(to, "speciality"))),
		"Treatments":                  treatments,
		"Units":                       strs(func(c any) any { return str(shared.Prop(c, "units")) }),
		"addManualFacility":           false,
		"appointmentDate":             ToMDY(o.Str("appointmentDate")),
		"automationId":                cmp.Or(in.AutomationID, randomUUID()),
		"callbackContext":             CallbackContext{PhysicianID: in.PhysicianID, OrderUID: in.OrderUID},
		"eligibilityOtherPatientName": nil,
		"isSpeciality":                truthy(shared.Prop(fac, "isSpeciality")),
		"orderUid":                    in.OrderUID,
		"placeOfService":              cmp.Or(o.Str("placeOfService"), facStr("placeOfService")),
		"retryCount":                  0,
		"token":                       in.Token,
	}
	// The ProviderLastName rule: only when the full name really has a "Last, First" shape.
	if fromLast != "" && strings.Contains(fromName, ",") {
		content["ProviderLastName"] = fromLast
	}
	for _, k := range []string{
		"LinkedFacility", "LinkedFacilityAddress", "LinkedFacilityNPI", "LinkedFacilityOtherName", "LinkedFacilityType",
		"LinkedPlaceOfService", "LinkedProviderFullName", "LinkedProviderID", "LinkedProviderNPI", "LinkedProviderType", "LinkedSpecialty",
	} {
		content[k] = ""
	}

	return QueueItemContent{Content: content, QueueName: meta["queueName"].(string), Notes: notes, Meta: meta}
}
