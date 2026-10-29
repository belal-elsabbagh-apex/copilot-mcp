package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func TestFailuresThatAreUserOrUpstreamStateStayQuiet(t *testing.T) {
	cases := map[string]error{
		"explicit expected error":   shared.Expectedf("nope"),
		"wrapped expected error":    fmt.Errorf("sync: %w", shared.Expectedf("sync_settings not implemented")),
		"unknown profile":           errors.New("unknown profile 'x' (available: ossm)"),
		"auth 401":                  errors.New("login failed 401: forbidden"),
		"upstream 500":              errors.New("UiPath GET /odata/Jobs -> 500: boom"),
		"not found":                 errors.New("order abc not found in this env"),
		"missing config":            errors.New("No config found. Set COPILOT_MCP_CONFIG ..."),
		"invalid config":            errors.New("Config from x is invalid:\n  - copilot.prod: be is required"),
		"bad env":                   errors.New("env 'x' not in profile (expected 'prod' or 'pre_prod')"),
		"missing env auth":          errors.New("profile 'x' has no pre_prod auth — add clinicUid (support account) or be/email/password (direct login)"),
		"clinic-less switch result": errors.New("switch-clinic returned a clinic-less token for abc — refusing to use it"),
	}
	for name, err := range cases {
		if !isExpectedFailure(err) {
			t.Errorf("isExpectedFailure(%s: %q) = false, want true", name, err)
		}
	}
}

func TestFailuresThatLookLikeOurBugGetFlagged(t *testing.T) {
	cases := map[string]error{
		"nil dereference":        errors.New("runtime error: invalid memory address or nil pointer dereference"),
		"missing field in reply": errors.New("create draft returned no orderUid: {}"),
		"bad date":               errors.New("unrecognized date 13/40/2026"),
		"bare error":             errors.New("boom"),
	}
	for name, err := range cases {
		if isExpectedFailure(err) {
			t.Errorf("isExpectedFailure(%s: %q) = true, want false", name, err)
		}
	}
}

func queryOf(t *testing.T, rawURL string) url.Values {
	t.Helper()
	_, q, _ := strings.Cut(rawURL, "?")
	v, err := url.ParseQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIssueURLCarriesToolVersionAndBugLabel(t *testing.T) {
	u := issueURL("clone_order", "boom happened", "9.9.9", "")
	if !strings.HasPrefix(u, RepoURL+"/issues/new?") {
		t.Fatalf("issueURL() = %q, want prefix %q", u, RepoURL+"/issues/new?")
	}
	q := queryOf(t, u)
	for field, want := range map[string]string{"title": "[tool: clone_order]", "body": "9.9.9"} {
		if !strings.Contains(q.Get(field), want) {
			t.Errorf("issueURL() %s = %q, want containing %q", field, q.Get(field), want)
		}
	}
	if q.Get("labels") != "bug" || !strings.Contains(q.Get("body"), "boom happened") {
		t.Errorf("issueURL() labels=%q body=%q, want bug label and the error text", q.Get("labels"), q.Get("body"))
	}
}

func TestIssueURLEncodesLikeEncodeURIComponent(t *testing.T) {
	u := issueURL("t", "a b+c(d)!'*", "1", "")
	if strings.Contains(u, "+") || !strings.Contains(u, "a%20b%2Bc(d)!'*") {
		t.Errorf("issueURL() = %q, want space as %%20, + as %%2B and !'()* literal", u)
	}
}

func TestIssueURLHonoursRepositoryOverrideAndTrimsTrailingSlash(t *testing.T) {
	u := issueURL("t", "m", "1", "https://github.com/acme/repo/")
	if !strings.HasPrefix(u, "https://github.com/acme/repo/issues/new?") {
		t.Errorf("issueURL() = %q, want the override repo with the slash trimmed", u)
	}
}

func TestBugReportUsesBugLabelTitleAndMatchingURL(t *testing.T) {
	p := formatMcpIssue(KindBug, "clone_order loses ICD codes", "Cloned order abc had 3 ICDs in prod but 0 in pre-prod.", "clone_order", "9.9.9", "")
	if p.Repo != RepoURL || p.Title != "[bug] clone_order loses ICD codes" || strings.Join(p.Labels, ",") != "bug" {
		t.Errorf("formatMcpIssue() = %+v, want default repo, [bug] title, bug label", p)
	}
	for _, want := range []string{"`clone_order`", "9.9.9", "0 in pre-prod"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("body = %q, want containing %q", p.Body, want)
		}
	}
	q := queryOf(t, p.URL)
	if q.Get("title") != p.Title || q.Get("body") != p.Body || q.Get("labels") != "bug" {
		t.Errorf("url query = %v, want it to round-trip title/body/labels", q)
	}
}

func TestFeedbackReportUsesEnhancementLabelAndOmitsToolLine(t *testing.T) {
	p := formatMcpIssue(KindFeedback, "add a dry-run flag to apply_settings_sync", "A dry-run would let us preview request bodies.", "", "1.0.0", "https://github.com/acme/repo/")
	if p.Repo != "https://github.com/acme/repo" || strings.Join(p.Labels, ",") != "enhancement" {
		t.Errorf("formatMcpIssue() = %+v, want trimmed override repo and enhancement label", p)
	}
	if strings.Contains(p.Body, "**Tool:**") {
		t.Errorf("body = %q, want no Tool line when none was given", p.Body)
	}
}

func TestUnknownFailureCarriesReportIssueWithPrefilledURL(t *testing.T) {
	useConfig(t, nil)
	body := errorPayload("get_order", errors.New("undefined is not a function"), "1.5.0")
	if body.Error != "undefined is not a function" || body.ReportIssue == nil {
		t.Fatalf("errorPayload() = %+v, want the message and a reportIssue", body)
	}
	if !strings.Contains(body.ReportIssue.URL, "/issues/new?") || !strings.Contains(body.ReportIssue.URL, "get_order") {
		t.Errorf("reportIssue.url = %q, want a new-issue URL naming the tool", body.ReportIssue.URL)
	}
}

func TestExpectedFailureHasNoReportIssue(t *testing.T) {
	useConfig(t, nil)
	if body := errorPayload("clone_order", errors.New("unknown profile 'x'"), "1.5.0"); body.ReportIssue != nil {
		t.Errorf("errorPayload() reportIssue = %+v, want none for an expected failure", body.ReportIssue)
	}
}

func TestReportIssueIsSuppressedWhenFeedbackIsDisabled(t *testing.T) {
	useConfig(t, validConfig(map[string]any{"enabled": false, "repositoryUrl": "https://github.com/acme/repo"}))
	if body := errorPayload("doctor", errors.New("kaboom"), "1.5.0"); body.ReportIssue != nil {
		t.Errorf("errorPayload() reportIssue = %+v, want none when feedback.enabled is false", body.ReportIssue)
	}
}

func TestReportIssueTargetsConfiguredRepository(t *testing.T) {
	useConfig(t, validConfig(map[string]any{"repositoryUrl": "https://github.com/acme/repo"}))
	body := errorPayload("doctor", errors.New("kaboom"), "1.5.0")
	if body.ReportIssue == nil || !strings.HasPrefix(body.ReportIssue.URL, "https://github.com/acme/repo/issues/new?") {
		t.Errorf("errorPayload() = %+v, want an issue URL on the configured repo", body)
	}
}

func TestTransportFailuresAreOfUnknownOriginAndGetFlagged(t *testing.T) {
	cases := map[string]error{
		"connection refused": &url.Error{Op: "Get", URL: "https://be.example.com:443/x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}},
		"connection reset":   fmt.Errorf("copilot GET /x: %w", &net.OpError{Op: "read", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 443}, Err: errors.New("read: connection reset by peer")}),
		"timeout":            &url.Error{Op: "Post", URL: "https://x:443/", Err: context.DeadlineExceeded},
	}
	for name, err := range cases {
		if isExpectedFailure(err) {
			t.Errorf("isExpectedFailure(%s: %q) = true, want false", name, err)
		}
	}
	// Text alone (no typed chain) still reads as the upstream answering.
	if !isExpectedFailure(errors.New("login failed 401: forbidden")) {
		t.Error("an HTTP status in the message must stay quiet")
	}
}
