package shared

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"
)

// Prop reads a string-keyed property off an untyped JSON value: nil when v is not an object or
// the key is absent. Centralizes the comma-ok dance so call sites never assert inline — a bare
// assertion panics the day the upstream API changes a field's shape.
func Prop(v any, key string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m[key]
}

// StringProp reads a property only when it is a string.
func StringProp(v any, key string) (string, bool) {
	s, ok := Prop(v, key).(string)
	return s, ok
}

// EnvelopeRows pulls the rows out of the common `{ "data": [...] }` envelope, or nil when the
// shape isn't as expected.
func EnvelopeRows(v any) []any {
	rows, _ := Prop(v, "data").([]any)
	return rows
}

// MsBetween is the milliseconds between two ISO timestamps; ok=false when either is missing or
// unparseable.
func MsBetween(start, end string) (ms int64, ok bool) {
	a, okA := ParseTime(start)
	b, okB := ParseTime(end)
	if !okA || !okB {
		return 0, false
	}
	return b.Sub(a).Milliseconds(), true
}

// ParseTime parses the ISO timestamps the orchestrator APIs emit. A zoneless stamp reads as UTC;
// JS Date.parse, which the TS port relied on, read it as local time — only differences between
// two stamps were ever used, and those agree either way.
func ParseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// SafeJSONParse parses text as JSON, falling back to the raw string when it isn't valid JSON
// ("" -> nil). For HTTP response bodies that have to be read defensively.
func SafeJSONParse(text string) any {
	if text == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return text
	}
	return v
}

// StepProgress reports progress for tools whose slowness is many small steps rather than one long
// call (settings section crawls, sync actions, page sweeps, per-job fetches). Optional at every
// call site — a domain function must work with a nil reporter; use Step to call it safely.
type StepProgress func(done, total int, label string)

// Step calls p when it is set.
func (p StepProgress) Step(done, total int, label string) {
	if p != nil {
		p(done, total, label)
	}
}

// Field is a BE value passed through to a tool result with TS's three states: absent (TS
// `undefined` — the key is omitted, use `omitzero`), JSON null (marshals `null`) or a value.
type Field struct {
	V   any
	Set bool
}

// IsZero makes `omitzero` drop an absent field and keep a null one.
func (f Field) IsZero() bool { return !f.Set }

// MarshalJSON renders V as-is (nil → null).
func (f Field) MarshalJSON() ([]byte, error) { return MarshalJSON(f.V) }

// MarshalJSON is json.Marshal without HTML escaping: JSON.stringify never turns < > & into \u003c
// etc., and every MarshalJSON method or request body must stay byte-identical to it (a method's
// output is copied into the tool result as-is, so escaping here would leak to the client).
func MarshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// NPIField normalizes a BE NPI to a string: the BE has sent NPIs as JSON numbers, but an NPI is an
// identifier, so a number becomes its exact decimal string (never exponent form). Strings, null
// and absent pass through unchanged. Deliberately differs from the TS server, which passed
// numbers through.
func NPIField(f Field) Field {
	if n, ok := f.V.(float64); ok {
		return Field{V: strconv.FormatFloat(n, 'f', -1, 64), Set: f.Set}
	}
	return f
}

// Coalesce is JS `a ?? b ?? c`: the first Field that is present and not null wins (of any type);
// when every operand is nullish the LAST one is returned as-is, so a trailing null stays null and
// a trailing absent stays omitted.
func Coalesce(fields ...Field) Field {
	for _, f := range fields {
		if f.Set && f.V != nil {
			return f
		}
	}
	if len(fields) == 0 {
		return Field{}
	}
	return fields[len(fields)-1]
}

// Pass reads the path with JS optional chaining (`v?.a?.b`): a missing or null intermediate, or a
// missing leaf, is absent; a leaf that is JSON null is a null Field.
func Pass(v any, keys ...string) Field {
	for i, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return Field{}
		}
		if v, ok = m[k]; !ok {
			return Field{}
		}
		if v == nil && i < len(keys)-1 {
			return Field{}
		}
	}
	return Field{V: v, Set: true}
}

// Str reads a string property of an untyped JSON value, "" when absent or not a string.
func Str(v any, key string) string {
	s, _ := StringProp(v, key)
	return s
}

// NonEmpty is s as a nullable JSON string: "" -> null (TS `string | null`).
func NonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
