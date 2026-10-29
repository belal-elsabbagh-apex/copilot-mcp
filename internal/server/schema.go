package server

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// Tool input schemas are inferred from the In struct by the SDK's jsonschema package:
//
//   - the json tag name is the property name; a field WITHOUT omitempty/omitzero is required;
//   - `jsonschema:"text"` is the zod .describe() text (the tag carries ONLY the description);
//   - Go types give string/integer/number/boolean/array/object; named string types are strings;
//   - pointers are "optional + nullable" in the SDK, but zod optional is not nullable, so
//     inputSchema collapses ["null", T] back to T (a client sending null is rejected, as in TS).
//
// Everything else zod expressed (enum, default, min/max, minLength, format, array item rules) is
// declared with a Patch passed to addTool. Defaults are applied by the SDK before the input is
// decoded, so a `Default` patch is what makes the zod `.default(10)` behaviour work; the field
// must then be a plain non-pointer value with `omitempty` (optional in the schema, as in zod).
//
// Worked example — TS:
//
//	inputSchema: {
//	  profile: z.string().min(1).describe("Credential profile / account name from config (required)"),
//	  env: z.enum(["prod", "pre_prod"]).describe("Env"),
//	  limit: z.number().int().min(1).max(50).optional().default(10).describe("Max candidates"),
//	  uids: z.array(z.string().min(8)).min(1).describe("orderUids"),
//	  kind: z.enum(["bug", "feedback"]).optional().describe("..."),
//	}
//
// Go:
//
//	type findArgs struct {
//		Profile string     `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
//		Env     config.Env `json:"env" jsonschema:"Env"`
//		Limit   int        `json:"limit,omitempty" jsonschema:"Max candidates"`
//		UIDs    []string   `json:"uids" jsonschema:"orderUids"`
//		Kind    string     `json:"kind,omitempty" jsonschema:"..."`
//	}
//
//	addTool(s, &mcp.Tool{Name: "find", Title: "...", Description: "...", Annotations: readOnlyHints(true)},
//		find,
//		MinLen("profile", 1),
//		Enum("env", config.Envs...),
//		Range("limit", 1, 50), Default("limit", 10),
//		MinItems("uids", 1), ItemsOf("uids", func(s *jsonschema.Schema) { s.MinLength = new(8) }),
//		Enum("kind", "bug", "feedback"),
//	)
//
// A Patch naming a property the struct does not have panics at registration, so a typo fails the
// first test run instead of silently doing nothing.

// Patch adjusts one property of an inferred input schema.
type Patch struct {
	prop  string
	apply func(*jsonschema.Schema)
}

// OnProp is the escape hatch: run fn on the schema of property prop.
func OnProp(prop string, fn func(*jsonschema.Schema)) Patch { return Patch{prop, fn} }

// Enum restricts a string property to vals (z.enum). Accepts any string-based type, e.g.
// config.Envs... or orchestrator.Orchestrators....
func Enum[T ~string](prop string, vals ...T) Patch {
	return OnProp(prop, func(s *jsonschema.Schema) {
		s.Enum = nil
		for _, v := range vals {
			s.Enum = append(s.Enum, string(v))
		}
	})
}

// Default sets the schema default (z.default). The SDK fills it in when the client omits the field.
func Default(prop string, v any) Patch {
	return OnProp(prop, func(s *jsonschema.Schema) { s.Default = mustMarshal(v) })
}

// MinLen is z.string().min(n).
func MinLen(prop string, n int) Patch {
	return OnProp(prop, func(s *jsonschema.Schema) { s.MinLength = &n })
}

// Range is z.number().min(lo).max(hi).
func Range(prop string, lo, hi float64) Patch {
	return OnProp(prop, func(s *jsonschema.Schema) { s.Minimum, s.Maximum = &lo, &hi })
}

// MinItems is z.array(...).min(n).
func MinItems(prop string, n int) Patch {
	return OnProp(prop, func(s *jsonschema.Schema) { s.MinItems = &n })
}

// ItemsOf patches the element schema of an array property (z.array(z.string().min(8))).
func ItemsOf(prop string, fn func(*jsonschema.Schema)) Patch {
	return OnProp(prop, func(s *jsonschema.Schema) { fn(s.Items) })
}

// inputSchema infers the schema for In, collapses pointer nullability, and applies patches.
func inputSchema[In any](tool string, patches []Patch) *jsonschema.Schema {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool %q: inferring input schema: %v", tool, err))
	}
	for _, p := range schema.Properties {
		dropNull(p)
	}
	for _, p := range patches {
		prop, ok := schema.Properties[p.prop]
		if !ok {
			panic(fmt.Sprintf("tool %q: schema patch for unknown property %q", tool, p.prop))
		}
		p.apply(prop)
	}
	return schema
}

// dropNull turns ["null", T] into T, recursively through array items.
func dropNull(s *jsonschema.Schema) {
	if len(s.Types) == 2 && slices.Contains(s.Types, "null") {
		s.Type = s.Types[1-slices.Index(s.Types, "null")]
		s.Types = nil
	}
	if s.Items != nil {
		dropNull(s.Items)
	}
	for _, p := range s.Properties {
		dropNull(p)
	}
}

// mustMarshal is for values the program itself constructs (schema defaults); a failure is a bug.
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
