package orchestrator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Backend-neutral fault helpers: the dedupe signature and the GitHub issue payload shape shared
// by every orchestrator's build_faulted_job_issue. The `<!-- fault-signature -->` marker in an
// issue body and the search query that finds it MUST stay in lockstep, so both strings are built
// here, once.

// DefaultRepo is the default target repo (the RPA automation project). Override per call.
const DefaultRepo = "Apex-Medical-AI-Inc/RPAPlaywright"

// FaultedJobIssue is the ready-to-post GitHub issue payload (nothing is posted by this server).
type FaultedJobIssue struct {
	Repo              string   `json:"repo"`
	Title             string   `json:"title"`
	Body              string   `json:"body"`
	Labels            []string `json:"labels"`
	FaultSignature    string   `json:"faultSignature"`    // stable, normalized error — the dedupe key
	SearchQuery       string   `json:"searchQuery"`       // GitHub query to find an existing issue for this fault
	RecurrenceComment string   `json:"recurrenceComment"` // post this on the existing issue when one is found
}

// FirstLine is the first line of s, trimmed and capped at 120 characters.
func FirstLine(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	return shared.SliceUTF16(jsTrim(first), 120)
}

var (
	guidRE      = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	isoTSRE     = regexp.MustCompile(`(?i)\d{4}-\d{2}-\d{2}[t ]\d{2}:\d{2}:\d{2}(?:\.\d+)?z?`)
	longDigitRE = regexp.MustCompile(`\d{4,}`)
)

// signatureCap bounds a normalized signature.
const signatureCap = 160

// NormalizeError collapses a raw error into a stable signature so the SAME recurring fault (a
// different job Key / ids / timestamps each run) maps to one issue. Strips GUIDs, ISO timestamps
// and long digit runs, drops quotes/backticks (they break GitHub search phrases), lowercases,
// collapses whitespace and truncates.
func NormalizeError(msg string) string {
	s := jsLower(msg)
	s = guidRE.ReplaceAllString(s, "<id>")
	s = isoTSRE.ReplaceAllString(s, "<ts>")
	s = longDigitRE.ReplaceAllString(s, "<n>")
	s = strings.NewReplacer(`"`, "", "`", "").Replace(s)
	s = jsSpaceRE.ReplaceAllString(s, " ")
	return shared.SliceUTF16(jsTrim(s), signatureCap)
}

// SignatureMarker is the hidden marker appended to an issue body; FaultSearchQuery finds it by
// its phrase.
func SignatureMarker(signature string) string {
	return fmt.Sprintf("<!-- fault-signature: %s -->", signature)
}

// FaultSearchQuery is the GitHub search query that finds the open issue carrying signature.
func FaultSearchQuery(repo, signature string) string {
	return fmt.Sprintf(`repo:%s is:issue is:open in:body "fault-signature: %s"`, repo, signature)
}
