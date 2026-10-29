package uipath

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// maxConcurrency bounds every UiPath fan-out (per-job fetches, per-job log fetches). TS used
// chunks of 10 behind a barrier; this is a worker pool, so a slow row no longer stalls the rest
// of its chunk.
const maxConcurrency = 8

// httpClient shares one transport for every UiPath call. MaxIdleConnsPerHost is raised above the
// stdlib default of 2 so a maxConcurrency-wide fan-out reuses connections instead of
// re-handshaking TLS on each wave.
var httpClient = &http.Client{Transport: newTransport()}

func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 4 * maxConcurrency
	t.MaxIdleConnsPerHost = 2 * maxConcurrency
	return t
}

// rateGate pauses every UiPath request until the latest Retry-After a 429 asked for, so a herd of
// concurrent requests does not all retry at once (TS slept per-request).
type rateGate struct {
	mu       sync.Mutex
	resumeAt time.Time
}

var gate rateGate

func (g *rateGate) pause(d time.Duration) {
	at := time.Now().Add(d)
	g.mu.Lock()
	defer g.mu.Unlock()
	if at.After(g.resumeAt) {
		g.resumeAt = at
	}
}

// wait blocks until the gate is open (re-checking, as another 429 may extend it while we sleep).
func (g *rateGate) wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		d := time.Until(g.resumeAt)
		g.mu.Unlock()
		if d <= 0 {
			return nil
		}
		if err := shared.Sleep(ctx, d); err != nil {
			return err
		}
	}
}

// reqOpts are the optional parts of one Orchestrator call.
type reqOpts struct {
	params map[string]string // become the OData querystring
	body   any               // JSON-encoded when non-nil; adds Content-Type: application/json
	// folder is X-UIPATH-FolderPath ("" omits the header).
	folder string
	// orgUnitID is X-UIPATH-OrganizationUnitId, the numeric folder id ("" omits the header). The
	// QueueItems/QueueDefinitions endpoints are addressed by it, while Jobs accept the folder path.
	orgUnitID string
}

func base(cfg config.UipathConfig) string { return strings.TrimSuffix(cfg.OrchestratorURL, "/") }

// request makes one Orchestrator OData/REST call and returns the parsed JSON (nil for an empty
// body, e.g. a DELETE 204) or an *APIError on a non-2xx.
//
// A 401 gets exactly one retry with a freshly-refetched token — recovers a revoked/expired OAuth
// token instead of failing for the rest of the process (a no-op, harmless retry when only a static
// bearer/PAT is configured). A 429 gets up to shared.MaxRateLimitRetries retries after the
// server-told Retry-After, which pauses all UiPath traffic. The two counters are independent, so a
// 401 retry never eats into the 429 budget or vice versa.
//
// Both retries apply to every method: a 401 or 429 means the request was not processed, so
// replaying a write cannot double-post (the TS client did the same).
func request(ctx context.Context, method, path string, o reqOpts) (any, error) {
	cfg, err := config.Uipath()
	if err != nil {
		return nil, err
	}
	target := base(cfg) + path
	if len(o.params) > 0 {
		target += "?" + url.Values(toValues(o.params)).Encode()
	}
	var payload []byte
	if o.body != nil {
		if payload, err = shared.MarshalJSON(o.body); err != nil {
			return nil, err
		}
	}
	refreshed, rateLimited := false, 0
	for {
		if err := gate.wait(ctx); err != nil {
			return nil, err
		}
		token, err := resolveBearerToken(ctx, cfg)
		if err != nil {
			return nil, err
		}
		status, header, text, err := send(ctx, method, target, token, payload, o)
		if err != nil {
			return nil, err
		}
		switch {
		case status == http.StatusUnauthorized && !refreshed:
			invalidateBearerToken(token)
			refreshed = true
			continue
		case status == http.StatusTooManyRequests && rateLimited < shared.MaxRateLimitRetries:
			gate.pause(shared.RateLimitDelay(header.Get("Retry-After")))
			rateLimited++
			continue
		}
		if status >= http.StatusBadRequest {
			return nil, apiErrorFromText(method, target, status, text)
		}
		if text == "" {
			return nil, nil
		}
		return shared.SafeJSONParse(text), nil
	}
}

func send(ctx context.Context, method, target, token string, payload []byte, o reqOpts) (int, http.Header, string, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	// Folder scoping — copilot-doctor uses the folder-path header. Jobs live in a per-env folder
	// (prod "Authorization" vs dev "Authorization Dev Clone").
	if o.folder != "" {
		req.Header.Set("X-UIPATH-FolderPath", o.folder)
	}
	if o.orgUnitID != "" {
		req.Header.Set("X-UIPATH-OrganizationUnitId", o.orgUnitID)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(raw), err
}

func get(ctx context.Context, path string, params map[string]string, folder, orgUnitID string) (any, error) {
	return request(ctx, http.MethodGet, path, reqOpts{params: params, folder: folder, orgUnitID: orgUnitID})
}

// post and del: Orchestrator writes always carry an explicit FolderScope — unlike the GET helpers
// there is no folder fallback, so a write can never land in an implicit folder.
func post(ctx context.Context, path string, body any, scope FolderScope) (any, error) {
	return request(ctx, http.MethodPost, path, reqOpts{body: body, folder: scope.FolderPath, orgUnitID: scope.OrgUnitID})
}

func del(ctx context.Context, path string, scope FolderScope) (any, error) {
	return request(ctx, http.MethodDelete, path, reqOpts{folder: scope.FolderPath, orgUnitID: scope.OrgUnitID})
}

func toValues(m map[string]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = []string{v}
	}
	return out
}
