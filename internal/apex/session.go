package apex

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	apiPrefix     = "/api/v1"
	loginPath     = "/auth/login"
	maxPageSize   = 200 // above this the server answers 422
	tokenSkew     = 60 * time.Second
	fallbackTTL   = 10 * time.Minute // when the JWT carries no readable exp
	tokenCookie   = "access_token"
	loginDeadline = 60 * time.Second
)

type token struct {
	value     string
	expiresAt time.Time
}

// folderSet is name -> uuid for every folder, with the listing's order kept for messages.
type folderSet struct {
	ids   map[string]string
	names []string
}

// sess is the process-level Apex session: the JWT and the folder-id cache. gen invalidates
// results of calls that were in flight when a reset happened.
var sess struct {
	mu      sync.Mutex
	gen     int
	token   *token
	folders *folderSet
}

// flights single-flights the login and the folder listing, one key each.
var flights singleflight.Group

const (
	loginFlight   = "login"
	foldersFlight = "folders"
)

// ResetClient forgets the session token, the folder-id cache and the terminal-job read cache. Config reloads call it (a config
// edit may change the account, URL or folders); tests call it between cases.
func ResetClient() {
	flights.Forget(loginFlight) // a call in flight under the old account/URL must not serve new callers
	flights.Forget(foldersFlight)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.gen++
	sess.token, sess.folders = nil, nil
	readCache.Flush() // account, URL or folders may have changed: nothing cached under the old ones is valid
}

func init() { config.OnReload(func(config.ReloadInfo) { ResetClient() }) }

// jwtExpiry is the `exp` (seconds) of the JWT payload; ok=false when not readable.
func jwtExpiry(jwt string) (time.Time, bool) {
	exp, ok := shared.JWTClaim(jwt, "exp").(float64)
	return time.UnixMilli(int64(exp * 1000)), ok
}

// login's only useful output is the `access_token` Set-Cookie — the body holds user info.
func login(ctx context.Context, cfg config.ApexConfig) (token, error) {
	body, err := shared.MarshalJSON(map[string]string{"username": cfg.Username, "password": cfg.Password})
	if err != nil {
		return token{}, err
	}
	res, err := send(ctx, http.MethodPost, cfg.APIURL+apiPrefix+loginPath,
		map[string]string{"Content-Type": "application/json", "Accept": "application/json"}, body)
	if err != nil {
		return token{}, err
	}
	text, err := readBody(res)
	if err != nil {
		return token{}, err
	}
	if res.StatusCode >= 400 {
		return token{}, newAPIError("POST", loginPath, res.StatusCode, errorDetail(shared.SafeJSONParse(text), res.StatusCode))
	}
	var value string
	for _, c := range res.Cookies() {
		if c.Name == tokenCookie {
			value = c.Value
			break
		}
	}
	if value == "" {
		return token{}, newAPIError("POST", loginPath, res.StatusCode, "login succeeded but returned no access_token cookie")
	}
	expiry, ok := jwtExpiry(value)
	if !ok {
		expiry = time.Now().Add(fallbackTTL)
	}
	return token{value: value, expiresAt: expiry.Add(-tokenSkew)}, nil
}

func tokenKey(cfg config.ApexConfig) string { return "apex|" + cfg.APIURL + "|" + cfg.Username }

// loadOrLogin reuses the JWT a previous run persisted (its expiry already net of tokenSkew), else
// logs in and persists the new one — a restart must not burn the rate-limited login route.
func loadOrLogin(ctx context.Context, cfg config.ApexConfig) (token, error) {
	if v, exp, ok := memo.LoadToken(tokenKey(cfg)); ok {
		return token{value: v, expiresAt: exp}, nil
	}
	t, err := login(ctx, cfg)
	if err == nil {
		memo.SaveToken(tokenKey(cfg), t.value, t.expiresAt)
	}
	return t, err
}

// bearerToken is the live JWT; concurrent callers share one login (the login route is
// rate-limited). The login runs detached from the first caller's ctx so that caller's
// cancellation cannot fail the others.
func bearerToken(ctx context.Context, cfg config.ApexConfig) (string, error) {
	if v, ok := liveToken(); ok {
		return v, nil
	}
	sess.mu.Lock()
	gen := sess.gen
	sess.mu.Unlock()
	return shared.SingleFlight(ctx, &flights, loginFlight, func(ctx context.Context) (string, error) {
		if v, ok := liveToken(); ok { // a flight that just ended already logged in
			return v, nil
		}
		ctx, cancel := context.WithTimeout(ctx, loginDeadline)
		defer cancel()
		t, err := loadOrLogin(ctx, cfg)
		sess.mu.Lock()
		if err == nil && sess.gen == gen {
			sess.token = &t
		}
		sess.mu.Unlock()
		return t.value, err
	})
}

func liveToken() (string, bool) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.token != nil && sess.token.expiresAt.After(time.Now()) {
		return sess.token.value, true
	}
	return "", false
}

// dropToken forgets the token a 401 rejected (memory and disk) — unless a concurrent request
// already refreshed it.
func dropToken(cfg config.ApexConfig, used string) {
	sess.mu.Lock()
	drop := sess.token != nil && sess.token.value == used
	sess.mu.Unlock()
	if !drop {
		return
	}
	// Outside the lock: DropToken reads config, which can fire the reload listener
	// (ResetClient) that takes sess.mu. The in-memory token is cleared only afterwards, so a
	// concurrent 401 retry cannot reload the rejected JWT from disk in between and keep it.
	memo.DropToken(tokenKey(cfg))
	sess.mu.Lock()
	if sess.token != nil && sess.token.value == used {
		sess.token = nil
	}
	sess.mu.Unlock()
}

// folderMap is name -> uuid for every folder. Cached for the process; a failed fetch is not cached.
// ponytail: one page (200 folders) — walk total_pages if a deployment ever has more.
// This is the one call that cannot carry X-Folder-Id: it is how the ids are found.
func folderMap(ctx context.Context, cfg config.ApexConfig) (folderSet, error) {
	cached := func() *folderSet {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.folders
	}
	if set := cached(); set != nil {
		return *set, nil
	}
	sess.mu.Lock()
	gen := sess.gen
	sess.mu.Unlock()
	return shared.SingleFlight(ctx, &flights, foldersFlight, func(ctx context.Context) (folderSet, error) {
		if set := cached(); set != nil {
			return *set, nil
		}
		ctx, cancel := context.WithTimeout(ctx, loginDeadline)
		defer cancel()
		set, err := fetchFolders(ctx, cfg)
		if err == nil {
			sess.mu.Lock()
			if sess.gen == gen {
				sess.folders = &set
			}
			sess.mu.Unlock()
		}
		return set, err
	})
}

func fetchFolders(ctx context.Context, cfg config.ApexConfig) (folderSet, error) {
	res, err := transport(ctx, cfg, http.MethodGet, cfg.APIURL+apiPrefix+"/folders?page_size=200",
		map[string]string{"Accept": "application/json"}, nil)
	if err != nil {
		return folderSet{}, err
	}
	text, err := readBody(res)
	if err != nil {
		return folderSet{}, err
	}
	body := shared.SafeJSONParse(text)
	if res.StatusCode >= 400 {
		return folderSet{}, newAPIError("GET", "/folders", res.StatusCode, errorDetail(body, res.StatusCode))
	}
	set := folderSet{ids: map[string]string{}}
	rows, _ := shared.Prop(body, "items").([]any)
	for _, row := range rows {
		id, okID := shared.StringProp(row, "id")
		name, okName := shared.StringProp(row, "name")
		if !okID || !okName {
			continue
		}
		if _, dup := set.ids[name]; !dup {
			set.names = append(set.names, name)
		}
		set.ids[name] = id
	}
	return set, nil
}

// Folder is a resolved Apex folder: the X-Folder-Id uuid and its name.
type Folder struct {
	ID   string
	Name string
}

func lookupFolder(ctx context.Context, cfg config.ApexConfig, env config.Env, folder string) (Folder, error) {
	name := folder
	if name == "" {
		name = cfg.FolderByEnv[env]
	}
	set, err := folderMap(ctx, cfg)
	if err != nil {
		return Folder{}, err
	}
	id, ok := set.ids[name]
	if !ok {
		available := strings.Join(set.names, ", ")
		if available == "" {
			available = "none"
		}
		return Folder{}, shared.Expectedf("Apex folder '%s' not found (available: %s)", name, available)
	}
	return Folder{ID: id, Name: name}, nil
}

// ResolveFolderID is the X-Folder-Id uuid for an env's configured folder, or for an explicit
// `folder` name ("" = the env's folder).
func ResolveFolderID(ctx context.Context, env config.Env, folder string) (Folder, error) {
	cfg, err := config.ResolveApex()
	if err != nil {
		return Folder{}, err
	}
	return lookupFolder(ctx, cfg, env, folder)
}

// Login logs in now (or reuses the live session) without making a data call — what `doctor` probes.
func Login(ctx context.Context) error {
	cfg, err := config.ResolveApex()
	if err != nil {
		return err
	}
	_, err = bearerToken(ctx, cfg)
	return err
}

// DashboardLink is the dashboard deep link (`/jobs/{id}` is served from the same base URL); ""
// when Apex is unconfigured.
func DashboardLink(path string) string {
	cfg, err := config.ResolveApex()
	if err != nil {
		return ""
	}
	return cfg.APIURL + path
}
