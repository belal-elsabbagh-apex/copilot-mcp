package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type schemaArgs struct {
	Profile string     `json:"profile" jsonschema:"Credential profile"`
	Env     config.Env `json:"env" jsonschema:"Env"`
	Limit   int        `json:"limit,omitempty" jsonschema:"Max candidates"`
	UIDs    []string   `json:"uids" jsonschema:"orderUids"`
	Kind    *string    `json:"kind,omitempty" jsonschema:"Optional kind"`
}

func testSchemaPatches() []Patch {
	return []Patch{
		MinLen("profile", 1),
		Enum("env", config.Envs...),
		Range("limit", 1, 50), Default("limit", 10),
		MinItems("uids", 1), ItemsOf("uids", func(s *jsonschema.Schema) { s.MinLength = new(8) }),
		Enum("kind", "bug", "feedback"),
	}
}

func TestInferredSchemaMatchesTheZodShape(t *testing.T) {
	sc := inputSchema[schemaArgs]("t", testSchemaPatches())
	b, _ := json.Marshal(sc)
	var got struct {
		Required   []string
		Properties map[string]map[string]any
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Required) != 3 { // profile, env, uids; limit/kind are optional
		t.Errorf("required = %v, want profile, env, uids", got.Required)
	}
	p := got.Properties
	checks := map[string]bool{
		"description lands":        p["profile"]["description"] == "Credential profile" && p["profile"]["minLength"] == float64(1),
		"named string enum":        p["env"]["type"] == "string" && len(p["env"]["enum"].([]any)) == 2,
		"integer bounds + default": p["limit"]["type"] == "integer" && p["limit"]["minimum"] == float64(1) && p["limit"]["maximum"] == float64(50) && p["limit"]["default"] == float64(10),
		"array item rule":          p["uids"]["minItems"] == float64(1) && p["uids"]["items"].(map[string]any)["minLength"] == float64(8),
		"pointer is not nullable":  p["kind"]["type"] == "string" && p["kind"]["enum"] != nil,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("schema check %q failed; property schemas: %s", name, b)
		}
	}
}

func TestSchemaPatchesAreEnforcedAndDefaultsApplied(t *testing.T) {
	s := newTestServer(t)
	var seen schemaArgs
	addTool(s, &mcp.Tool{Name: "t", Description: "test tool"}, func(_ context.Context, _ *mcp.CallToolRequest, in schemaArgs) (any, error) {
		seen = in
		return "ok", nil
	}, testSchemaPatches()...)
	cs, _ := connect(t, s)
	valid := map[string]any{"profile": "ossm", "env": "prod", "uids": []string{"abcdefgh"}}

	if res := callTool(t, cs, "t", valid); res.IsError || seen.Limit != 10 {
		t.Fatalf("valid call: isError=%v limit=%d, want success with default limit 10", res.IsError, seen.Limit)
	}
	invalid := map[string]map[string]any{
		"bad enum":         {"env": "staging"},
		"empty profile":    {"profile": ""},
		"limit too big":    {"limit": 51},
		"short uid":        {"uids": []string{"short"}},
		"no uids":          {"uids": []string{}},
		"null optional":    {"kind": nil},
		"kind not in enum": {"kind": "complaint"},
	}
	for name, override := range invalid {
		args := map[string]any{}
		for k, v := range valid {
			args[k] = v
		}
		for k, v := range override {
			args[k] = v
		}
		if res := callTool(t, cs, "t", args); !res.IsError {
			t.Errorf("%s: call succeeded, want the schema to reject it", name)
		}
	}
}

func TestPatchingAPropertyTheStructLacksPanicsAtRegistration(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("inputSchema() with a patch for an unknown property did not panic, want a registration-time failure")
		}
	}()
	inputSchema[schemaArgs]("t", []Patch{MinLen("nope", 1)})
}
