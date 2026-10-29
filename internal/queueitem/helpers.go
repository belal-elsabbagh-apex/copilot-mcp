package queueitem

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ToMDY is the non-throwing MM/DD/YYYY normalizer (queue payloads prefer "" over an error) —
// the date-parsing rules themselves live once, in copilot.ToMDY.
func ToMDY(input string) string {
	s, err := copilot.ToMDY(input)
	if err != nil {
		return ""
	}
	return s
}

// DecodeJwtID is the JWT payload's `id` claim (string or number), nil when absent or unreadable.
func DecodeJwtID(token string) any {
	switch id := shared.JWTClaim(token, "id").(type) {
	case string, float64:
		return id
	}
	return nil
}

// SplitName: "Last, First M" -> LAST / FIRST (first token after the comma, uppercased).
func SplitName(full string) (last, first string) {
	s := strings.TrimSpace(full)
	before, after, found := strings.Cut(s, ",")
	if !found {
		return strings.ToUpper(s), ""
	}
	fields := strings.Fields(after)
	if len(fields) > 0 {
		first = strings.ToUpper(fields[0])
	}
	return strings.ToUpper(strings.TrimSpace(before)), first
}

// ResolveAutomationID: an explicit automationId always wins; otherwise resolve it from
// `profile` (the account name from config, e.g. "kafri"/"sclc"/"ossm") against the known
// account table, matched case-insensitively. "" when neither source has one.
func ResolveAutomationID(explicit, profile string) string {
	if e := strings.TrimSpace(explicit); e != "" {
		return e
	}
	id, _ := reference.AutomationID(strings.ToUpper(profile))
	return id
}

// str is JS String(v) with null/undefined as "".
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

// truthy is JS truthiness for untyped JSON.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	}
	return true
}

// list is a JSON array as []any; nil when absent or not an array.
func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// randomUUID is a v4 UUID (the placeholder automationId when none is known).
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// physicianIDText renders the id the way a JS template literal would (null -> "null").
func physicianIDText(id any) string {
	if id == nil {
		return "null"
	}
	return str(id)
}
