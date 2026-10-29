package apex

// The single Apex HTTP chokepoint — nothing else in the repo may talk to Apex.
// Owns: login (session.go: JWT from the Set-Cookie, in-memory, single-flighted, refreshed before
// its own `exp`), folder-name -> X-Folder-Id resolution (sent on every apexRequest call; login,
// the /folders listing and apexDownload are the documented exceptions — omitting it silently reads
// the user's default folder), the pre_prod-only write gate, retries (GETs only), and error
// shaping. Apex routes take paths relative to `/api/v1`.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	getAttempts       = 3 // network-error retries apply to GETs only: a retried write could double-post
	networkBackoff    = 50 * time.Millisecond
	maxRateLimitRetry = 3
	// fanOut bounds concurrent data GETs. The TS fanned out unbounded (get_job/get_job_logs over
	// up to 25 ids, list_jobs over every matching workflow); Apex rate-limits, so cap it here.
	fanOut = 8
)

var (
	// httpClient shares one transport; MaxIdleConnsPerHost covers the fan-out bound (default is 2).
	httpClient = &http.Client{Transport: apexTransport()}
	dataSem    = make(chan struct{}, fanOut)
)

func apexTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // DefaultTransport is always *http.Transport
	t.MaxIdleConnsPerHost = 2 * fanOut
	// 300s is undici's default headers timeout, which the TS client ran under: a slow POST /jobs may
	// already have started and writes are never retried, so a shorter cap could fail a live start.
	t.ResponseHeaderTimeout = 300 * time.Second
	return t
}

// acquire takes a data-GET slot; the returned func releases it.
func acquire(ctx context.Context) (release func(), err error) {
	select {
	case dataSem <- struct{}{}:
		return func() { <-dataSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// send performs one HTTP call, no auth and no retry.
func send(ctx context.Context, method, target string, headers map[string]string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return httpClient.Do(req)
}

// readBody drains and closes the response, returning the body as text.
func readBody(res *http.Response) (string, error) {
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	return string(b), err
}

// transport is one authenticated request. A 401 re-logins exactly once and retries (any method:
// the server rejected the credentials, so nothing ran). Only GETs retry on network errors or 429;
// writes surface the first failure. The caller owns the returned body.
func transport(ctx context.Context, cfg config.ApexConfig, method, target string, headers map[string]string, body []byte) (*http.Response, error) {
	relogged := false
	rateLimited, netFailures := 0, 0
	for {
		used, err := bearerToken(ctx, cfg)
		if err != nil {
			return nil, err
		}
		h := map[string]string{"Authorization": "Bearer " + used}
		for k, v := range headers {
			h[k] = v
		}
		res, err := send(ctx, method, target, h, body)
		if err != nil {
			netFailures++
			if ctx.Err() != nil || method != http.MethodGet || netFailures >= getAttempts {
				return nil, err
			}
			if err := shared.Sleep(ctx, networkBackoff*time.Duration(netFailures)); err != nil {
				return nil, err
			}
			continue
		}
		if res.StatusCode == http.StatusUnauthorized && !relogged {
			relogged = true
			_ = res.Body.Close()
			dropToken(cfg, used) // a concurrent request may already have refreshed it
			continue
		}
		if res.StatusCode == http.StatusTooManyRequests && method == http.MethodGet && rateLimited < maxRateLimitRetry {
			rateLimited++
			delay := shared.RateLimitDelay(res.Header.Get("Retry-After"))
			_ = res.Body.Close()
			if err := shared.Sleep(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		return res, nil
	}
}

// Prod and test folders share one deployment and one login, so this gate — checked before
// anything touches the network — is the only thing keeping writes out of prod.
func assertWritable(env config.Env, method, path, folder string) error {
	if method == http.MethodGet {
		return nil
	}
	if env != config.PreProd {
		return shared.Expectedf("Apex %s %s refused: writes are pre_prod-only (env=%s)", method, path, env)
	}
	if folder != "" {
		return shared.Expectedf("Apex %s %s refused: writes never take a folder override", method, path)
	}
	return nil
}

// A pre_prod folder that resolves to the prod folder would point every "pre_prod-only" write at
// prod — refuse before the write is sent. Uses the cached /folders listing: no extra request.
func assertEnvFoldersDistinct(ctx context.Context, cfg config.ApexConfig, method, path string) error {
	preProd, err := lookupFolder(ctx, cfg, config.PreProd, "")
	if err != nil {
		return err
	}
	prod, err := lookupFolder(ctx, cfg, config.Prod, "")
	if err != nil {
		return err
	}
	if preProd.ID == prod.ID {
		return shared.Expectedf("Apex %s %s refused: pre_prod and prod resolve to the same folder ('%s' / '%s') — fix apex.folderByEnv",
			method, path, preProd.Name, prod.Name)
	}
	return nil
}

// reqOpts are the optional parts of an apexRequest.
type reqOpts struct {
	Query  url.Values
	Body   any    // nil = no body; pass map[string]any{} for an empty JSON object
	Folder string // folder NAME override, reads only; "" = the env's folder
}

// apexRequest is one Apex JSON call, scoped to the env's folder (or opts.Folder, reads only).
// Returns the raw body (nil when empty); an *APIError on a non-2xx.
func apexRequest(ctx context.Context, env config.Env, method, path string, opts reqOpts) ([]byte, error) {
	if err := assertWritable(env, method, path, opts.Folder); err != nil {
		return nil, err
	}
	cfg, err := config.ResolveApex()
	if err != nil {
		return nil, err
	}
	if method != http.MethodGet {
		if err := assertEnvFoldersDistinct(ctx, cfg, method, path); err != nil {
			return nil, err
		}
	}
	folder, err := lookupFolder(ctx, cfg, env, opts.Folder)
	if err != nil {
		return nil, err
	}
	target := cfg.APIURL + apiPrefix + path
	if qs := opts.Query.Encode(); qs != "" {
		target += "?" + qs
	}
	headers := map[string]string{"Accept": "application/json", "X-Folder-Id": folder.ID}
	var body []byte
	if opts.Body != nil {
		headers["Content-Type"] = "application/json"
		if body, err = shared.MarshalJSON(opts.Body); err != nil {
			return nil, err
		}
	}
	if method == http.MethodGet {
		release, err := acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	res, err := transport(ctx, cfg, method, target, headers, body)
	if err != nil {
		return nil, err
	}
	text, err := readBody(res)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, newAPIError(method, path, res.StatusCode, errorDetail(shared.SafeJSONParse(text), res.StatusCode))
	}
	return []byte(text), nil
}

// getJSON GETs a route and decodes its JSON body into T.
func getJSON[T any](ctx context.Context, env config.Env, path, folder string) (T, error) {
	var out T
	raw, err := apexRequest(ctx, env, http.MethodGet, path, reqOpts{Folder: folder})
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

// Download is the metadata of a streamed artifact read.
type Download struct {
	Status       int
	ContentRange string // "" when absent
	Bytes        int64
}

// apexDownload streams the bytes of a `download_url` exactly as the artifact listing returns it
// (`/api/v1/jobs/{id}/artifacts/{artifactId}`) into dst. Bearer only: downloads need no folder
// header. A Range read (`bytes=-65536`) gives a cheap tail of a large file. GET-only, so no write
// gate. Nothing is written to dst on a non-2xx.
func apexDownload(ctx context.Context, downloadURL, rangeHeader string, dst io.Writer) (Download, error) {
	// Only same-origin API paths: the Bearer token must never be sent to another host.
	if !strings.HasPrefix(downloadURL, apiPrefix+"/") || strings.Contains(downloadURL, "..") {
		return Download{}, shared.Expectedf("Apex download refused: '%s' is not an %s/ path", downloadURL, apiPrefix)
	}
	cfg, err := config.ResolveApex()
	if err != nil {
		return Download{}, err
	}
	headers := map[string]string{}
	if rangeHeader != "" {
		headers["Range"] = rangeHeader
	}
	release, err := acquire(ctx)
	if err != nil {
		return Download{}, err
	}
	defer release()
	res, err := transport(ctx, cfg, http.MethodGet, cfg.APIURL+downloadURL, headers, nil)
	if err != nil {
		return Download{}, err
	}
	if res.StatusCode >= 400 {
		text, rerr := readBody(res)
		if rerr != nil {
			return Download{}, rerr
		}
		return Download{}, newAPIError("GET", strings.TrimPrefix(downloadURL, apiPrefix), res.StatusCode, errorDetail(shared.SafeJSONParse(text), res.StatusCode))
	}
	defer func() { _ = res.Body.Close() }()
	n, err := io.Copy(dst, res.Body)
	if err != nil {
		return Download{}, err
	}
	return Download{Status: res.StatusCode, ContentRange: res.Header.Get("Content-Range"), Bytes: n}, nil
}

// notFound reports the one 404 a caller may fold into "no such record"; any other status propagates.
func notFound(err error) bool {
	e, ok := errors.AsType[*APIError](err)
	return ok && e.Status == http.StatusNotFound
}
