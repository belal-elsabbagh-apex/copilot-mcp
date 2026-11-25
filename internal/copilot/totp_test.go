package copilot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

const verifyPath = "/api/v1/support/mfa/verify"

func TestTOTPMatchesRFC6238Vector(t *testing.T) {
	key := []byte("12345678901234567890")
	at := time.Unix(59, 0)
	if got := totp(key, at, 8); got != "94287082" {
		t.Errorf("totp(T=59, 8 digits) = %s, want 94287082", got)
	}
	if got := totp(key, at, 6); got != "287082" {
		t.Errorf("totp(T=59, 6 digits) = %s, want 287082", got)
	}
}

// mfaBE answers physician/login with the MFA challenge in the given mode, then handles verify.
func mfaBE(b *beServer, mode string, verify func(w http.ResponseWriter)) {
	b.handler = func(w http.ResponseWriter, path string) {
		switch {
		case strings.Contains(path, "physician/login"):
			w.WriteHeader(403)
			fmt.Fprintf(w, `{"error":{"code":"SUPPORT_MFA_REQUIRED","additionalInfo":{"mfaToken":"mfa-secret-token","mode":%q}}}`, mode)
		case strings.Contains(path, "mfa/verify"):
			verify(w)
		default:
			defaultHandler(w, path)
		}
	}
}

func TestConnectSupportAnswersTheMFAChallengeThenSwitchesClinic(t *testing.T) {
	be := setup(t, nil)
	totpNow = func() time.Time { return time.Unix(59, 0) }
	t.Cleanup(func() { totpNow = time.Now })
	mfaBE(be, "verify", func(w http.ResponseWriter) { fmt.Fprintf(w, `{"token":%q}`, liveToken()) })

	s, err := Connect(t.Context(), config.Prod, "ossm")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.paths(), []string{loginPath, verifyPath, switchPath}) {
		t.Errorf("paths = %v, want login, verify, switch-clinic", be.paths())
	}
	// 6-digit code of the RFC key at T=59 (testTOTPSecret is that key), sent with the challenge's Bearer token
	if be.bodies[1]["otp"] != "287082" || be.auths[1] != "Bearer mfa-secret-token" {
		t.Errorf("verify otp = %v, auth = %q", be.bodies[1]["otp"], be.auths[1])
	}
	if s.ActiveClinicID == nil || *s.ActiveClinicID != 25 {
		t.Errorf("session = %+v", s)
	}
}

func TestConnectRefusesEnrollModeWithoutVerifying(t *testing.T) {
	be := setup(t, nil)
	mfaBE(be, "enroll", func(http.ResponseWriter) { t.Error("verify must not be called in enroll mode") })
	_, err := Connect(t.Context(), config.Prod, "ossm")
	if err == nil || !strings.Contains(err.Error(), "enroll once in the web app") {
		t.Errorf("err = %v", err)
	}
	if !slices.Equal(be.paths(), []string{loginPath}) {
		t.Errorf("paths = %v, want only login", be.paths())
	}
}

func TestConnectRefusesMFAChallengeWithoutATOTPSecret(t *testing.T) {
	be := setup(t, nil)
	raw, err := os.ReadFile(os.Getenv("COPILOT_MCP_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	delete(cfg["copilot"].(map[string]any)["support"].(map[string]any)["prod"].(map[string]any), "totpSecret")
	raw, _ = json.Marshal(cfg)
	if err := os.WriteFile(os.Getenv("COPILOT_MCP_CONFIG"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	config.ResetForTest()
	mfaBE(be, "verify", func(http.ResponseWriter) { t.Error("verify must not be called without a secret") })

	_, err = Connect(t.Context(), config.Prod, "ossm")
	if err == nil || !strings.Contains(err.Error(), "copilot.support.prod.totpSecret") || strings.Contains(err.Error(), "mfa-secret-token") {
		t.Errorf("err = %v, want it to name copilot.support.prod.totpSecret", err)
	}
}

func TestConnectDoesNotRetryAFailedVerify(t *testing.T) {
	be := setup(t, nil)
	mfaBE(be, "verify", func(w http.ResponseWriter) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"code":"invalid-otp"}}`))
	})
	_, err := Connect(t.Context(), config.Prod, "ossm")
	if err == nil || !strings.Contains(err.Error(), "401 (invalid-otp)") {
		t.Fatalf("err = %v", err)
	}
	for _, leak := range []string{"mfa-secret-token", testTOTPSecret} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q", leak)
		}
	}
	if !slices.Equal(be.paths(), []string{loginPath, verifyPath}) {
		t.Errorf("paths = %v, want exactly one login and one verify", be.paths())
	}
}
