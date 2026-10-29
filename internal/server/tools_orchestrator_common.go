package server

import (
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/backends"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"github.com/google/jsonschema-go/jsonschema"
)

// Orchestrator tools share one vocabulary: an explicit `env` and `orchestrator` (never defaulted)
// in, `{ orchestrator, env, ...result }` out. Writes take env as the literal pre_prod. The shared
// argument descriptions live in each args struct's jsonschema tag (a tag cannot reference a const).

// head is the `{ orchestrator, env }` prefix of every orchestrator tool result; embed it first.
type head struct {
	Orchestrator orchestrator.Orchestrator `json:"orchestrator"`
	Env          config.Env                `json:"env"`
}

// orchestratorEnum and envEnum are the schema enums every orchestrator tool shares.
func orchestratorEnum() Patch { return Enum("orchestrator", orchestrator.Orchestrators...) }

func envEnum() Patch { return Enum("env", config.Envs...) }

// preProdOnly is z.literal("pre_prod"): the schema itself refuses prod for every write tool.
func preProdOnly() Patch {
	return OnProp("env", func(s *jsonschema.Schema) { s.Const = new(any(string(config.PreProd))) })
}

func positiveInt() func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { s.ExclusiveMinimum = new(float64(0)) }
}

// backendOf is the backend serving o. The schema enum already refuses unknown values, so a nil
// here is a wiring bug, not user input.
func backendOf(o orchestrator.Orchestrator) (orchestrator.Backend, error) {
	if b := backends.BackendFor(o); b != nil {
		return b, nil
	}
	return nil, shared.Expectedf("unknown orchestrator %q", string(o))
}

// describe sets a property's description where the text holds backticks, which a struct tag cannot.
func describe(prop, text string) Patch {
	return OnProp(prop, func(s *jsonschema.Schema) { s.Description = text })
}
