package server

import (
	"bytes"
	"encoding/json"
	"strings"
)

// renderJSON is JSON.stringify(v, null, 2): two-space indent, no trailing newline, and HTML
// escaping OFF (encoding/json would turn "<ASK-USER>" into \u003cASK-USER\u003e). Every tool
// result and resource body goes through it so the text is byte-comparable with the TS server.
func renderJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
