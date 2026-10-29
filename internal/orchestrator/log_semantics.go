package orchestrator

import (
	"regexp"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Semantic classification of a job's robot logs: level severity and free-text failure-language
// detection. A leaf file (no dependency on the digest) so uipath's refineJobLogs and DigestLogs
// can both use it.

var errorLevels = map[string]bool{"error": true, "fatal": true, "critical": true}

// failureTerms are words/phrases that suggest a failure even when the log's own level is benign.
var failureTerms = []string{
	"exceptions?",
	"errors?",
	"errored",
	"faults?",
	"faulted",
	"fail(?:s|ed|ing|ure)?",
	"crash(?:es|ed|ing)?",
	"abort(?:s|ed|ing)?",
	"terminat(?:e|ed|es|ing|ion)",
	"kill(?:s|ed)?",
	"halt(?:s|ed|ing)?",
	"panic(?:ked|king)?",
	"fatal",
	"critical",
	"severe",
	"unable to",
	"not able",
	"cannot",
	"can[’'`]?t",
	"could ?n[o’'`]?t",
	"did ?n[o’'`]?t",
	"was ?n[o’'`]?t able",
	"no response",
	"not found",
	"missing",
	"invalid",
	"unexpected",
	"unhandled",
	"illegal",
	"denied",
	"rejected",
	"refused",
	"unauthori[sz]ed",
	"forbidden",
	"expired",
	"access denied",
	"permission denied",
	"invalid credentials",
	"time(?:d)? ?out",
	"timeout",
	"unreachable",
	"disconnected",
	"connection (?:refused|reset|lost|closed|error)",
	"reset by peer",
	"retries exhausted",
	"max(?:imum)? retries",
	"gave up",
	"giving up",
	"stack ?trace",
	"traceback",
	"null ?reference",
	"null ?pointer",
	"out of memory",
	"stack overflow",
	"overflow",
	"deadlock",
	"segfault",
	"segmentation fault",
	"corrupt(?:s|ed|ion|ing)?",
	"broken",
	"bad request",
	"internal server error",
	"service unavailable",
	"bad gateway",
	"gateway timeout",
	"too many requests",
}

var failurePattern = regexp.MustCompile(`(?i)\b(?:` + strings.Join(failureTerms, "|") + `)\b`)

// failureScanWindow: free-text failure phrases front-load their meaning ("ABORT: ...", "Unable
// to find submit button", "the request timed out" — all comfortably under this). Embedded data
// blobs (a full result.json dump, HTML page titles, screenshot paths) can run many hundreds of
// characters and incidentally contain one of these generic words deep inside with no bearing on
// the actual outcome — bounding the scan avoids flagging those while still catching every real
// message.
const failureScanWindow = 200

// IsFailureLog reports whether a log is an error-level line or its message reads like a failure.
func IsFailureLog(l JobLog) bool {
	return errorLevels[strings.ToLower(l.Level)] ||
		failurePattern.MatchString(shared.SliceUTF16(l.Message, failureScanWindow))
}
