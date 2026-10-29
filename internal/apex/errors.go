// Package apex is the Apex orchestrator backend: one HTTP chokepoint (client.go), typed endpoint
// functions (api.go), pure mappers (map.go), the job.log parser (joblog.go), the reads
// (backend*.go), the only writes (actions.go) and artifact download (artifacts.go).
package apex

import (
	"fmt"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

const (
	detailCap     = 300
	forbiddenHint = " — the Apex account lacks the role for this action (e.g. jobs:download_artifacts, jobs:force_cancel); ask an Apex admin to grant it"
)

// APIError is a non-2xx answer from the Apex API (or a login that returned no session cookie).
// Path is relative to /api/v1 so callers branch on Status/Path instead of regexing text.
// Message = Apex's own `detail`, already capped. It is Expected: upstream state, not a bug here.
type APIError struct {
	shared.ExpectedError
	Status int
	Method string
	Path   string
}

func newAPIError(method, path string, status int, detail string) *APIError {
	return &APIError{
		ExpectedError: shared.ExpectedError{Msg: fmt.Sprintf("Apex %s %s -> %d: %s", method, path, status, detail)},
		Status:        status, Method: method, Path: path,
	}
}

// errorDetail is the message from an Apex error body: `detail` as a string, or pydantic's
// [{loc,msg}] list.
func errorDetail(body any, status int) string {
	var text string
	switch detail := shared.Prop(body, "detail").(type) {
	case string:
		text = detail
	case []any:
		parts := make([]string, len(detail))
		for i, item := range detail {
			parts[i] = pydanticMessage(item)
		}
		text = strings.Join(parts, "; ")
	default:
		text = bodyText(body)
	}
	if text == "" {
		text = fmt.Sprintf("HTTP %d", status)
	}
	if cut := shared.SliceUTF16(text, detailCap); cut != text {
		text = cut + "…"
	}
	if status == 403 {
		return text + forbiddenHint
	}
	return text
}

func bodyText(body any) string {
	switch b := body.(type) {
	case nil:
		return ""
	case string:
		return b
	}
	out, err := shared.MarshalJSON(body)
	if err != nil {
		return ""
	}
	return string(out)
}

func pydanticMessage(item any) string {
	text, ok := shared.StringProp(item, "msg")
	if !ok {
		out, _ := shared.MarshalJSON(item)
		text = string(out)
	}
	loc, _ := shared.Prop(item, "loc").([]any)
	if len(loc) == 0 {
		return text
	}
	parts := make([]string, len(loc))
	for i, l := range loc {
		parts[i] = fmt.Sprint(l)
	}
	return strings.Join(parts, ".") + ": " + text
}
