// Package uipath is the UiPath Orchestrator backend: the OData/REST client, the pure
// UiPath -> orchestrator-core mappers, the pre_prod-only writes, and the
// orchestrator.Backend implementation (Backend).
package uipath

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const snippetCap = 300

// APIError is the one error type for a non-2xx HTTP response anywhere in the UiPath domain
// (Orchestrator OData/REST calls, the OAuth token endpoint). It carries enough structure for
// callers to branch on Status — e.g. treating a 404 as "no such record" while letting a
// 401/500/etc. propagate — instead of matching message text.
//
// It is an Expected error: reaching UiPath and getting a structured HTTP status back is upstream
// state, not a bug in this server.
type APIError struct {
	shared.ExpectedError
	Status int
	Method string
	URL    string
	// Body is the parsed JSON body when the response was JSON, else the raw text.
	Body any
}

// apiErrorFromText builds an APIError from a raw response body. The message quotes the JSON
// compacted in its original key order (a decoded Go map would sort the keys).
func apiErrorFromText(method, url string, status int, text string) *APIError {
	body := shared.SafeJSONParse(text)
	if _, isText := body.(string); !isText && body != nil {
		var buf bytes.Buffer
		if json.Compact(&buf, []byte(text)) == nil {
			text = buf.String()
		}
	}
	return newAPIError(method, url, status, body, text)
}

func newAPIError(method, url string, status int, body any, text string) *APIError {
	if cut := shared.SliceUTF16(text, snippetCap); cut != text {
		text = cut + "…"
	}
	return &APIError{
		ExpectedError: shared.ExpectedError{Msg: fmt.Sprintf("UiPath %s %s -> %d: %s", method, url, status, text)},
		Status:        status,
		Method:        method,
		URL:           url,
		Body:          body,
	}
}

// IsNotFound is true only for "the requested entity doesn't exist" — the one status a caller may
// fold into a null/empty result instead of propagating. Any other status (401, 500, a malformed
// request, ...) must propagate.
func IsNotFound(err error) bool {
	e, ok := errors.AsType[*APIError](err)
	return ok && e.Status == http.StatusNotFound
}
