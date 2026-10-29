package settings

import (
	"encoding/json"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// undefined marks a value that is absent (TS `undefined`), as distinct from a JSON null. It
// matters in two places: diffs (a key missing on one side is not the same as null there) and the
// JSON edge (an absent value is omitted, a null is kept).
type undefined struct{}

var undef any = undefined{}

func isUndef(v any) bool {
	_, ok := v.(undefined)
	return ok
}

// canonical is the stable JSON of an already-normalized value (object keys and arrays are sorted
// by stripNoise, so the encoding is canonical). Absent -> "undefined", so a present key with a
// null value never collides with an absent key.
func canonical(v any) string {
	if isUndef(v) {
		return "undefined"
	}
	b, err := shared.MarshalJSON(v)
	if err != nil {
		return "undefined"
	}
	return string(b)
}

// rawOrOmit encodes v, or returns nil (omitted via omitempty on a RawMessage) when v is absent.
func rawOrOmit(v any) (json.RawMessage, error) {
	if isUndef(v) {
		return nil, nil
	}
	return shared.MarshalJSON(v)
}

// nonNil returns s, or an empty slice when s is nil, so it marshals as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// MarshalJSON omits a side that is absent.
func (d FieldDiff) MarshalJSON() ([]byte, error) {
	prod, err := rawOrOmit(d.Prod)
	if err != nil {
		return nil, err
	}
	pre, err := rawOrOmit(d.Preprod)
	if err != nil {
		return nil, err
	}
	return shared.MarshalJSON(struct {
		Path    string          `json:"path"`
		Prod    json.RawMessage `json:"prod,omitempty"`
		Preprod json.RawMessage `json:"preprod,omitempty"`
	}{d.Path, prod, pre})
}

type sectionHead struct {
	Key   string      `json:"key"`
	Label string      `json:"label"`
	Kind  SectionKind `json:"kind"`
	Equal bool        `json:"equal"`
}

// MarshalJSON emits the TS shape: key/label/kind/equal, then `error` for a failed fetch, `diffs`
// for an object section, or the three list fields for a list section.
func (r SectionResult) MarshalJSON() ([]byte, error) {
	h := sectionHead{r.Key, r.Label, r.Kind, r.Equal}
	switch {
	case r.Error != "":
		return shared.MarshalJSON(struct {
			sectionHead
			Error string `json:"error"`
		}{h, r.Error})
	case r.Kind == KindObject:
		return shared.MarshalJSON(struct {
			sectionHead
			Diffs []FieldDiff `json:"diffs"`
		}{h, nonNil(r.Diffs)})
	}
	return shared.MarshalJSON(struct {
		sectionHead
		OnlyInProd    []any         `json:"onlyInProd"`
		OnlyInPreProd []any         `json:"onlyInPreProd"`
		Changed       []ChangedItem `json:"changed"`
	}{h, nonNil(r.OnlyInProd), nonNil(r.OnlyInPreProd), nonNil(r.Changed)})
}

// MarshalJSON emits key/label/tags/kind, then `error`, or `count` (list sections) and `data`.
func (s GetSettingsSection) MarshalJSON() ([]byte, error) {
	type head struct {
		Key   string      `json:"key"`
		Label string      `json:"label"`
		Tags  []string    `json:"tags"`
		Kind  SectionKind `json:"kind"`
	}
	h := head{s.Key, s.Label, nonNil(s.Tags), s.Kind}
	if s.Error != "" {
		return shared.MarshalJSON(struct {
			head
			Error string `json:"error"`
		}{h, s.Error})
	}
	data, err := rawOrOmit(s.Data)
	if err != nil {
		return nil, err
	}
	return shared.MarshalJSON(struct {
		head
		Count *int            `json:"count,omitempty"`
		Data  json.RawMessage `json:"data,omitempty"`
	}{h, s.Count, data})
}
