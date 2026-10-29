package uipath

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/memo"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// UiPath Orchestrator auth token resolution: OAuth client-credentials (optional) with a static
// bearer/PAT fallback. resolveBearerToken is the single function request (client.go) calls to get
// the value that goes into the `Authorization: Bearer` header.

// expirySkew: refresh this long before the token's real expiry so a request never races a
// just-expired token.
const expirySkew = 60 * time.Second

const tokenFetchTimeout = 30 * time.Second

var tokenURLRE = regexp.MustCompile(`^(https://cloud\.uipath\.com/[^/]+)/`)

type tokenState struct {
	mu      sync.Mutex
	tok     string
	key     string // persisted-token key of tok
	expires time.Time
	gen     int // bumped on every invalidation so a fetch started before it never repopulates the cache
}

var (
	tokens      tokenState
	tokenFlight singleflight.Group
)

func init() {
	// Any config edit invalidates the cache — cheap, and avoids a rotated client secret (same
	// clientId) surviving under a stale token.
	config.OnReload(func(config.ReloadInfo) { invalidateBearerToken("") })
}

// invalidateBearerToken forces a refetch on the next resolveBearerToken call. With stale set it
// is a no-op unless the cache still holds that token: when several in-flight requests see a 401
// for the same revoked token, only the first drops it and the rest reuse the token it refetched
// (the TS version refetched once per 401). "" invalidates unconditionally (config reload, tests).
func invalidateBearerToken(stale string) {
	tokens.mu.Lock()
	if stale != "" && tokens.tok != stale {
		tokens.mu.Unlock()
		return
	}
	key := tokens.key
	if stale == "" {
		tokens.tok, tokens.expires = "", time.Time{}
		tokens.gen++
	}
	tokens.mu.Unlock()
	if stale == "" {
		return
	}
	// Outside the lock: DropToken reads config, which can fire the reload listener that
	// re-enters this function. The in-memory token is cleared only afterwards, so a concurrent
	// refetch cannot reload the rejected token from disk in between and keep it.
	memo.DropToken(key) // a 401: the persisted copy is just as dead
	tokens.mu.Lock()
	if tokens.tok == stale {
		tokens.tok, tokens.expires = "", time.Time{}
		tokens.gen++
	}
	tokens.mu.Unlock()
}

// defaultTokenURL derives the identity-server token endpoint from the org segment of
// orchestratorUrl (https://cloud.uipath.com/{org}/{tenant}/orchestrator_ ->
// .../{org}/identity_/connect/token). Only used when oauth.tokenUrl isn't set explicitly.
func defaultTokenURL(orchestratorURL string) (string, error) {
	m := tokenURLRE.FindStringSubmatch(orchestratorURL)
	if m == nil {
		return "", errors.New("cannot derive uipath.oauth.tokenUrl from orchestratorUrl; set uipath.oauth.tokenUrl explicitly")
	}
	return m[1] + "/identity_/connect/token", nil
}

func fetchOAuthToken(ctx context.Context, oauth *config.UipathOAuth, orchestratorURL string) (string, time.Duration, error) {
	tokenURL := oauth.TokenURL
	if tokenURL == "" {
		var err error
		if tokenURL, err = defaultTokenURL(orchestratorURL); err != nil {
			return "", 0, err
		}
	}
	form := "grant_type=client_credentials&client_id=" + url.QueryEscape(oauth.ClientID) +
		"&client_secret=" + url.QueryEscape(oauth.ClientSecret)
	if oauth.Scope != "" {
		form += "&scope=" + url.QueryEscape(oauth.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", 0, err
	}
	text := string(raw)
	if res.StatusCode >= http.StatusBadRequest {
		return "", 0, apiErrorFromText(http.MethodPost, tokenURL, res.StatusCode, text)
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", 0, errors.New("UiPath OAuth token response was not valid JSON")
	}
	token, _ := shared.StringProp(parsed, "access_token")
	if token == "" {
		return "", 0, errors.New("UiPath OAuth token response missing access_token")
	}
	expiresIn := 3600.0
	if n, ok := shared.Prop(parsed, "expires_in").(float64); ok {
		expiresIn = n
	}
	return token, time.Duration(expiresIn*float64(time.Second)) - expirySkew, nil
}

// oauthToken returns the cached OAuth token, fetching it (once, shared by every concurrent
// caller) when absent or near expiry. TS did not single-flight this: a fan-out against a cold
// cache fired one token request per in-flight call.
func oauthToken(ctx context.Context, cfg config.UipathConfig) (string, error) {
	if tok, ok := cachedToken(); ok {
		return tok, nil
	}
	tokens.mu.Lock()
	gen := tokens.gen
	tokens.mu.Unlock()
	return shared.SingleFlight(ctx, &tokenFlight, "oauth", func(ctx context.Context) (string, error) {
		if tok, ok := cachedToken(); ok { // a flight that just ended already refreshed it
			return tok, nil
		}
		ctx, cancel := context.WithTimeout(ctx, tokenFetchTimeout)
		defer cancel()
		tok, expires, err := persistedOrFetchToken(ctx, cfg)
		tokens.mu.Lock()
		if err == nil && tokens.gen == gen {
			tokens.tok, tokens.expires, tokens.key = tok, expires, oauthTokenKey(cfg)
		}
		tokens.mu.Unlock()
		return tok, err
	})
}

func cachedToken() (string, bool) {
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	return tokens.tok, tokens.tok != "" && time.Now().Before(tokens.expires)
}

// persistedOrFetchToken reuses the OAuth token a previous run persisted (keyed by token endpoint,
// client id and scope — never the secret), else fetches and persists a new one. Expiry is already
// net of expirySkew.
func persistedOrFetchToken(ctx context.Context, cfg config.UipathConfig) (string, time.Time, error) {
	key := oauthTokenKey(cfg)
	if tok, exp, ok := memo.LoadToken(key); ok {
		return tok, exp, nil
	}
	tok, ttl, err := fetchOAuthToken(ctx, cfg.OAuth, cfg.OrchestratorURL)
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(ttl)
	memo.SaveToken(key, tok, exp)
	return tok, exp, nil
}

func oauthTokenKey(cfg config.UipathConfig) string {
	tokenURL := cfg.OAuth.TokenURL
	if tokenURL == "" {
		tokenURL, _ = defaultTokenURL(cfg.OrchestratorURL) // an underivable URL fails the fetch anyway
	}
	return "uipath|" + tokenURL + "|" + cfg.OAuth.ClientID + "|" + cfg.OAuth.Scope
}

// resolveBearerToken resolves the token to send as `Authorization: Bearer <token>`. Tries OAuth
// first when configured (cached across calls until it's near expiry); falls back to the static
// bearer/PAT if OAuth isn't configured, or if its token request fails.
func resolveBearerToken(ctx context.Context, cfg config.UipathConfig) (string, error) {
	if cfg.OAuth != nil {
		tok, err := oauthToken(ctx, cfg)
		if err == nil {
			return tok, nil
		}
		if cfg.Bearer != "" && ctx.Err() == nil {
			return cfg.Bearer, nil
		}
		return "", err
	}
	if cfg.Bearer == "" {
		return "", fmt.Errorf("uipath config has neither oauth nor bearer configured")
	}
	return cfg.Bearer, nil
}
