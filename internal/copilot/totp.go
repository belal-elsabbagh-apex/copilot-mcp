package copilot

// TOTP step-up for support accounts. Pre-prod's physician/login answers a support account with
// 403 SUPPORT_MFA_REQUIRED {additionalInfo:{mfaToken, mode}}; mode "verify" is completed with
// POST /support/mfa/verify (Bearer mfaToken, {otp}). Derived from the pre-prod web app bundle
// (Login.*.js, supportMfaPaths.*.js) — not yet proven live. Never log or return the otp, the
// secret or the mfaToken: a wrong code counts toward lockout, so every refusal here is final.

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 default HMAC; the authenticator apps the BE enrols use SHA-1
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	mfaVerifyPath = "/api/v1/support/mfa/verify"
	mfaRequired   = "SUPPORT_MFA_REQUIRED"
)

// totpNow is the clock, a hook so tests can pin the 30 s step.
var totpNow = time.Now

// totp is RFC 6238 over HMAC-SHA-1 with a 30 s step, `digits` digits.
func totp(key []byte, t time.Time, digits int) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(t.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

// mfaChallenge reads a login 403 body; ok=false when it is not the support MFA challenge.
func mfaChallenge(data any) (token, mode string, ok bool) {
	e := shared.Prop(data, "error")
	if code, _ := shared.StringProp(e, "code"); code != mfaRequired {
		return "", "", false
	}
	info := shared.Prop(e, "additionalInfo")
	token, _ = shared.StringProp(info, "mfaToken")
	mode, _ = shared.StringProp(info, "mode")
	return token, mode, true
}

// completeMFA answers a SUPPORT_MFA_REQUIRED login challenge. It never retries.
func completeMFA(ctx context.Context, client *httpClient, env config.Env, data any, totpSecret string) (loginResult, error) {
	secretKey := fmt.Sprintf("copilot.support.%s.totpSecret", env)
	mfaToken, mode, _ := mfaChallenge(data)
	if mode == "enroll" {
		return loginResult{}, shared.Expectedf("login rejected: support account has no authenticator enrolled; enroll once in the web app")
	}
	if mode != "verify" || mfaToken == "" {
		return loginResult{}, shared.Expectedf("login rejected: support MFA challenge has an unsupported shape (mode %q)", mode)
	}
	if totpSecret == "" {
		return loginResult{}, shared.Expectedf("login rejected: support account requires MFA — set %s in the config", secretKey)
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.NewReplacer(" ", "", "=", "").Replace(totpSecret)))
	if err != nil || len(key) == 0 {
		return loginResult{}, shared.Expectedf("login rejected: %s is not valid base32", secretKey)
	}
	r, err := client.Req(ctx, "POST", mfaVerifyPath, ReqBody{
		JSON:   map[string]any{"otp": totp(key, totpNow(), 6)},
		Header: map[string]string{"Authorization": "Bearer " + mfaToken},
	})
	if err != nil {
		return loginResult{}, err
	}
	if r.Status >= 400 {
		code, _ := shared.StringProp(shared.Prop(r.Data, "error"), "code")
		return loginResult{}, shared.Expectedf("login rejected: support MFA verify failed %d (%s); not retried — wrong codes count toward lockout", r.Status, cmp.Or(code, http.StatusText(r.Status)))
	}
	token, _ := shared.StringProp(r.Data, "token")
	if token == "" {
		return loginResult{}, fmt.Errorf("support MFA verify succeeded but returned no token")
	}
	return loginResult{client, token}, nil
}
