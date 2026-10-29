package doctor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/apex"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

var folders = map[string]string{
	"Authorization":      "11111111-1111-1111-1111-111111111111",
	"Authorization Test": "22222222-2222-2222-2222-222222222222",
	"Eligibility Check":  "33333333-3333-3333-3333-333333333333",
}

// apexStub is an Apex server answering login and the folder listing; every request is counted.
type apexStub struct {
	*httptest.Server
	calls  atomic.Int32
	logins atomic.Int32
	reject atomic.Bool
}

func newApexStub(t *testing.T) *apexStub {
	t.Helper()
	s := &apexStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/login"):
			s.logins.Add(1)
			if s.reject.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"detail":"Invalid credentials"}`))
				return
			}
			seg := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
			jwt := seg(map[string]any{"alg": "none"}) + "." + seg(map[string]any{"sub": "u", "exp": time.Now().Add(24 * time.Hour).Unix()}) + ".sig"
			http.SetCookie(w, &http.Cookie{Name: "access_token", Value: jwt, HttpOnly: true})
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/folders"):
			items := []map[string]any{}
			for _, n := range []string{"Authorization", "Authorization Test", "Eligibility Check"} {
				items = append(items, map[string]any{"id": folders[n], "name": n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "total": 3, "page": 1, "page_size": 200, "total_pages": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// useConfig writes the config (apex nil = no apex section) and clears the Apex env fallbacks.
func useConfig(t *testing.T, apexSection map[string]any) {
	t.Helper()
	creds := func(n string) map[string]any {
		return map[string]any{"be": "https://be." + n + ".example.com", "email": n + "@x.com", "password": "pw"}
	}
	cfg := map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": creds("prod"), "pre_prod": creds("preprod")},
		"uipath":  map[string]any{"orchestratorUrl": "https://cloud.uipath.com/o/t/orchestrator_", "bearer": "b"},
	}
	if apexSection != nil {
		cfg["apex"] = apexSection
	}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", path)
	for _, k := range []string{"APEX_API_URL", "APEX_USER_NAME", "APEX_PASSWORD"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	config.ResetForTest()
	apex.ResetClient()
	t.Cleanup(func() { config.ResetForTest(); apex.ResetClient() })
}

func runApexProbes(t *testing.T) []Check {
	t.Helper()
	probes, err := ApexProbes()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]Check, len(probes))
	for i, p := range probes {
		out[i] = p(t.Context())
	}
	return out
}

func byName(checks []Check, name string) Check {
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

func names(checks []Check) string {
	parts := make([]string, len(checks))
	for i, c := range checks {
		parts[i] = fmt.Sprintf("%s:%t", c.Name, c.OK)
	}
	return strings.Join(parts, ",")
}

func apexSection(url string) map[string]any {
	return map[string]any{"apiUrl": url, "username": "tester", "password": "pw"}
}

func TestApexProbesReportSourceLoginAndEachEnvsFolder(t *testing.T) {
	stub := newApexStub(t)
	useConfig(t, apexSection(stub.URL))
	checks := runApexProbes(t)

	if got, want := names(checks), "apex config:true,apex login:true,apex prod folder:true,apex pre_prod folder:true"; got != want {
		t.Fatalf("checks = %s, want %s", got, want)
	}
	if got, want := byName(checks, "apex config").Detail, "apiUrl from config, username from config, password from config"; got != want {
		t.Errorf("config detail = %q, want %q", got, want)
	}
	if got, want := byName(checks, "apex prod folder").Detail, "folder 'Authorization' -> "+folders["Authorization"]; got != want {
		t.Errorf("prod folder detail = %q, want %q", got, want)
	}
	if got, want := byName(checks, "apex pre_prod folder").Detail, "folder 'Authorization Test' -> "+folders["Authorization Test"]; got != want {
		t.Errorf("pre_prod folder detail = %q, want %q", got, want)
	}
	if n := stub.logins.Load(); n != 1 {
		t.Errorf("logins = %d, want 1 (checks share one session)", n)
	}
	if strings.Contains(byName(checks, "apex login").Detail, "tester") {
		t.Error("login detail leaks the username")
	}
}

func TestApexProbesARejectedLoginFailsTheLoginAndFolderChecksWithoutErroring(t *testing.T) {
	stub := newApexStub(t)
	stub.reject.Store(true)
	useConfig(t, apexSection(stub.URL))
	checks := runApexProbes(t)

	if !byName(checks, "apex config").OK {
		t.Error("config check failed, want ok")
	}
	if l := byName(checks, "apex login"); l.OK || !strings.Contains(l.Detail, "Invalid credentials") {
		t.Errorf("login check = %+v, want failure naming Invalid credentials", l)
	}
	if byName(checks, "apex prod folder").OK {
		t.Error("prod folder check ok, want failure without a login")
	}
}

func TestApexProbesAMissingFolderFailsOnlyThatEnvNamingTheFoldersThatExist(t *testing.T) {
	stub := newApexStub(t)
	sec := apexSection(stub.URL)
	sec["folderByEnv"] = map[string]any{"pre_prod": "Missing Folder"}
	useConfig(t, sec)
	checks := runApexProbes(t)

	if !byName(checks, "apex prod folder").OK {
		t.Error("prod folder check failed, want ok")
	}
	bad := byName(checks, "apex pre_prod folder")
	if bad.OK || !strings.Contains(bad.Detail, "'Missing Folder' not found") || !strings.Contains(bad.Detail, "Authorization, Authorization Test, Eligibility Check") {
		t.Errorf("pre_prod folder check = %+v, want a not-found naming the existing folders", bad)
	}
}

func TestApexProbesNameWhichFieldsCameFromTheEnvironment(t *testing.T) {
	stub := newApexStub(t)
	useConfig(t, map[string]any{"username": "file-user"})
	t.Setenv("APEX_API_URL", stub.URL)
	t.Setenv("APEX_PASSWORD", "pw")
	config.ResetForTest()
	checks := runApexProbes(t)

	if got, want := byName(checks, "apex config").Detail, "apiUrl from env, username from config, password from env"; got != want {
		t.Errorf("config detail = %q, want %q", got, want)
	}
	if !byName(checks, "apex login").OK {
		t.Errorf("login check = %+v, want ok", byName(checks, "apex login"))
	}
}

func TestApexProbesWhollyUnconfiguredIsOnePassingOptionalCheckAndNoRequest(t *testing.T) {
	useConfig(t, nil)
	checks := runApexProbes(t)
	if len(checks) != 1 || checks[0].Name != "apex config" || checks[0].Target != "apex" || !checks[0].OK || !strings.Contains(checks[0].Detail, "not configured (optional)") {
		t.Errorf("checks = %+v, want one passing 'not configured (optional)' check", checks)
	}
}

func TestApexProbesHalfConfiguredFailsNamingWhatIsMissing(t *testing.T) {
	useConfig(t, map[string]any{"username": "only-a-user"})
	checks := runApexProbes(t)
	if len(checks) != 1 || checks[0].OK || !strings.Contains(checks[0].Detail, "missing apiUrl, password") {
		t.Errorf("checks = %+v, want one failing check naming apiUrl, password", checks)
	}
}

func TestRunProbesEveryEnvInFixedOrderAndOneFailureNeverAbortsTheOthers(t *testing.T) {
	// A cancelled context fails every network probe without touching the network, while the
	// (unconfigured) Apex check needs none and still passes.
	useConfig(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var steps int
	rep, err := Run(ctx, "", func(done, total int, label string) { steps++ })
	if err != nil {
		t.Fatal(err)
	}
	want := "copilot prod auth,copilot pre_prod auth,copilot prod clinic scope,copilot pre_prod clinic scope,uipath prod folder,uipath pre_prod folder,apex config"
	var got []string
	for _, c := range rep.Checks {
		got = append(got, c.Name)
	}
	if strings.Join(got, ",") != want {
		t.Errorf("checks = %v, want %s", got, want)
	}
	if rep.OK || rep.Account != "(default)" || steps != len(rep.Checks) {
		t.Errorf("report ok=%t account=%q steps=%d, want a failing report for the default account with one step per check", rep.OK, rep.Account, steps)
	}
	if !rep.Checks[len(rep.Checks)-1].OK {
		t.Error("apex check failed, want the optional-unconfigured pass")
	}
}

func TestClinicScopeCountsVisibleLocationsAndFailsOnBEError(t *testing.T) {
	status := http.StatusOK
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/orders/locations") {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"locations":["a","b"]}`))
			return
		}
		claims, _ := json.Marshal(map[string]any{"id": 1, "timestamp": time.Now().Add(time.Hour).UnixMilli()})
		fmt.Fprintf(w, `{"token":"h.%s.s"}`, base64.StdEncoding.EncodeToString(claims))
	}))
	t.Cleanup(be.Close)
	creds := map[string]any{"be": be.URL, "email": "e@x.com", "password": "pw"}
	cfg, _ := json.Marshal(map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": creds, "pre_prod": creds},
		"uipath":  map[string]any{"orchestratorUrl": "https://cloud.uipath.com/o/t/orchestrator_", "bearer": "b"},
	})
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", path)
	config.ResetForTest()
	copilot.ResetSessionCacheForTest()
	t.Cleanup(func() { config.ResetForTest(); copilot.ResetSessionCacheForTest() })

	if got, err := clinicScope(t.Context(), config.Prod, ""); err != nil || got != "2 locations visible" {
		t.Errorf("clinicScope = %q, %v; want \"2 locations visible\"", got, err)
	}
	status = http.StatusForbidden
	if _, err := clinicScope(t.Context(), config.Prod, ""); err == nil || !strings.Contains(err.Error(), "GET /orders/locations failed 403") {
		t.Errorf("clinicScope error = %v, want the 403 reported", err)
	}
}
