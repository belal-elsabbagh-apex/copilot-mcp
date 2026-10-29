package copilot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
)

const (
	clinicUID  = "b58a95cf-e53d-4eae-bf86-9a57ad624022"
	clinicUID2 = "c741d791-3e3b-4102-b95c-f94549949c9a"
	day        = 24 * time.Hour
)

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.StdEncoding.EncodeToString(b) + ".s"
}

func liveToken(clinic ...int) string {
	c := map[string]any{"id": 335, "timestamp": time.Now().Add(day).UnixMilli()}
	if len(clinic) > 0 {
		c["activeClinicId"] = clinic[0]
	}
	return jwt(c)
}

// beServer is a scripted BE recording every request path + body.
type beServer struct {
	*httptest.Server
	mu      sync.Mutex
	reqs    []string
	bodies  []map[string]any
	handler func(w http.ResponseWriter, path string)
}

func defaultHandler(w http.ResponseWriter, path string) {
	switch {
	case strings.Contains(path, "physician/login"):
		fmt.Fprintf(w, `{"token":%q}`, liveToken())
	case strings.Contains(path, "support/switch-clinic"):
		fmt.Fprintf(w, `{"token":%q}`, liveToken(25))
	default:
		w.Write([]byte("{}"))
	}
}

func (b *beServer) paths() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.reqs)
}

func (b *beServer) reset() {
	b.mu.Lock()
	b.reqs, b.bodies = nil, nil
	b.mu.Unlock()
}

// setup writes the config + cache fixture (a prod support profile pair and a direct pre_prod one)
// and starts a scripted BE every URL in it points at.
func setup(t *testing.T, extraCopilot map[string]any) *beServer {
	t.Helper()
	b := &beServer{handler: defaultHandler}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.mu.Lock()
		b.reqs = append(b.reqs, r.URL.Path)
		b.bodies = append(b.bodies, body)
		h := b.handler
		b.mu.Unlock()
		h(w, r.URL.Path)
	}))
	t.Cleanup(b.Close)
	creds := func(email string) map[string]any {
		return map[string]any{"be": b.URL, "email": email, "password": "pw"}
	}
	cp := map[string]any{
		"support": map[string]any{"prod": creds("support@example.com")},
		"profiles": map[string]any{
			"ossm":  map[string]any{"prod": map[string]any{"clinicUid": clinicUID}, "pre_prod": creds("ossm-preprod@example.com")},
			"kafri": map[string]any{"prod": map[string]any{"clinicUid": clinicUID2}},
		},
	}
	for k, v := range extraCopilot {
		cp[k] = v
	}
	cfg, _ := json.Marshal(map[string]any{"copilot": cp, "uipath": map[string]any{"orchestratorUrl": "https://cloud.uipath.com/o/t/orchestrator_", "bearer": "x"}})
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", cfgPath)
	t.Setenv("COPILOT_MCP_CACHE", filepath.Join(dir, "cache", "sessions.json"))
	config.ResetForTest()
	ResetSessionCacheForTest()
	t.Cleanup(func() { config.ResetForTest(); ResetSessionCacheForTest() })
	return b
}

const (
	loginPath  = "/api/v1/copilot/physician/login"
	switchPath = "/api/v1/support/switch-clinic"
)

func TestTokenClaimsReadWellFormedTokens(t *testing.T) {
	tok := jwt(map[string]any{"id": 335, "timestamp": 1_770_000_000_000, "activeClinicId": 25})
	if ms, ok := TokenExpiry(tok); !ok || ms != 1_770_000_000_000 {
		t.Errorf("TokenExpiry = %v, %v", ms, ok)
	}
	if id := TokenClinicID(tok); id == nil || *id != 25 {
		t.Errorf("TokenClinicID = %v", id)
	}
}

func TestTokenClaimsAreAbsentForGarbageAndNonNumbers(t *testing.T) {
	for _, tok := range []string{"not-a-jwt", jwt(map[string]any{"id": 335}), jwt(map[string]any{"timestamp": "soon", "activeClinicId": "25"})} {
		if _, ok := TokenExpiry(tok); ok {
			t.Errorf("TokenExpiry(%q) ok, want absent", tok)
		}
		if TokenClinicID(tok) != nil {
			t.Errorf("TokenClinicID(%q) set, want nil", tok)
		}
	}
	// a clinic-less login token has an expiry but no activeClinicId
	clinicless := liveToken()
	if _, ok := TokenExpiry(clinicless); !ok || TokenClinicID(clinicless) != nil {
		t.Error("clinic-less token: want expiry without a clinic id")
	}
}

func TestTokenClaimsDecodeUnpaddedURLSafeSegments(t *testing.T) {
	seg := base64.RawURLEncoding.EncodeToString([]byte(`{"timestamp":5,"activeClinicId":7}`))
	if ms, ok := TokenExpiry("h." + seg + ".s"); !ok || ms != 5 {
		t.Errorf("TokenExpiry = %v, %v (Node's base64 decoding accepts url-safe, unpadded)", ms, ok)
	}
}

// Golden values produced by the TS sessionKey — a user sharing ~/.cache/copilot-mcp/sessions.json
// between the TS and Go servers only reuses sessions if the hashes agree byte for byte.
func TestSessionKeyMatchesTheTSHash(t *testing.T) {
	for _, c := range []struct {
		env         config.Env
		scope, want string
	}{
		{config.Prod, "support", "61bf3748a6ec1f51"},
		{config.PreProd, "direct", "da094ce524591462"},
		{config.Prod, "clinic:" + clinicUID, "0d7cece4f2444e15"},
	} {
		if got := SessionKey(c.env, "https://be.example.com", "s@example.com", c.scope); got != c.want {
			t.Errorf("SessionKey(%s, %s) = %s, want %s", c.env, c.scope, got, c.want)
		}
	}
}

func TestConnectSupportLogsInThenSwitchesClinic(t *testing.T) {
	be := setup(t, nil)
	s, err := Connect(t.Context(), config.Prod, "ossm")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.paths(), []string{loginPath, switchPath}) {
		t.Errorf("paths = %v, want login then switch-clinic", be.paths())
	}
	if be.bodies[0]["email"] != "support@example.com" || be.bodies[1]["clinicUid"] != clinicUID {
		t.Errorf("bodies = %v", be.bodies)
	}
	if s.Mode != config.ModeSupport || s.ClinicUID != clinicUID || s.ActiveClinicID == nil || *s.ActiveClinicID != 25 || s.Client.Env() != config.Prod || s.Profile != "ossm" {
		t.Errorf("session = %+v", s)
	}
}

func TestConnectRefusesAClinicLessSwitchTokenAndCachesNothingForTheClinic(t *testing.T) {
	be := setup(t, nil)
	be.handler = func(w http.ResponseWriter, _ string) { fmt.Fprintf(w, `{"token":%q}`, liveToken()) }
	if _, err := Connect(t.Context(), config.Prod, "ossm"); err == nil || !strings.Contains(err.Error(), "clinic-less") {
		t.Fatalf("err = %v, want clinic-less refusal", err)
	}
	be.reset()
	be.handler = defaultHandler
	s, err := Connect(t.Context(), config.Prod, "ossm")
	if err != nil || *s.ActiveClinicID != 25 {
		t.Fatalf("s = %+v, err = %v", s, err)
	}
	if !slices.Equal(be.paths(), []string{switchPath}) {
		t.Errorf("paths = %v, want only switch-clinic (login was cached, clinic was not)", be.paths())
	}
}

func TestConnectSurfacesRateLimitedLoginWithReset(t *testing.T) {
	be := setup(t, nil)
	be.handler = func(w http.ResponseWriter, _ string) {
		w.Header().Set("RateLimit-Reset", "412")
		w.WriteHeader(429)
	}
	_, err := Connect(t.Context(), config.Prod, "ossm")
	if err == nil || !strings.Contains(err.Error(), "login failed 429: support login rate-limited (10 logins per 15 minutes) — retry after 412s") {
		t.Errorf("err = %v", err)
	}
}

func TestConnectDirectIssuesOneLoginWithTheProfilesOwnCredentials(t *testing.T) {
	be := setup(t, nil)
	s, err := Connect(t.Context(), config.PreProd, "ossm")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.paths(), []string{loginPath}) || be.bodies[0]["email"] != "ossm-preprod@example.com" {
		t.Errorf("paths = %v, bodies = %v", be.paths(), be.bodies)
	}
	if s.Mode != config.ModeDirect || s.ClinicUID != "" || s.Client.Env() != config.PreProd {
		t.Errorf("session = %+v", s)
	}
}

func TestRepeatedConnectInOneProcessIssuesNoRequests(t *testing.T) {
	be := setup(t, nil)
	mustConnect(t, config.Prod, "ossm")
	mustConnect(t, config.PreProd, "ossm")
	be.reset()
	if s := mustConnect(t, config.Prod, "ossm"); *s.ActiveClinicID != 25 {
		t.Errorf("session = %+v", s)
	}
	mustConnect(t, config.PreProd, "ossm")
	if len(be.paths()) != 0 {
		t.Errorf("paths = %v, want none", be.paths())
	}
}

func mustConnect(t *testing.T, env config.Env, profile string) Session {
	t.Helper()
	s, err := Connect(t.Context(), env, profile)
	if err != nil {
		t.Fatalf("Connect(%s, %s): %v", env, profile, err)
	}
	return s
}

func TestDiskCacheCarriesSessionsAcrossARestart(t *testing.T) {
	be := setup(t, nil)
	mustConnect(t, config.Prod, "ossm")
	mustConnect(t, config.PreProd, "ossm")
	ResetSessionCacheForTest() // a restart: memory gone, file kept
	be.reset()
	if s := mustConnect(t, config.Prod, "ossm"); *s.ActiveClinicID != 25 {
		t.Errorf("session = %+v", s)
	}
	if s := mustConnect(t, config.PreProd, "ossm"); s.Mode != config.ModeDirect {
		t.Errorf("session = %+v", s)
	}
	if len(be.paths()) != 0 {
		t.Errorf("paths = %v, want none", be.paths())
	}
}

func TestSecondSupportProfileReusesTheClinicLessSession(t *testing.T) {
	be := setup(t, nil)
	mustConnect(t, config.Prod, "ossm")
	ResetSessionCacheForTest()
	be.reset()
	be.handler = func(w http.ResponseWriter, _ string) { fmt.Fprintf(w, `{"token":%q}`, liveToken(6)) }
	s := mustConnect(t, config.Prod, "kafri")
	if !slices.Equal(be.paths(), []string{switchPath}) || be.bodies[0]["clinicUid"] != clinicUID2 || *s.ActiveClinicID != 6 {
		t.Errorf("paths = %v, session = %+v; want switch only", be.paths(), s)
	}
}

func TestAnExpiredCachedTokenReauthenticates(t *testing.T) {
	be := setup(t, nil)
	expired := jwt(map[string]any{"id": 335, "timestamp": time.Now().Add(-day).UnixMilli(), "activeClinicId": 25})
	be.handler = func(w http.ResponseWriter, _ string) { fmt.Fprintf(w, `{"token":%q}`, expired) }
	mustConnect(t, config.Prod, "ossm")
	ResetSessionCacheForTest()
	be.reset()
	be.handler = defaultHandler
	s := mustConnect(t, config.Prod, "ossm")
	if !slices.Equal(be.paths(), []string{loginPath, switchPath}) || *s.ActiveClinicID != 25 {
		t.Errorf("paths = %v", be.paths())
	}
}

func TestATokenExpiringInsideTheSkewIsNotServedFromCache(t *testing.T) {
	be := setup(t, nil)
	soon := jwt(map[string]any{"id": 335, "timestamp": time.Now().Add(5 * time.Minute).UnixMilli()})
	be.handler = func(w http.ResponseWriter, _ string) { fmt.Fprintf(w, `{"token":%q}`, soon) }
	mustConnect(t, config.PreProd, "ossm")
	be.reset()
	mustConnect(t, config.PreProd, "ossm")
	if len(be.paths()) != 1 {
		t.Errorf("paths = %v, want a fresh login (token is within the 10-minute skew)", be.paths())
	}
}

func TestConcurrentConnectsSingleFlightIntoOneLoginAndSwitch(t *testing.T) {
	be := setup(t, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := Connect(t.Context(), config.Prod, "ossm"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if !slices.Equal(be.paths(), []string{loginPath, switchPath}) {
		t.Errorf("paths = %v, want exactly one login + one switch", be.paths())
	}
}

// The TS only single-flighted the final clinic session: two profiles in one env connecting cold
// together each burned a support login against the 10-per-15-minutes budget.
func TestConcurrentColdConnectsForTwoClinicsShareOneSupportLogin(t *testing.T) {
	be := setup(t, nil)
	be.handler = func(w http.ResponseWriter, p string) {
		if strings.Contains(p, "physician/login") {
			time.Sleep(50 * time.Millisecond) // widen the race window
		}
		defaultHandler(w, p)
	}
	var wg sync.WaitGroup
	for _, p := range []string{"ossm", "kafri"} {
		wg.Go(func() { mustConnect(t, config.Prod, p) })
	}
	wg.Go(func() {
		if _, err := ListClinics(t.Context(), config.Prod); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	logins := 0
	for _, p := range be.paths() {
		if p == loginPath {
			logins++
		}
	}
	if logins != 1 {
		t.Errorf("support logins = %d, want 1 (paths %v)", logins, be.paths())
	}
}

func TestACancelledCallerDoesNotFailTheOthersSharingItsLogin(t *testing.T) {
	be := setup(t, nil)
	be.handler = func(w http.ResponseWriter, p string) {
		time.Sleep(50 * time.Millisecond)
		defaultHandler(w, p)
	}
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { _, err := Connect(ctx, config.Prod, "ossm"); errc <- err }()
	time.Sleep(10 * time.Millisecond)
	var other atomic.Value
	done := make(chan struct{})
	go func() {
		_, err := Connect(t.Context(), config.Prod, "ossm")
		other.Store(fmt.Sprint(err))
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-errc; err == nil {
		t.Error("the cancelled caller should return its ctx error")
	}
	<-done
	if got := other.Load(); got != "<nil>" {
		t.Errorf("other caller err = %v, want nil", got)
	}
}

func TestListClinicsParsesWithoutSwitching(t *testing.T) {
	be := setup(t, nil)
	be.handler = func(w http.ResponseWriter, p string) {
		if strings.Contains(p, "support/clinics") {
			fmt.Fprintf(w, `{"clinics":[{"clinicUid":%q,"name":"OSSM Clinic","lastActiveAt":"2026-09-07T00:00:00Z","ownerEmail":"owner@ossm.example.com"},{"clinicUid":%q,"name":"Hassan Kafri","lastActiveAt":null}]}`, clinicUID, clinicUID2)
			return
		}
		defaultHandler(w, p)
	}
	clinics, err := ListClinics(t.Context(), config.Prod)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.paths(), []string{loginPath, "/api/v1/support/clinics"}) {
		t.Errorf("paths = %v", be.paths())
	}
	b, _ := json.Marshal(clinics)
	want := fmt.Sprintf(`[{"clinicUid":%q,"name":"OSSM Clinic","lastActiveAt":"2026-09-07T00:00:00Z","ownerEmail":"owner@ossm.example.com"},{"clinicUid":%q,"name":"Hassan Kafri","lastActiveAt":null,"ownerEmail":""}]`, clinicUID, clinicUID2)
	if string(b) != want {
		t.Errorf("clinics = %s\nwant %s", b, want)
	}
}

func TestListClinicsNeedsASupportAccountForTheEnv(t *testing.T) {
	be := setup(t, nil)
	_, err := ListClinics(t.Context(), config.PreProd)
	if err == nil || !strings.Contains(err.Error(), "copilot.support.pre_prod is required") || len(be.paths()) != 0 {
		t.Errorf("err = %v, paths = %v", err, be.paths())
	}
}

// ---- disk cache: format, mode, TS compatibility --------------------------------

func TestDiskCacheIsMode0600InA0700DirAndCarriesNoEmail(t *testing.T) {
	setup(t, nil)
	mustConnect(t, config.Prod, "ossm")
	path := cachePath()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode = %v, want 0600", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Errorf("cache dir mode = %v, want 0700", di.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "@example.com") {
		t.Error("cache file must not contain the account email")
	}
	var d struct {
		Version  int
		Sessions map[string]map[string]any
	}
	if err := json.Unmarshal(raw, &d); err != nil || d.Version != 1 || len(d.Sessions) != 2 {
		t.Fatalf("d = %+v, err = %v; want version 1 with the support + clinic sessions", d, err)
	}
	for k, e := range d.Sessions {
		for _, f := range []string{"scope", "activeClinicId", "token", "cookies", "expiresAt"} {
			if _, ok := e[f]; !ok {
				t.Errorf("session %s lacks TS field %q", k, f)
			}
		}
	}
	if leftovers, _ := filepath.Glob(path + ".*"); len(leftovers) != 0 {
		t.Errorf("leftover temp files: %v", leftovers)
	}
}

func TestSessionCacheFalseNeverWritesTheFile(t *testing.T) {
	setup(t, map[string]any{"sessionCache": false})
	mustConnect(t, config.PreProd, "ossm")
	if _, err := os.Stat(cachePath()); !os.IsNotExist(err) {
		t.Errorf("cache file exists (stat err %v), want none with sessionCache:false", err)
	}
}

func TestDiskWriteMergesSiblingEntriesAndDropsExpiredOnes(t *testing.T) {
	setup(t, nil)
	sib := cachedSession{Scope: "direct", Token: "t", Cookies: map[string]string{}, ExpiresAt: float64(time.Now().Add(day).UnixMilli())}
	old := cachedSession{Scope: "direct", Token: "t", Cookies: map[string]string{}, ExpiresAt: float64(time.Now().Add(-day).UnixMilli())}
	if err := memo.WriteAtomic(filepath.Dir(cachePath()), filepath.Base(cachePath()), mustJSON(t, diskCache{Version: 1, Sessions: map[string]cachedSession{"sibling": sib, "stale": old}})); err != nil {
		t.Fatal(err)
	}
	mustConnect(t, config.PreProd, "ossm")
	d := readDisk()
	if _, ok := d.Sessions["sibling"]; !ok {
		t.Error("a sibling process's live entry was clobbered")
	}
	if _, ok := d.Sessions["stale"]; ok {
		t.Error("an expired entry survived the rewrite")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// testdata/ts-sessions.json is in the exact shape the TS server writes (JSON.stringify of its
// DiskCache), keyed by the TS-computed hash for (pre_prod, https://be.example.com, s@example.com,
// direct). The Go server must reuse it without a single request.
func TestReusesASessionFileWrittenByTheTSServer(t *testing.T) {
	be := setup(t, nil)
	raw, err := os.ReadFile("testdata/ts-sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := memo.WriteAtomic(filepath.Dir(cachePath()), filepath.Base(cachePath()), raw); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"copilot": map[string]any{"profiles": map[string]any{"tsuser": map[string]any{
		"pre_prod": map[string]any{"be": "https://be.example.com", "email": "s@example.com", "password": "pw"},
	}}}, "uipath": map[string]any{"orchestratorUrl": "https://cloud.uipath.com/o/t/orchestrator_", "bearer": "x"}})
	cfgPath := filepath.Join(t.TempDir(), "ts.json")
	os.WriteFile(cfgPath, cfg, 0o600)
	t.Setenv("COPILOT_MCP_CONFIG", cfgPath)
	config.ResetForTest()

	s := mustConnect(t, config.PreProd, "tsuser")
	if len(be.paths()) != 0 {
		t.Errorf("paths = %v, want none — the TS session should have been reused", be.paths())
	}
	if !strings.HasPrefix(s.Token, "h.eyJpZCI6MzM1") || s.ActiveClinicID != nil {
		t.Errorf("session = %+v", s)
	}
	if got := s.Client.(*httpClient).cookies(); got["sid"] != "abc" {
		t.Errorf("cookies = %v, want the TS file's cookie jar", got)
	}
}

func TestACorruptOrWrongVersionCacheFileReadsAsEmpty(t *testing.T) {
	setup(t, nil)
	for _, content := range []string{"{", `{"version":2,"sessions":{}}`, `{"version":1}`} {
		if err := memo.WriteAtomic(filepath.Dir(cachePath()), filepath.Base(cachePath()), []byte(content)); err != nil {
			t.Fatal(err)
		}
		if d := readDisk(); len(d.Sessions) != 0 {
			t.Errorf("readDisk(%q) = %+v, want empty", content, d)
		}
	}
}

func TestCachePathHonorsOverrideThenAbsoluteXDG(t *testing.T) {
	t.Setenv("COPILOT_MCP_CACHE", "")
	t.Setenv("XDG_CACHE_HOME", "relative/dir")
	home, _ := os.UserHomeDir()
	if got, want := cachePath(), filepath.Join(home, ".cache", "copilot-mcp", "sessions.json"); got != want {
		t.Errorf("relative XDG: %s, want %s (invalid XDG is treated as unset)", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	if got := cachePath(); got != "/xdg/copilot-mcp/sessions.json" {
		t.Errorf("cachePath = %s", got)
	}
	t.Setenv("COPILOT_MCP_CACHE", "/x/y.json")
	if got := cachePath(); got != "/x/y.json" {
		t.Errorf("cachePath = %s", got)
	}
}
