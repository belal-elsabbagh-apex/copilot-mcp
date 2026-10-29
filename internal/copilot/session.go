package copilot

// The single auth chokepoint for the Copilot BE: login, switch-clinic, session caching (memory +
// disk), and the rate-limit message. Nothing else in this codebase may POST to physician/login or
// support/switch-clinic.
//
// Two auth modes, resolved per profile per env by config.ResolveAuth:
//   - "direct": the pre-existing per-account physician login (unchanged behavior, now cached).
//   - "support": prod's new model — the support account logs in to a clinic-less session, then
//     POSTs /support/switch-clinic to get the clinic-scoped token that carries MANAGE_ORDERS. See
//     ~/projects/apex-auth-submit/anthem-v2-migration/docs/copilot-support-account-auth.md for the
//     full ground truth (referenced inline as §N below).
//
// Caching: sessions are cached by (env, be, email, scope) where scope is "direct", "support" (the
// clinic-less support session), or `clinic:<uid>`. In memory for the process lifetime; on disk
// (0600, under the user's cache dir, same file and format as the TS server) so a restart doesn't
// burn the 10-logins-per-15-minutes support login budget (§2). Every login path is single-flighted
// so concurrent tool calls never double-login — including the clinic-less support login shared by
// connect and ListClinics (the TS only single-flighted the final clinic session, so concurrent cold
// connects for two profiles in one env each burned a support login).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Session is an authenticated BE connection.
type Session struct {
	Client         Client // authenticated (and switched, in support mode); tagged with env
	Env            config.Env
	Profile        string // "" = no profile
	Mode           config.AuthMode
	Email          string // the account that authenticated
	ClinicUID      string // support mode only; "" otherwise
	ActiveClinicID *int64 // non-nil in support mode; may be nil in direct mode
	Token          string
}

// Clinic is one entry of the support account's clinic listing.
type Clinic struct {
	ClinicUID    string  `json:"clinicUid"`
	Name         string  `json:"name"`
	LastActiveAt *string `json:"lastActiveAt"`
	OwnerEmail   string  `json:"ownerEmail"`
}

// ---- pure helpers -----------------------------------------------------------

// TokenExpiry is the JWT `timestamp` claim (already milliseconds — §2), ok=false when
// absent/malformed.
func TokenExpiry(token string) (ms float64, ok bool) {
	v, ok := shared.JWTClaim(token, "timestamp").(float64)
	return v, ok && !math.IsInf(v, 0) && !math.IsNaN(v)
}

// TokenClinicID is the JWT `activeClinicId` claim, nil when absent/malformed (a clinic-less token).
func TokenClinicID(token string) *int64 {
	v, ok := shared.JWTClaim(token, "activeClinicId").(float64)
	if !ok {
		return nil
	}
	n := int64(v)
	return &n
}

// SessionKey is the hashed cache key, so the on-disk cache carries no account email. Byte-for-byte
// the TS hash: sha256("env|be|email|scope") hex, first 16 chars.
func SessionKey(env config.Env, be, email, scope string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s", env, be, email, scope))
	return hex.EncodeToString(sum[:])[:16]
}

func sameClinicID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ---- single flight ------------------------------------------------------------

var flights singleflight.Group

// ResetSessionCacheForTest clears the in-memory caches. Tests only — does not touch disk (tests
// point COPILOT_MCP_CACHE at a tmp file).
func ResetSessionCacheForTest() {
	cacheMu.Lock()
	clear(mem)
	cacheMu.Unlock()
}

// ---- login primitives (the ONLY two call sites that may POST these paths) ----

type loginResult struct {
	client *httpClient
	token  string
}

func doLogin(ctx context.Context, be string, env config.Env, email, password string) (loginResult, error) {
	client := newHTTPClient(be, env, nil)
	r, err := client.Req(ctx, "POST", "/api/v1/copilot/physician/login", ReqBody{JSON: map[string]any{"email": email, "password": password}})
	if err != nil {
		return loginResult{}, err
	}
	if r.Status == 429 {
		reset := r.Header.Get("ratelimit-reset")
		if reset == "" {
			reset = "?"
		}
		return loginResult{}, fmt.Errorf("login failed 429: support login rate-limited (10 logins per 15 minutes) — retry after %ss; do not rotate to another account", reset)
	}
	if r.Status >= 400 {
		return loginResult{}, fmt.Errorf("login failed %d: %s", r.Status, shared.SliceUTF16(r.Text, 300))
	}
	token, _ := shared.StringProp(r.Data, "token")
	if token == "" {
		return loginResult{}, fmt.Errorf("login succeeded but returned no token")
	}
	return loginResult{client, token}, nil
}

func doSwitchClinic(ctx context.Context, be string, env config.Env, cookies map[string]string, clinicUID string) (loginResult, error) {
	client := newHTTPClient(be, env, cookies)
	r, err := client.Req(ctx, "POST", "/api/v1/support/switch-clinic", ReqBody{JSON: map[string]any{"clinicUid": clinicUID}})
	if err != nil {
		return loginResult{}, err
	}
	if r.Status >= 400 {
		return loginResult{}, fmt.Errorf("switch-clinic failed %d for clinic %s: %s", r.Status, clinicUID, shared.SliceUTF16(r.Text, 300))
	}
	token, _ := shared.StringProp(r.Data, "token")
	if token == "" {
		return loginResult{}, fmt.Errorf("switch-clinic succeeded but returned no token for clinic %s", clinicUID)
	}
	return loginResult{client, token}, nil
}

// ---- the chokepoint -----------------------------------------------------------

// cachePut stores a session under key. A token with no readable `timestamp` claim is not cached
// (fail closed rather than guess a TTL).
func cachePut(key, scope string, lr loginResult, activeClinicID *int64) {
	expiresAt, ok := TokenExpiry(lr.token)
	if !ok || expiresAt <= 0 {
		return
	}
	cachePutEntry(key, cachedSession{
		Scope:          scope,
		ActiveClinicID: activeClinicID,
		Token:          lr.token,
		Cookies:        lr.client.cookies(),
		ExpiresAt:      expiresAt,
	})
}

func sessionFromEntry(e cachedSession, env config.Env, be string, mode config.AuthMode, email, clinicUID string) Session {
	return Session{
		Client:         newHTTPClient(be, env, e.Cookies),
		Env:            env,
		Mode:           mode,
		Email:          email,
		ClinicUID:      clinicUID,
		ActiveClinicID: e.ActiveClinicID,
		Token:          e.Token,
	}
}

func connectDirect(ctx context.Context, auth config.EnvAuth, env config.Env) (Session, error) {
	key := SessionKey(env, auth.BE, auth.Email, "direct")
	if cached, ok := cacheGet(key); ok {
		return sessionFromEntry(cached, env, auth.BE, config.ModeDirect, auth.Email, ""), nil
	}
	lr, err := doLogin(ctx, auth.BE, env, auth.Email, auth.Password)
	if err != nil {
		return Session{}, err
	}
	activeClinicID := TokenClinicID(lr.token)
	cachePut(key, "direct", lr, activeClinicID)
	return Session{Client: lr.client, Env: env, Mode: config.ModeDirect, Email: auth.Email, ActiveClinicID: activeClinicID, Token: lr.token}, nil
}

// supportCookies is the clinic-less support session's cookies (cached under its own scope — it
// survives every switch and is the cheap way to re-list clinics). Cold logins are single-flighted
// on the support key so concurrent connects/ListClinics share ONE login.
func supportCookies(ctx context.Context, env config.Env, be, email, password string) (map[string]string, error) {
	key := SessionKey(env, be, email, "support")
	cookies, err := shared.SingleFlight(ctx, &flights, key, func(ctx context.Context) (map[string]string, error) {
		if cached, ok := cacheGet(key); ok {
			return cached.Cookies, nil
		}
		lr, err := doLogin(ctx, be, env, email, password)
		if err != nil {
			return nil, err
		}
		cachePut(key, "support", lr, nil)
		return lr.client.cookies(), nil
	})
	if err != nil {
		return nil, err
	}
	return maps.Clone(cookies), nil
}

func connectSupport(ctx context.Context, auth config.EnvAuth, env config.Env) (Session, error) {
	scope := "clinic:" + auth.ClinicUID
	key := SessionKey(env, auth.BE, auth.Email, scope)
	if cached, ok := cacheGet(key); ok && cached.Scope == scope && sameClinicID(TokenClinicID(cached.Token), cached.ActiveClinicID) {
		return sessionFromEntry(cached, env, auth.BE, config.ModeSupport, auth.Email, auth.ClinicUID), nil
	}
	cookies, err := supportCookies(ctx, env, auth.BE, auth.Email, auth.Password)
	if err != nil {
		return Session{}, err
	}
	sw, err := doSwitchClinic(ctx, auth.BE, env, cookies, auth.ClinicUID)
	if err != nil {
		return Session{}, err
	}
	activeClinicID := TokenClinicID(sw.token)
	if activeClinicID == nil {
		return Session{}, fmt.Errorf("switch-clinic returned a clinic-less token for %s — refusing to use it", auth.ClinicUID)
	}
	cachePut(key, scope, sw, activeClinicID)
	return Session{Client: sw.client, Env: env, Mode: config.ModeSupport, Email: auth.Email, ClinicUID: auth.ClinicUID, ActiveClinicID: activeClinicID, Token: sw.token}, nil
}

// Connect authenticates profile ("" = no profile) in env and returns its session, from cache when
// a fresh one exists. Concurrent calls for the same (env, be, email, scope) share one login.
func Connect(ctx context.Context, env config.Env, profile string) (Session, error) {
	auth, err := config.ResolveAuth(profile, env)
	if err != nil {
		return Session{}, err
	}
	scope := "direct"
	if auth.Mode == config.ModeSupport {
		scope = "clinic:" + auth.ClinicUID
	}
	key := SessionKey(env, auth.BE, auth.Email, scope)
	s, err := shared.SingleFlight(ctx, &flights, key, func(ctx context.Context) (Session, error) {
		if auth.Mode == config.ModeSupport {
			return connectSupport(ctx, auth, env)
		}
		return connectDirect(ctx, auth, env)
	})
	if err != nil {
		return Session{}, err
	}
	s.Profile = profile
	return s, nil
}

// ListClinics lists the support account's clinics for env (the only other support-login caller).
func ListClinics(ctx context.Context, env config.Env) ([]Clinic, error) {
	support, err := config.ResolveSupport(env)
	if err != nil {
		return nil, err
	}
	cookies, err := supportCookies(ctx, env, support.BE, support.Email, support.Password)
	if err != nil {
		return nil, err
	}
	r, err := newHTTPClient(support.BE, env, cookies).Req(ctx, "GET", "/api/v1/support/clinics", ReqBody{})
	if err != nil {
		return nil, err
	}
	if r.Status >= 400 {
		return nil, fmt.Errorf("list clinics failed %d: %s", r.Status, shared.SliceUTF16(r.Text, 300))
	}
	list, ok := shared.Prop(r.Data, "clinics").([]any)
	if !ok {
		return []Clinic{}, nil
	}
	out := make([]Clinic, len(list))
	for i, c := range list {
		out[i].ClinicUID, _ = shared.StringProp(c, "clinicUid")
		out[i].Name, _ = shared.StringProp(c, "name")
		if s, ok := shared.StringProp(c, "lastActiveAt"); ok {
			out[i].LastActiveAt = &s
		}
		out[i].OwnerEmail, _ = shared.StringProp(c, "ownerEmail")
	}
	return out, nil
}

// ClinicOwnerEmail is the owner email of one clinic, "" when the clinic isn't listed.
func ClinicOwnerEmail(ctx context.Context, env config.Env, clinicUID string) (string, error) {
	clinics, err := ListClinics(ctx, env)
	if err != nil {
		return "", err
	}
	for _, c := range clinics {
		if c.ClinicUID == clinicUID {
			return c.OwnerEmail, nil
		}
	}
	return "", nil
}

// skew is how long before a token's expiry a cached session stops being served.
const skew = 10 * time.Minute
