package server

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Port of the v2.0.0 TS src/server.test.ts' schema acceptance tables. They run over the tools that are
// registered, so each tool set is held to the TS contract as it lands (exact tool presence is
// TestRegistersExactlyTheExpectedTools' job). Validation here is the schema alone — the same
// check the SDK applies before a handler runs; rules the schema cannot express (JSON-schema
// `format`, which the SDK does not enforce, and the XOR rules) belong to the handlers.

const (
	jobID     = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	processID = "609b3602-b6f2-44b2-9ee2-6a8988fac1f5"
)

type m = map[string]any

// toolSchemas resolves every registered tool's advertised input schema.
func toolSchemas(t *testing.T) map[string]*jsonschema.Resolved {
	t.Helper()
	out := map[string]*jsonschema.Resolved{}
	for _, tool := range registeredTools(t) {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var s jsonschema.Schema
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		r, err := s.Resolve(nil)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		out[tool.Name] = r
	}
	return out
}

func accepts(t *testing.T, r *jsonschema.Resolved, args m) bool {
	t.Helper()
	b, _ := json.Marshal(args)
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return r.Validate(v) == nil
}

var schemaCases = map[string]struct{ valid, invalid m }{
	// profile is required (any non-empty string; validated against config at call time); env is
	// required on every env-taking tool. Invalid cases omit a required field or use a wrong type.
	"find_clone_candidates": {m{"profile": "ossm"}, m{}},
	"delete_preprod_order":  {m{"uids": []string{"abcdefgh"}, "profile": "ossm"}, m{"uids": []string{}, "profile": "ossm"}},
	"create_preprod_order": {
		m{"profile": "ossm", "patientName": "DOE, JOHN", "patientBirthDate": "01/02/1980", "insuranceName": "Acme Health", "typeUid": "abcdefgh-type", "orderNamesUids": []string{"abcdefgh-name"}, "icdCodes": []m{{"code": "M54.5"}}},
		m{"patientName": "DOE, JOHN", "patientBirthDate": "01/02/1980", "insuranceName": "Acme Health", "typeUid": "abcdefgh-type", "orderNamesUids": []string{"abcdefgh-name"}}, // profile is required
	},
	"submit_preprod_order": {m{"profile": "ossm", "orderUid": "abcdefgh"}, m{"orderUid": "abcdefgh"}},
	"build_queue_item": {
		m{"orderUid": "abcdefgh", "env": "pre_prod", "profile": "ossm", "orchestrator": "uipath"},
		m{"orderUid": "abcdefgh", "env": "pre_prod", "profile": "ossm"}, // orchestrator is required
	},
	"find_order_queue_items": {m{"orderUid": "abcdefgh", "env": "prod"}, m{"orderUid": "abcdefgh"}}, // orchestrator omitted = every orchestrator
	"find_stuck_orders": {
		m{"env": "prod", "profile": "ossm", "crossCheck": true},
		m{"env": "prod", "profile": "ossm", "orchestrator": "nope"},
	},
	"pull_queue_item":       {m{"orchestrator": "uipath", "env": "pre_prod", "itemId": "123"}, nil}, // url "not a url" is a handler rule: format is not enforced
	"list_queue_items":      {m{"env": "pre_prod", "orchestrator": "uipath", "queue": "PMG", "status": "New"}, m{"env": "pre_prod", "orchestrator": "uipath", "queue": "PMG", "status": "Bogus"}},
	"diff_settings":         {m{"tags": []string{"orders"}, "profile": "ossm"}, m{"profile": 123}},
	"list_setting_sections": {m{"tag": "orders"}, m{"tag": 123}},
	"get_settings":          {m{"env": "pre_prod", "profile": "ossm", "tags": []string{"orders"}}, m{"profile": "ossm"}},
	"plan_settings_sync":    {m{"profile": "ossm", "tags": []string{"orders"}}, m{}},
	// The actionIds-XOR-all rule is enforced at runtime (Expected error), not in the schema.
	"apply_settings_sync":      {m{"profile": "ossm", "actionIds": []string{"specialties:create:MRI:Cardiology"}}, m{"actionIds": []string{"specialties:create:MRI:Cardiology"}}},
	"get_order":                {m{"orderUid": "abcdefgh", "env": "prod", "profile": "ossm"}, m{"orderUid": "abcdefgh", "env": "prod"}},
	"search_orders":            {m{"env": "prod", "profile": "ossm", "insurances": []string{"OPTUM CARE NETWORK"}}, m{"profile": "ossm"}},
	"get_order_category_stats": {m{"env": "prod", "profile": "ossm"}, m{"profile": "ossm"}},
	"get_login_token":          {m{"env": "prod", "profile": "ossm"}, m{"profile": "ossm"}},
	"list_clinics":             {m{"env": "prod"}, m{}},
	"doctor":                   {m{"profile": "ossm"}, m{}},
	"build_faulted_job_issue":  {m{"env": "prod", "orchestrator": "uipath", "jobId": jobID}, m{"env": "prod", "jobId": jobID}},
	"list_jobs":                {m{"env": "prod", "orchestrator": "uipath", "processName": "OPTUM"}, m{"env": "prod", "processName": "OPTUM"}},
	// The jobId-XOR-jobIds rule is enforced at runtime, not in the schema.
	"get_job_logs":   {m{"env": "prod", "orchestrator": "uipath", "jobIds": []string{jobID, jobID}}, m{"env": "prod", "orchestrator": "uipath", "jobIds": slices.Repeat([]string{jobID}, 26)}}, // max 25
	"list_queues":    {m{"env": "pre_prod", "orchestrator": "uipath", "nameContains": "auth"}, m{"env": "pre_prod", "nameContains": "auth"}},
	"list_processes": {m{"env": "pre_prod", "orchestrator": "uipath"}, m{"env": "pre_prod"}},
	"list_triggers":  {m{"env": "pre_prod", "orchestrator": "uipath", "queueId": "79926"}, m{"env": "pre_prod", "queueId": "79926"}},
	"get_job":        {m{"env": "pre_prod", "orchestrator": "uipath", "jobId": jobID}, m{"env": "pre_prod", "jobId": jobID}},
	"get_job_artifacts": {
		m{"env": "pre_prod", "jobId": jobID, "download": []string{"job.log", "trace.zip"}},
		m{"env": "pre_prod", "jobId": jobID, "download": "everything"}, // "all" or a non-empty filename list
	},
	"add_queue_item": {
		m{"env": "pre_prod", "orchestrator": "uipath", "queue": "Scratch queue", "reference": "TEST-1-abcd1234", "specificContent": m{"orderUid": "abcd1234"}},
		m{"env": "prod", "orchestrator": "uipath", "queue": "Scratch queue", "reference": "TEST-1-abcd1234", "specificContent": m{"orderUid": "abcd1234"}}, // env is the literal pre_prod
	},
	"delete_queue_item": {m{"env": "pre_prod", "orchestrator": "uipath", "itemId": "123"}, m{"env": "prod", "orchestrator": "uipath", "itemId": "123"}},
	"start_job":         {m{"env": "pre_prod", "orchestrator": "uipath", "processId": processID}, m{"env": "prod", "orchestrator": "uipath", "processId": processID}},
	"stop_job":          {m{"env": "pre_prod", "orchestrator": "uipath", "jobId": jobID, "strategy": "Kill"}, m{"env": "prod", "orchestrator": "uipath", "jobId": jobID}},
	"build_mcp_issue": {
		m{"kind": "feedback", "title": "clone_order output is hard to scan", "details": "The per-order rows mix successes and failures; a split summary would read better."},
		m{"kind": "complaint", "title": "x", "details": "too short"},
	},
}

func TestToolInputSchemasAcceptRepresentativePayloadsAndRejectInvalidOnes(t *testing.T) {
	schemas := toolSchemas(t)
	for name, c := range schemaCases {
		r, ok := schemas[name]
		if !ok {
			continue
		}
		if !accepts(t, r, c.valid) {
			t.Errorf("%s: rejected the valid payload %v", name, c.valid)
		}
		if c.invalid != nil && accepts(t, r, c.invalid) {
			t.Errorf("%s: accepted the invalid payload %v", name, c.invalid)
		}
	}
}

// Every tool that dispatches through backendFor() must name its orchestrator, and only an enabled one.
func TestOrchestratorToolsRejectAnUnknownOrchestratorAndApexWritesRejectProd(t *testing.T) {
	schemas := toolSchemas(t)
	apex := m{"env": "pre_prod", "orchestrator": "apex"}
	with := func(extra m) m {
		out := m{}
		for _, src := range []m{apex, extra} {
			for k, v := range src {
				out[k] = v
			}
		}
		return out
	}
	apexPayloads := map[string]m{
		"list_jobs":               with(m{"processName": "CENCAL"}),
		"get_job":                 with(m{"jobId": jobID, "includeLogDigest": true}),
		"get_job_logs":            with(m{"jobId": jobID, "tail": 50}),
		"build_faulted_job_issue": with(m{"jobId": jobID}),
		"list_queues":             with(m{"nameContains": "auth"}),
		"list_queue_items":        with(m{"queue": "CENCAL", "status": "New"}),
		"find_order_queue_items":  with(m{"orderUid": "abcdefgh"}),
		"pull_queue_item":         with(m{"queueId": jobID, "itemId": jobID}),
		"list_processes":          with(nil),
		"list_triggers":           with(m{"queueId": jobID}),
		"build_queue_item":        with(m{"orderUid": "abcdefgh", "profile": "ossm"}),
		"find_stuck_orders":       with(m{"profile": "ossm", "crossCheck": true}),
		"add_queue_item":          with(m{"queue": "CENCAL auth submit queue", "reference": "ord-1-1700000000", "specificContent": m{"orderUid": "abcdefgh"}}),
		"delete_queue_item":       with(m{"itemId": jobID, "queueId": jobID}),
		"start_job":               with(m{"processId": jobID, "matchValue": "auto-1"}),
		"stop_job":                with(m{"jobId": jobID, "strategy": "Kill"}),
	}
	optionalOrchestrator := []string{"find_order_queue_items", "find_stuck_orders"} // omitted = every backend
	apexWrites := []string{"add_queue_item", "delete_queue_item", "start_job", "stop_job"}
	for name, valid := range apexPayloads {
		r, ok := schemas[name]
		if !ok {
			continue
		}
		if !accepts(t, r, valid) {
			t.Errorf("%s: rejected an apex payload %v", name, valid)
		}
		bogus := with(valid)
		bogus["orchestrator"] = "bogus"
		if accepts(t, r, bogus) {
			t.Errorf("%s: accepted orchestrator=bogus", name)
		}
		noOrch := with(valid)
		delete(noOrch, "orchestrator")
		if got, want := accepts(t, r, noOrch), slices.Contains(optionalOrchestrator, name); got != want {
			t.Errorf("%s: accepts omitted orchestrator = %v, want %v", name, got, want)
		}
		if prod := with(valid); slices.Contains(apexWrites, name) {
			prod["env"] = "prod"
			if accepts(t, r, prod) {
				t.Errorf("%s: accepted env=prod for apex, want the schema literal to refuse it", name)
			}
		}
	}
}

// The cache override is exactly these four tools' optional boolean, default false.
func TestOnlyTheCachedReadToolsTakeAnOptionalBooleanRefresh(t *testing.T) {
	base := m{"env": "pre_prod", "jobId": jobID}
	withOrch := m{"env": "pre_prod", "orchestrator": "apex", "jobId": jobID}
	cases := map[string]m{
		"get_job": withOrch, "get_job_logs": withOrch, "build_faulted_job_issue": withOrch, "get_job_artifacts": base,
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range registeredTools(t) {
		byName[tool.Name] = tool
	}
	schemas := toolSchemas(t)
	for name, payload := range cases {
		with := func(v any) m {
			out := m{"refresh": v}
			for k, x := range payload {
				out[k] = x
			}
			return out
		}
		if !accepts(t, schemas[name], with(true)) || accepts(t, schemas[name], with("yes")) {
			t.Errorf("%s: refresh must accept a boolean and reject a string", name)
		}
		raw, _ := json.Marshal(byName[name].InputSchema)
		var s struct {
			Properties map[string]struct {
				Type        string `json:"type"`
				Default     any    `json:"default"`
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		p := s.Properties["refresh"]
		if p.Type != "boolean" || p.Default != false || !strings.HasPrefix(p.Description, "Bypass cached results and re-fetch from upstream") {
			t.Errorf("%s: refresh = %+v", name, p)
		}
		if slices.Contains(s.Required, "refresh") {
			t.Errorf("%s: refresh must be optional", name)
		}
	}
	for name := range schemas {
		if _, ok := cases[name]; ok {
			continue
		}
		raw, _ := json.Marshal(byName[name].InputSchema)
		if strings.Contains(string(raw), `"refresh"`) {
			t.Errorf("%s: must not take refresh", name)
		}
	}
}
