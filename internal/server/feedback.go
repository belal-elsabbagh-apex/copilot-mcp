package server

// On-failure feedback: when a tool fails for a reason that looks like a bug in THIS server (not
// user/config/upstream state), the error response carries a `reportIssue` field with a prefilled
// GitHub new-issue URL so reporting is one click.
//
// Why classify at all: the user asked to nudge only on failures of unknown origin or the server's
// own responsibility — never on expected, user-actionable errors (bad profile, missing/invalid
// config, bad input, not-found, auth, any HTTP error the upstream system returned, or the
// sync_settings not-implemented stub).
//
// Why this runs on the raw error: most errors in the codebase are plain errors with the HTTP
// status baked into the message string, so the only reliable place to classify is the catch site,
// on the original error — which is exactly what toolError does.

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// RepoURL is the default repo for issues; overridable via config (feedback.repositoryUrl).
const RepoURL = "https://github.com/belal-elsabbagh-apex/copilot-mcp"

// Message shapes that mean "expected" even on a plain error (the codebase returns those):
//   - httpResponse: an upstream system returned a structured HTTP response (4xx OR 5xx): reaching
//     the external system and getting a status back is not an MCP bug, it's upstream/user state.
//   - userPhrase: a known validation / user-input / config guard fired.
var (
	httpResponse = regexp.MustCompile(`(?:->|failed)\s*\d{3}\b|\b\d{3}:\s`)
	userPhrase   = regexp.MustCompile(`(?i)unknown profile|not found|is required|provide either|no config found|config from .* is invalid|not in profile|missing the|is missing|expected 'prod'|clinicUid|clinic-less`)
)

// isExpectedFailure decides whether a failure is user/config/upstream state (stay quiet) rather
// than a likely bug in this server (suggest a GitHub issue).
func isExpectedFailure(err error) bool {
	if shared.IsExpected(err) {
		return true
	}
	// A transport failure is of unknown origin (TS: "fetch failed"); its text carries ":443: "
	// from the address, which the status-code regexp would misread as an HTTP response.
	var urlErr *url.Error
	var opErr *net.OpError
	if errors.As(err, &urlErr) || errors.As(err, &opErr) {
		return false
	}
	msg := err.Error()
	return httpResponse.MatchString(msg) || userPhrase.MatchString(msg)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return shared.SliceUTF16(line, 120)
}

func newIssueURL(repo, title, body string, labels []string) string {
	q := "title=" + shared.URIComponent(title) +
		"&body=" + shared.URIComponent(body) +
		"&labels=" + shared.URIComponent(strings.Join(labels, ","))
	return repo + "/issues/new?" + q
}

func repoBase(repositoryURL string) string {
	if repositoryURL == "" {
		repositoryURL = RepoURL
	}
	return strings.TrimRight(repositoryURL, "/")
}

// issueURL builds a prefilled GitHub new-issue URL (title + body + bug label). It carries only the
// tool name, the error message, and the server version — no tool args, no credentials.
func issueURL(tool, message, version, repositoryURL string) string {
	title := "[tool: " + tool + "] " + firstLine(message)
	body := strings.Join([]string{
		"**Tool:** `" + tool + "`",
		"**Server version:** " + version,
		"",
		"**Error:**",
		"```",
		message,
		"```",
		"",
		"_Auto-suggested by copilot-mcp on an unexpected tool failure. Please add what you were doing._",
	}, "\n")
	return newIssueURL(repoBase(repositoryURL), title, body, []string{"bug"})
}

// IssueKind is what a user-initiated report is about.
type IssueKind string

// The report kinds.
const (
	KindBug      IssueKind = "bug"
	KindFeedback IssueKind = "feedback"
)

// IssuePayload is a GitHub issue draft for a report about this MCP server.
type IssuePayload struct {
	Repo   string   `json:"repo"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
	URL    string   `json:"url"` // prefilled new-issue link — works with no GitHub tooling in the host
}

// formatMcpIssue builds a GitHub issue payload for a user-initiated report about this MCP server —
// a bug OR general feedback (idea, friction, docs gap); no tool failure required. BUILD ONLY —
// posting is the host's job (GitHub MCP / gh), or the user clicks URL. Deliberately independent of
// feedback.enabled: that flag only gates the automatic nudge on failures, never an explicit ask.
func formatMcpIssue(kind IssueKind, title, details, tool, version, repositoryURL string) IssuePayload {
	repo := repoBase(repositoryURL)
	labels := []string{"enhancement"}
	if kind == KindBug {
		labels = []string{"bug"}
	}
	issueTitle := "[" + string(kind) + "] " + firstLine(title)
	lines := []string{}
	if tool != "" {
		lines = append(lines, "**Tool:** `"+tool+"`")
	}
	lines = append(lines,
		"**Server version:** "+version,
		"",
		details,
		"",
		"_Filed via copilot-mcp build_mcp_issue._",
	)
	body := strings.Join(lines, "\n")
	return IssuePayload{Repo: repo, Title: issueTitle, Body: body, Labels: labels, URL: newIssueURL(repo, issueTitle, body, labels)}
}

type reportIssue struct {
	Message string `json:"message"`
	URL     string `json:"url"`
}

// errorBody is the JSON a failed tool returns: {error, reportIssue?}.
type errorBody struct {
	Error       string       `json:"error"`
	ReportIssue *reportIssue `json:"reportIssue,omitempty"`
}

// errorPayload builds the failure body for tool. Failures classified unknown (with feedback
// enabled) get a reportIssue block with a prefilled issue URL. Never fails: a missing config just
// keeps the defaults (enabled, default repo).
func errorPayload(tool string, err error, version string) errorBody {
	body := errorBody{Error: err.Error()}
	if isExpectedFailure(err) {
		return body
	}
	enabled, repositoryURL := true, ""
	if cfg, cfgErr := config.Feedback(); cfgErr == nil {
		enabled, repositoryURL = cfg.Enabled, cfg.RepositoryURL
	}
	if enabled {
		body.ReportIssue = &reportIssue{
			Message: "This looks like a bug in copilot-mcp — please report it.",
			URL:     issueURL(tool, body.Error, version, repositoryURL),
		}
	}
	return body
}
