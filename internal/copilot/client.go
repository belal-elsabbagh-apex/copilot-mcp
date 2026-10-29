// Package copilot is the EHR Copilot BE domain: the HTTP client, the single auth chokepoint
// (session.go), the pre-prod order mint engine, order search and order documents.
//
// It imports only shared and config — never the orchestrator packages.
package copilot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// requestTimeout is a ceiling so a hung socket can't wedge a tool call forever; per-call
// cancellation still comes from ctx.
const requestTimeout = 5 * time.Minute

var httpDoer = &http.Client{Timeout: requestTimeout}

// Response is one BE reply. Data is the body parsed as JSON, or the raw text when it isn't JSON
// (nil for an empty body); Text is always the raw body.
type Response struct {
	Status int
	Data   any
	Text   string
	Header http.Header
}

// FilePart is one multipart/form-data file field.
type FilePart struct {
	Field, Filename, ContentType string
	Data                         []byte
}

// ReqBody is the optional body of a request: JSON (nil = none) or a multipart file.
type ReqBody struct {
	JSON any
	Form *FilePart
}

// Client is a BE HTTP client for one tenant. It is an interface because the write engines and the
// tools built on them are tested against scripted fakes.
type Client interface {
	Base() string
	// Env is the tenant this client points at; "" means untagged. Write engines refuse anything
	// but a client tagged config.PreProd.
	Env() config.Env
	// Req performs one request. A non-2xx status is NOT an error — callers read Response.Status.
	Req(ctx context.Context, method, path string, body ReqBody) (Response, error)
}

// AssertPreProdClient is the fail-closed guard for the BE write engines (mint, order delete,
// settings apply): only a client explicitly tagged pre_prod may write. An untagged client is
// refused too, so a future call site can't silently write to the wrong tenant.
func AssertPreProdClient(c Client, what string) error {
	if c.Env() == config.PreProd {
		return nil
	}
	tag := string(c.Env())
	if tag == "" {
		tag = "untagged"
	}
	return fmt.Errorf("%s writes are pre_prod-only — refusing client tagged '%s' (%s)", what, tag, c.Base())
}

// httpClient is a minimal cookie-jar client (mirrors the BE's set-cookie session flow). It is safe
// for concurrent use.
type httpClient struct {
	base string
	env  config.Env
	mu   sync.Mutex
	jar  map[string]string
}

// newHTTPClient returns an unauthenticated client for base. Pass env to tag it with its tenant —
// required for clients handed to a write engine (see AssertPreProdClient). It seeds the cookie jar
// from cookies (copied); session.go snapshots it back out
// after a request populates it.
func newHTTPClient(base string, env config.Env, cookies map[string]string) *httpClient {
	return &httpClient{base: base, env: env, jar: maps.Clone(cookies)}
}

func (c *httpClient) Base() string    { return c.base }
func (c *httpClient) Env() config.Env { return c.env }
func (c *httpClient) cookies() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jar == nil {
		return map[string]string{}
	}
	return maps.Clone(c.jar)
}

func (c *httpClient) cookieHeader() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := make([]string, 0, len(c.jar))
	for _, k := range slices.Sorted(maps.Keys(c.jar)) {
		parts = append(parts, k+"="+c.jar[k])
	}
	return strings.Join(parts, "; ")
}

func (c *httpClient) storeCookies(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sc := range h.Values("Set-Cookie") {
		pair, _, _ := strings.Cut(sc, ";")
		i := strings.Index(pair, "=")
		if i <= 0 {
			continue
		}
		if c.jar == nil {
			c.jar = map[string]string{}
		}
		c.jar[strings.TrimSpace(pair[:i])] = strings.TrimSpace(pair[i+1:])
	}
}

func (c *httpClient) Req(ctx context.Context, method, path string, body ReqBody) (Response, error) {
	var reader io.Reader
	contentType := ""
	switch {
	case body.JSON != nil:
		b, err := shared.MarshalJSON(body.JSON)
		if err != nil {
			return Response{}, fmt.Errorf("encode %s %s body: %w", method, path, err)
		}
		reader, contentType = bytes.NewReader(b), "application/json"
	case body.Form != nil:
		b, ct, err := encodeForm(body.Form)
		if err != nil {
			return Response{}, err
		}
		reader, contentType = bytes.NewReader(b), ct
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return Response{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if h := c.cookieHeader(); h != "" {
		req.Header.Set("Cookie", h)
	}
	res, err := httpDoer.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = res.Body.Close() }()
	c.storeCookies(res.Header)
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return Response{}, err
	}
	text := string(raw)
	return Response{Status: res.StatusCode, Data: shared.SafeJSONParse(text), Text: text, Header: res.Header}, nil
}

func encodeForm(f *FilePart) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.Field, f.Filename))
	h.Set("Content-Type", f.ContentType)
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(f.Data); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// ReqWithRefresh retries once on a 403 missing_token (session-cookie race) — the same race recurs
// at every write call site (MintPreprodOrder's PUTs and note upload, SubmitOrder), so they all
// share this instead of hand-copying the check-refresh-retry sequence.
func ReqWithRefresh(ctx context.Context, c Client, method, path string, body ReqBody) (Response, error) {
	r, err := c.Req(ctx, method, path, body)
	if err != nil || r.Status != http.StatusForbidden || !strings.Contains(r.Text, "missing_token") {
		return r, err
	}
	if _, err := c.Req(ctx, "GET", "/api/v1/physician/refresh", ReqBody{}); err != nil {
		return Response{}, err
	}
	return c.Req(ctx, method, path, body)
}
