package orchestrator

import "github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"

// Field is one ordered key/value of a JSON object.
type Field struct {
	Key   string
	Value any
}

// Fields is an ordered field list that marshals as a JSON object in that order (a Go map would
// sort the keys and lose the priority order).
type Fields []Field

// MarshalJSON writes the fields as an object, in order.
func (f Fields) MarshalJSON() ([]byte, error) {
	out := []byte{'{'}
	for i, kv := range f {
		if i > 0 {
			out = append(out, ',')
		}
		k, err := shared.MarshalJSON(kv.Key)
		if err != nil {
			return nil, err
		}
		v, err := shared.MarshalJSON(kv.Value)
		if err != nil {
			return nil, err
		}
		out = append(append(append(out, k...), ':'), v...)
	}
	return append(out, '}'), nil
}
