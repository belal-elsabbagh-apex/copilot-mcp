package apex

// Parser for Apex's per-job `job.log` artifact — the only log source (there is no RobotLogs API).
// The file interleaves the adapter's own lines, the bot's NDJSON log lines and raw Python crash
// output, so each line is classified and normalized to the shared JobLog row that the
// digest/filter code reads. Pure: no I/O.

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// LineKind classifies one job.log line.
type LineKind string

// The line kinds.
const (
	KindHeader        LineKind = "header"
	KindFooter        LineKind = "footer"
	KindAdapter       LineKind = "adapter"
	KindAdapterResult LineKind = "adapter-result"
	KindNDJSON        LineKind = "ndjson"
	KindTraceback     LineKind = "traceback"
	KindPlain         LineKind = "plain"
)

// LogLine is one parsed job.log row.
type LogLine struct {
	orchestrator.JobLog
	Kind   LineKind
	Fields map[string]any // the bot's NDJSON `fields` object, when it sent one
}

// JobLogFooter is the log's true last line; it carries the run's own verdict flags.
type JobLogFooter struct {
	ExitCode  *int // `None` in the log -> nil
	TimedOut  bool
	Cancelled bool
	Suspended bool
}

// AdapterResult is `[apex-adapter] result=<R> reason=<reason|-> exit=<n> outcomes=<csv|->`.
type AdapterResult struct {
	Result   string   `json:"result"`
	Reason   *string  `json:"reason"`
	Exit     *int     `json:"exit"`
	Outcomes []string `json:"outcomes"`
}

// ParsedJobLog is a whole job.log.
type ParsedJobLog struct {
	Rows          []LogLine
	Footer        *JobLogFooter
	AdapterResult *AdapterResult
}

var (
	headerRE    = regexp.MustCompile(`^=== apex job \S+ started\b`)
	footerRE    = regexp.MustCompile(`^=== apex job \S+ finished:(.*)===$`)
	resultRE    = regexp.MustCompile(`^\[apex-adapter\] result=(\S+) reason=(.*?) exit=(\S+) outcomes=(\S*)$`)
	tracebackRE = regexp.MustCompile(`^Traceback \(most recent call last\):`)
	exitCodeRE  = regexp.MustCompile(`\bexit_code=(-?\d+)\b`)
	intRE       = regexp.MustCompile(`^-?\d+$`)
	flagREs     = map[string]*regexp.Regexp{
		"timed_out": regexp.MustCompile(`\btimed_out=True\b`),
		"cancelled": regexp.MustCompile(`\bcancelled=True\b`),
		"suspended": regexp.MustCompile(`\bsuspended=True\b`),
	}
	levels = map[string]string{"INFO": "Info", "WARN": "Warn", "WARNING": "Warn", "ERROR": "Error", "DEBUG": "Debug"}
)

// levelOf: unknown levels survive verbatim (capitalized) rather than being guessed at.
func levelOf(raw any) string {
	s, ok := raw.(string)
	if !ok || s == "" {
		return "Info"
	}
	up := strings.ToUpper(s)
	if l, ok := levels[up]; ok {
		return l
	}
	r := []rune(up)
	return string(r[:1]) + strings.ToLower(string(r[1:]))
}

func footerOf(line string) *JobLogFooter {
	m := footerRE.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	body := m[1]
	f := &JobLogFooter{
		TimedOut:  flagREs["timed_out"].MatchString(body),
		Cancelled: flagREs["cancelled"].MatchString(body),
		Suspended: flagREs["suspended"].MatchString(body),
	}
	if e := exitCodeRE.FindStringSubmatch(body); e != nil {
		if n, err := strconv.Atoi(e[1]); err == nil {
			f.ExitCode = &n
		}
	}
	return f
}

func adapterResultOf(line string) *AdapterResult {
	m := resultRE.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	res := &AdapterResult{Result: m[1], Outcomes: []string{}}
	if m[2] != "" && m[2] != "-" {
		res.Reason = &m[2]
	}
	if intRE.MatchString(m[3]) {
		if n, err := strconv.Atoi(m[3]); err == nil {
			res.Exit = &n
		}
	}
	if m[4] != "-" {
		for o := range strings.SplitSeq(m[4], ",") {
			if o != "" {
				res.Outcomes = append(res.Outcomes, o)
			}
		}
	}
	return res
}

// ndjsonRow reads a bot NDJSON line: a JSON object with a string `message`. Anything else that
// merely starts with "{" (truncated, not JSON, a JSON dump without `message`) is a plain line.
func ndjsonRow(line string) (LogLine, bool) {
	if !strings.HasPrefix(line, "{") {
		return LogLine{}, false
	}
	obj, ok := shared.SafeJSONParse(line).(map[string]any)
	if !ok {
		return LogLine{}, false
	}
	msg, ok := obj["message"].(string)
	if !ok {
		return LogLine{}, false
	}
	stamp, _ := obj["timestamp"].(string)
	fields, _ := obj["fields"].(map[string]any)
	return LogLine{
		JobLog: orchestrator.JobLog{Level: levelOf(obj["level"]), Message: msg, TimeStamp: stamp},
		Kind:   KindNDJSON, Fields: fields,
	}, true
}

func textRow(kind LineKind, line, level string) LogLine {
	return LogLine{JobLog: orchestrator.JobLog{Level: level, Message: line}, Kind: kind}
}

// inheritTimestamps: only NDJSON lines carry a timestamp. Untimed lines inherit the previous
// row's, so a gap between two timed rows is never blamed on an untimed line between them; leading
// untimed rows (header, early adapter lines) take the first timestamp that follows. A log with no
// timestamp at all keeps "".
func inheritTimestamps(rows []LogLine) {
	last := ""
	for _, r := range rows {
		if r.TimeStamp != "" {
			last = r.TimeStamp
			break
		}
	}
	for i := range rows {
		if rows[i].TimeStamp != "" {
			last = rows[i].TimeStamp
		} else {
			rows[i].TimeStamp = last
		}
	}
}

func parseJobLog(text string) ParsedJobLog {
	out := ParsedJobLog{Rows: []LogLine{}}
	inTraceback := false
	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimRightFunc(raw, unicode.IsSpace)
		if line == "" {
			continue
		}
		// A crash is reported at Error so the digest and minLevel filters catch it. The block is
		// the Traceback line, its indented frames, then one unindented exception line.
		if inTraceback || tracebackRE.MatchString(line) {
			out.Rows = append(out.Rows, textRow(KindTraceback, line, "Error"))
			inTraceback = !inTraceback || unicode.IsSpace([]rune(line)[0])
			continue
		}
		if f := footerOf(line); f != nil {
			out.Footer = f
			out.Rows = append(out.Rows, textRow(KindFooter, line, "Info"))
			continue
		}
		if r := adapterResultOf(line); r != nil {
			out.AdapterResult = r
			out.Rows = append(out.Rows, textRow(KindAdapterResult, line, "Info"))
			continue
		}
		switch row, ok := ndjsonRow(line); {
		case headerRE.MatchString(line):
			out.Rows = append(out.Rows, textRow(KindHeader, line, "Info"))
		case strings.HasPrefix(line, "[apex-adapter]"):
			out.Rows = append(out.Rows, textRow(KindAdapter, line, "Info"))
		case ok:
			out.Rows = append(out.Rows, row)
		default:
			out.Rows = append(out.Rows, textRow(KindPlain, line, "Info"))
		}
	}
	inheritTimestamps(out.Rows)
	return out
}
