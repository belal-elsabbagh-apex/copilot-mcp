package shared

import (
	"encoding/json"
	"testing"
)

func TestCoalesceFollowsJSNullishChain(t *testing.T) {
	row := map[string]any{"npi": nil, "num": float64(123), "name": "x"}
	cases := map[string]struct {
		got  Field
		want string // "" = omitted
	}{
		"number passes through":  {Coalesce(Pass(row, "missing"), Pass(row, "num")), "123"},
		"null skipped for value": {Coalesce(Pass(row, "npi"), Pass(row, "name")), `"x"`},
		"null last stays null":   {Coalesce(Pass(row, "missing"), Pass(row, "npi")), "null"},
		"absent last is omitted": {Coalesce(Pass(row, "npi"), Pass(row, "missing")), ""},
		"non-object operand":     {Coalesce(Pass(nil, "a"), Pass(row, "npi")), "null"},
	}
	for name, c := range cases {
		if c.want == "" {
			if c.got.Set {
				t.Errorf("%s: want omitted, got %+v", name, c.got)
			}
			continue
		}
		b, err := json.Marshal(c.got)
		if err != nil || string(b) != c.want {
			t.Errorf("%s: got %s (%v), want %s", name, b, err, c.want)
		}
	}
}

func TestNPIFieldIsAlwaysAString(t *testing.T) {
	row := map[string]any{"num": float64(1234567893), "str": "1234567893", "null": nil}
	cases := map[string]struct {
		got  Field
		want string
	}{
		"number becomes decimal string": {NPIField(Pass(row, "num")), `"1234567893"`},
		"string unchanged":              {NPIField(Pass(row, "str")), `"1234567893"`},
		"null stays null":               {NPIField(Pass(row, "null")), "null"},
		"absent is omitted":             {NPIField(Pass(row, "missing")), ""},
	}
	for name, c := range cases {
		if c.want == "" {
			if c.got.Set {
				t.Errorf("%s: want omitted, got %+v", name, c.got)
			}
			continue
		}
		if b, err := json.Marshal(c.got); err != nil || string(b) != c.want {
			t.Errorf("%s: got %s (%v), want %s", name, b, err, c.want)
		}
	}
}
