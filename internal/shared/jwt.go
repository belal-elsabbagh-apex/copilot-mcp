package shared

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// JWTClaim base64-decodes JWT segment 1 and reads one claim. Never fails — a malformed token or
// missing claim reads as nil. Decoding is lenient like Node's Buffer.from(_, "base64"): padding
// optional, url-safe alphabet accepted.
func JWTClaim(token, claim string) any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimRight(parts[1], "="))
	raw, err := base64.RawStdEncoding.DecodeString(seg)
	if err != nil {
		return nil
	}
	var payload any
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	return Prop(payload, claim)
}
