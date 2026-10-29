package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func envCreds(name string) map[string]any {
	return map[string]any{"be": "https://be." + name + ".example.com", "email": name + "@example.com", "password": "pw"}
}

func fixture() map[string]any {
	return map[string]any{
		"copilot": map[string]any{
			"sessionCache": false,
			"prod":         envCreds("prod"),
			"pre_prod":     envCreds("preprod"),
			"support":      map[string]any{"prod": envCreds("support-prod")},
			"profiles": map[string]any{
				"ossm": map[string]any{"prod": envCreds("ossm-prod"), "pre_prod": envCreds("ossm-preprod")},
				// prod migrated to support-account auth; pre_prod still a direct login.
				"kafri": map[string]any{
					"prod":     map[string]any{"clinicUid": "b58a95cf-e53d-4eae-bf86-9a57ad624022"},
					"pre_prod": envCreds("kafri-preprod"),
				},
				// carries BOTH — clinicUid must win (mid-migration shape).
				"both": map[string]any{"prod": map[string]any{
					"be": "https://be.both-prod.example.com", "email": "both-prod@example.com", "password": "pw",
					"clinicUid": "c741d791-3e3b-4102-b95c-f94549949c9a",
				}},
				"prodonly": map[string]any{"prod": envCreds("prodonly")},
			},
		},
		"uipath": map[string]any{
			"orchestratorUrl": "https://cloud.uipath.com/myorg/mytenant/orchestrator_",
			"bearer":          "test-bearer",
		},
	}
}

// withConfig writes v as the config file, points COPILOT_MCP_CONFIG at it and resets the
// cache. Returns the file path.
func withConfig(t *testing.T, v any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	writeJSON(t, path, v)
	t.Setenv("COPILOT_MCP_CONFIG", path)
	ResetForTest()
	t.Cleanup(ResetForTest)
	return path
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// touch forces a detectable mtime change regardless of filesystem timestamp resolution.
func touch(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

func mustAuth(t *testing.T, profile string, env Env) EnvAuth {
	t.Helper()
	a, err := ResolveAuth(profile, env)
	if err != nil {
		t.Fatalf("ResolveAuth(%q, %s): %v", profile, env, err)
	}
	return a
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %v, want containing %q", err, substr)
	}
}

func TestResolveAuthFallsBackToTopLevelPairWithoutProfile(t *testing.T) {
	withConfig(t, fixture())
	want := EnvAuth{Mode: ModeDirect, BE: "https://be.prod.example.com", Email: "prod@example.com", Password: "pw"}
	if got := mustAuth(t, "", Prod); got != want {
		t.Errorf("prod = %+v, want %+v", got, want)
	}
	if got := mustAuth(t, "", PreProd).Email; got != "preprod@example.com" {
		t.Errorf("pre_prod email = %q", got)
	}
}

func TestResolveAuthDirectProfileStaysDirectInBothEnvs(t *testing.T) {
	withConfig(t, fixture())
	want := EnvAuth{Mode: ModeDirect, BE: "https://be.ossm-prod.example.com", Email: "ossm-prod@example.com", Password: "pw"}
	if got := mustAuth(t, "ossm", Prod); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := mustAuth(t, "ossm", PreProd).Mode; got != ModeDirect {
		t.Errorf("pre_prod mode = %s", got)
	}
}

func TestResolveAuthClinicUIDUsesSupportAccount(t *testing.T) {
	withConfig(t, fixture())
	want := EnvAuth{Mode: ModeSupport, BE: "https://be.support-prod.example.com", Email: "support-prod@example.com", Password: "pw", ClinicUID: "b58a95cf-e53d-4eae-bf86-9a57ad624022"}
	if got := mustAuth(t, "kafri", Prod); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestResolveAuthNonMigratedEnvStillLogsInDirectly(t *testing.T) {
	withConfig(t, fixture())
	want := EnvAuth{Mode: ModeDirect, BE: "https://be.kafri-preprod.example.com", Email: "kafri-preprod@example.com", Password: "pw"}
	if got := mustAuth(t, "kafri", PreProd); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestResolveAuthClinicUIDWinsOverCredentialsOnSameEntry(t *testing.T) {
	withConfig(t, fixture())
	got := mustAuth(t, "both", Prod)
	if got.Mode != ModeSupport || got.Email != "support-prod@example.com" {
		t.Errorf("got %+v, want support mode with the support account", got)
	}
	// be may be overridden per profile; the account is the support account's.
	if got.BE != "https://be.both-prod.example.com" {
		t.Errorf("be = %q", got.BE)
	}
}

func TestResolveSupportErrorsWhenEnvHasNoSupportAccount(t *testing.T) {
	withConfig(t, fixture())
	if _, err := ResolveAuth("kafri", Prod); err != nil {
		t.Errorf("kafri prod: %v", err)
	}
	_, err := ResolveSupport(PreProd)
	wantErr(t, err, "copilot.support.pre_prod is required")
}

func TestResolveAuthUnknownProfileAndMissingEnv(t *testing.T) {
	withConfig(t, fixture())
	_, err := ResolveAuth("nope", Prod)
	wantErr(t, err, "unknown profile 'nope'")
	_, err = ResolveAuth("prodonly", PreProd)
	wantErr(t, err, "has no pre_prod auth")
}

func TestLoadRejectsEnvEntryWithNeitherClinicUIDNorFullCredentials(t *testing.T) {
	f := fixture()
	profiles := f["copilot"].(map[string]any)["profiles"].(map[string]any)
	profiles["broken"] = map[string]any{"prod": map[string]any{"be": "https://x.example.com"}}
	withConfig(t, f)
	_, err := ResolveAuth("broken", Prod)
	wantErr(t, err, "copilot.profiles.broken.prod: needs either clinicUid")
}

func TestResolveSupportReturnsConfiguredAccount(t *testing.T) {
	withConfig(t, fixture())
	got, err := ResolveSupport(Prod)
	if err != nil || got.Email != "support-prod@example.com" {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestSessionCacheEnabledHonoursExplicitFalse(t *testing.T) {
	f := fixture()
	withConfig(t, f)
	if on, err := SessionCacheEnabled(); err != nil || on {
		t.Errorf("explicit false: on=%v err=%v", on, err)
	}
	delete(f["copilot"].(map[string]any), "sessionCache")
	withConfig(t, f)
	if on, err := SessionCacheEnabled(); err != nil || !on {
		t.Errorf("default: on=%v err=%v", on, err)
	}
}

func TestListProfilesIsEmptyWhenConfigIsAbsent(t *testing.T) {
	withConfig(t, fixture())
	if got := ListProfiles(); len(got) != 4 || got[0] != "both" {
		t.Errorf("profiles = %v", got)
	}
	t.Setenv("COPILOT_MCP_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("COPILOT_MCP_LOCAL_DIR", t.TempDir())
	ResetForTest()
	if got := ListProfiles(); got != nil {
		t.Errorf("profiles = %v, want nil", got)
	}
	if st := Status(); st.OK || !strings.Contains(st.Error, "No config found") {
		t.Errorf("status = %+v", st)
	}
}

func TestUipathAuthSchema(t *testing.T) {
	url := fixture()["uipath"].(map[string]any)["orchestratorUrl"]
	oauth := map[string]any{"clientId": "id", "clientSecret": "secret"}
	cases := []struct {
		name    string
		uipath  map[string]any
		wantErr string
		bearer  string
		oauthID string
	}{
		{"accepts bearer only", map[string]any{"orchestratorUrl": url, "bearer": "b"}, "", "b", ""},
		{"accepts oauth only, no bearer", map[string]any{"orchestratorUrl": url, "oauth": oauth}, "", "", "id"},
		{"accepts both", map[string]any{"orchestratorUrl": url, "bearer": "b", "oauth": oauth}, "", "b", "id"},
		{"rejects neither", map[string]any{"orchestratorUrl": url}, "bearer, oauth, or both", "", ""},
		{"rejects foreign orchestrator url", map[string]any{"orchestratorUrl": "https://example.com/x", "bearer": "b"}, "orchestratorUrl must look like", "", ""},
		{"rejects missing url", map[string]any{"bearer": "b"}, "uipath.orchestratorUrl: orchestratorUrl is required", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture()
			f["uipath"] = tc.uipath
			withConfig(t, f)
			got, err := Uipath()
			if tc.wantErr != "" {
				wantErr(t, err, tc.wantErr)
				return
			}
			if err != nil || got.Bearer != tc.bearer {
				t.Fatalf("bearer = %q, err = %v", got.Bearer, err)
			}
			if (tc.oauthID == "") != (got.OAuth == nil) || (got.OAuth != nil && got.OAuth.ClientID != tc.oauthID) {
				t.Errorf("oauth = %+v, want id %q", got.OAuth, tc.oauthID)
			}
		})
	}
}

func TestUipathReturnsValidatedBlock(t *testing.T) {
	withConfig(t, fixture())
	got, err := Uipath()
	if err != nil || got.Bearer != "test-bearer" || !strings.Contains(got.OrchestratorURL, "cloud.uipath.com") {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestFeedbackDefaultsToEnabled(t *testing.T) {
	f := fixture()
	withConfig(t, f)
	if got, err := Feedback(); err != nil || got != (FeedbackConfig{Enabled: true}) {
		t.Errorf("default = %+v, %v", got, err)
	}
	f["feedback"] = map[string]any{"enabled": false, "repositoryUrl": "https://github.com/o/r"}
	withConfig(t, f)
	if got, err := Feedback(); err != nil || got != (FeedbackConfig{RepositoryURL: "https://github.com/o/r"}) {
		t.Errorf("configured = %+v, %v", got, err)
	}
}

func TestLegacySplitFilesAssembleTheSingleFileShape(t *testing.T) {
	dir := t.TempDir()
	f := fixture()
	writeJSON(t, filepath.Join(dir, "order-copy-credentials.json"), f["copilot"])
	writeJSON(t, filepath.Join(dir, "uipath-config.json"), f["uipath"])
	t.Setenv("COPILOT_MCP_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("COPILOT_MCP_LOCAL_DIR", dir)
	ResetForTest()
	t.Cleanup(ResetForTest)
	if got := mustAuth(t, "", Prod).Email; got != "prod@example.com" {
		t.Errorf("email = %q", got)
	}
}

// ---- Apex ----

func withApex(t *testing.T, apex map[string]any) {
	t.Helper()
	for _, k := range []string{"APEX_API_URL", "APEX_USER_NAME", "APEX_PASSWORD", "APEX_URL", "APEX_API_URL_1"} {
		t.Setenv(k, "")
	}
	f := fixture()
	if apex != nil {
		f["apex"] = apex
	}
	withConfig(t, f)
}

func mustApex(t *testing.T) ApexConfig {
	t.Helper()
	a, err := ResolveApex()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestConfigWithoutApexSectionStillLoads(t *testing.T) {
	withApex(t, nil)
	cfg, err := Load()
	if err != nil || cfg.Apex != nil || cfg.Uipath.Bearer != "test-bearer" {
		t.Errorf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestInvalidApexURLFailsValidationNamingTheField(t *testing.T) {
	withApex(t, map[string]any{"apiUrl": "not a url"})
	_, err := Load()
	wantErr(t, err, "apex.apiUrl must be a valid URL")
}

func TestApexFileWinsPerFieldAndEnvFillsTheRest(t *testing.T) {
	withApex(t, map[string]any{"apiUrl": "https://file.example.com", "username": "file-user"})
	t.Setenv("APEX_API_URL", "https://env.example.com")
	t.Setenv("APEX_USER_NAME", "env-user")
	t.Setenv("APEX_PASSWORD", "env-pw")
	a := mustApex(t)
	if a.APIURL != "https://file.example.com" || a.Username != "file-user" || a.Password != "env-pw" {
		t.Errorf("resolved = %+v", a)
	}
	if want := (ApexSources{SourceConfig, SourceConfig, SourceEnv}); a.Sources != want {
		t.Errorf("sources = %+v, want %+v", a.Sources, want)
	}
}

func TestApexEnvAloneSufficesAndTrailingSlashesAreStripped(t *testing.T) {
	withApex(t, nil)
	t.Setenv("APEX_API_URL", "https://env.example.com//")
	t.Setenv("APEX_USER_NAME", "env-user")
	t.Setenv("APEX_PASSWORD", "env-pw")
	a := mustApex(t)
	if a.APIURL != "https://env.example.com" {
		t.Errorf("apiUrl = %q", a.APIURL)
	}
	if want := (ApexSources{SourceEnv, SourceEnv, SourceEnv}); a.Sources != want {
		t.Errorf("sources = %+v", a.Sources)
	}
}

func TestApexUnconfiguredErrorNamesExactlyTheMissingFields(t *testing.T) {
	withApex(t, map[string]any{"apiUrl": "https://file.example.com"})
	t.Setenv("APEX_PASSWORD", "env-pw")
	_, err := ResolveApex()
	nc, ok := errors.AsType[*ApexNotConfiguredError](err)
	if !ok || !shared.IsExpected(err) {
		t.Fatalf("err = %v, want expected *ApexNotConfiguredError", err)
	}
	msg := nc.Error()
	for _, want := range []string{"missing username", "apex.username", "APEX_USER_NAME"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	for _, bad := range []string{"apiUrl", "password"} {
		if strings.Contains(msg, bad) {
			t.Errorf("message %q must not mention %q", msg, bad)
		}
	}
	if len(nc.Missing) != 1 || nc.Missing[0] != "username" || !nc.Partial {
		t.Errorf("missing = %v, partial = %v", nc.Missing, nc.Partial)
	}
}

func TestApexNothingConfiguredListsAllFieldsAndBothWaysToSupplyThem(t *testing.T) {
	withApex(t, nil)
	_, err := ResolveApex()
	nc, ok := errors.AsType[*ApexNotConfiguredError](err)
	if !ok || nc.Partial {
		t.Fatalf("err = %v", err)
	}
	wantErr(t, err, "missing apiUrl, username, password. Set apex.apiUrl, apex.username, apex.password in the config file, or the environment variables APEX_API_URL, APEX_USER_NAME, APEX_PASSWORD")
}

func TestApexEmptyEnvVarCountsAsUnsetAndNumberedNamesAreNeverRead(t *testing.T) {
	withApex(t, map[string]any{"username": "u", "password": "p"})
	t.Setenv("APEX_API_URL", "")
	t.Setenv("APEX_API_URL_1", "https://numbered.example.com")
	t.Setenv("APEX_URL", "https://legacy.example.com")
	_, err := ResolveApex()
	wantErr(t, err, "missing apiUrl")
}

func TestApexEnvIsReadOnEveryCall(t *testing.T) {
	withApex(t, map[string]any{"username": "u", "password": "p"})
	t.Setenv("APEX_API_URL", "https://one.example.com")
	if got := mustApex(t).APIURL; got != "https://one.example.com" {
		t.Errorf("first = %q", got)
	}
	t.Setenv("APEX_API_URL", "https://two.example.com")
	if got := mustApex(t).APIURL; got != "https://two.example.com" {
		t.Errorf("second = %q", got)
	}
}

func TestApexFoldersDefaultPerEnvAndPartialOverrideKeepsOtherDefault(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"apiUrl": "https://a.example.com", "username": "u", "password": "p"}
	}
	withApex(t, base())
	got := mustApex(t).FolderByEnv
	if got[Prod] != "Authorization" || got[PreProd] != "Authorization Test" {
		t.Errorf("defaults = %v", got)
	}
	b := base()
	b["folderByEnv"] = map[string]any{"pre_prod": "Sandbox"}
	withApex(t, b)
	got = mustApex(t).FolderByEnv
	if got[Prod] != "Authorization" || got[PreProd] != "Sandbox" {
		t.Errorf("override = %v", got)
	}
}

func TestApexWriteGuardSettingsPassThroughAndUnsetStayAbsent(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"apiUrl": "https://a.example.com", "username": "u", "password": "p"}
	}
	withApex(t, base())
	if a := mustApex(t); len(a.ServerURLByEnv) != 0 || a.QueueURL != "" {
		t.Errorf("unset = %+v", a)
	}
	b := base()
	b["serverUrlByEnv"] = map[string]any{"pre_prod": "https://be.pre.example.com"}
	b["queueUrl"] = "https://sqs.example.com/q"
	withApex(t, b)
	a := mustApex(t)
	if len(a.ServerURLByEnv) != 1 || a.ServerURLByEnv[PreProd] != "https://be.pre.example.com" || a.QueueURL != "https://sqs.example.com/q" {
		t.Errorf("set = %+v", a)
	}
}

// ---- Live reload ----

func TestLoadPicksUpEditedFileOnNextCall(t *testing.T) {
	path := withConfig(t, fixture())
	if got := mustAuth(t, "", Prod).Email; got != "prod@example.com" {
		t.Fatalf("before = %q", got)
	}
	f := fixture()
	f["copilot"].(map[string]any)["prod"] = envCreds("prod-v2")
	writeJSON(t, path, f)
	touch(t, path)
	if got := mustAuth(t, "", Prod).Email; got != "prod-v2@example.com" {
		t.Errorf("after = %q", got)
	}
}

func TestOnReloadFiresOnReloadButNotOnFirstLoad(t *testing.T) {
	path := withConfig(t, fixture())
	var events []ReloadInfo
	unsubscribe := OnReload(func(i ReloadInfo) { events = append(events, i) })
	defer unsubscribe()
	mustAuth(t, "", Prod)
	if len(events) != 0 {
		t.Fatalf("events after first load = %v", events)
	}
	writeJSON(t, path, fixture())
	touch(t, path)
	mustAuth(t, "", Prod)
	if len(events) != 1 || events[0].Source != path {
		t.Errorf("events = %v, want one for %s", events, path)
	}
	unsubscribe()
	touch(t, path) // no-op mtime bump again below
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	mustAuth(t, "", Prod)
	if len(events) != 1 {
		t.Errorf("unsubscribed listener still fired: %v", events)
	}
}

func TestOnReloadPanickingListenerNeverBreaksLoading(t *testing.T) {
	path := withConfig(t, fixture())
	defer OnReload(func(ReloadInfo) { panic("bad listener") })()
	mustAuth(t, "", Prod)
	touch(t, path)
	mustAuth(t, "", Prod)
}

func TestLoadFailsClosedOnInvalidEditThenRecovers(t *testing.T) {
	path := withConfig(t, fixture())
	mustAuth(t, "", Prod)
	if err := os.WriteFile(path, []byte("{ not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, path)
	_, err := ResolveAuth("", Prod)
	wantErr(t, err, "not valid JSON")
	writeJSON(t, path, fixture())
	future := time.Now().Add(4 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if got := mustAuth(t, "", Prod).Email; got != "prod@example.com" {
		t.Errorf("after fix = %q", got)
	}
}

func TestLoadIsSafeUnderConcurrentCalls(t *testing.T) {
	path := withConfig(t, fixture())
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 50 {
				_, _ = ResolveAuth("ossm", Prod)
			}
		}()
	}
	for range 5 {
		touch(t, path)
	}
	for range 8 {
		<-done
	}
}

// ---- Default filename ----

func TestDefaultFilenamePrefersNewNameThenFallsBackToLegacy(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"new", "copilot-mcp.config.json"},
		{"old", "config.local.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("COPILOT_MCP_CONFIG", "")
			t.Chdir(dir)
			writeJSON(t, filepath.Join(dir, tc.file), fixture())
			ResetForTest()
			t.Cleanup(ResetForTest)
			if got := mustAuth(t, "", Prod).Email; got != "prod@example.com" {
				t.Errorf("email = %q", got)
			}
		})
	}
}
