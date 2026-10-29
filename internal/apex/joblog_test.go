package apex

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	logJob    = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	logHeader = "=== apex job " + logJob + " started (workflow=demo-portal runtime=python) ==="
	logFooter = "=== apex job " + logJob + " finished: exit_code=21 timed_out=False cancelled=False suspended=False duration=9s ==="
)

func nd(o map[string]any) string {
	base := map[string]any{"v": 1, "portal": "demo", "timestamp": "2026-10-08T00:40:00.000Z"}
	for k, v := range o {
		base[k] = v
	}
	b, _ := json.Marshal(base)
	return string(b)
}

// logFull is one of every line kind, in the order a failed run writes them.
var logFull = strings.Join([]string{
	logHeader,
	"[apex-adapter] claimed job, starting runtime",
	nd(map[string]any{"level": "INFO", "message": "login ok"}),
	nd(map[string]any{"level": "WARN", "message": "slow page", "timestamp": "2026-10-08T00:40:05.000Z", "fields": map[string]any{"ms": 5200}}),
	nd(map[string]any{"level": "ERROR", "message": "row not found", "timestamp": "2026-10-08T00:40:09.000Z"}),
	"a plain untyped line",
	"[trace] some other bracket line",
	"",
	"Traceback (most recent call last):",
	`  File "/app/main.py", line 10, in run`,
	`    raise ValueError("boom")`,
	"ValueError: boom",
	"[apex-adapter] result=FAILED reason=business_failure exit=21 outcomes=not_found,retry",
	logFooter,
}, "\n")

func kinds(rows []LogLine) []LineKind {
	out := make([]LineKind, len(rows))
	for i, r := range rows {
		out[i] = r.Kind
	}
	return out
}

func rowsOfKind(rows []LogLine, k LineKind) []LogLine {
	return slices.DeleteFunc(slices.Clone(rows), func(r LogLine) bool { return r.Kind != k })
}

func stamps(rows []LogLine) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.TimeStamp
	}
	return out
}

func TestParseJobLogClassifiesEveryLineSkippingBlankOnes(t *testing.T) {
	want := []LineKind{KindHeader, KindAdapter, KindNDJSON, KindNDJSON, KindNDJSON, KindPlain, KindPlain,
		KindTraceback, KindTraceback, KindTraceback, KindTraceback, KindAdapterResult, KindFooter}
	if got := kinds(parseJobLog(logFull).Rows); !slices.Equal(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}
}

func TestNDJSONRowsMapLevelsAndKeepMessageTimestampAndFields(t *testing.T) {
	nds := rowsOfKind(parseJobLog(logFull).Rows, KindNDJSON)
	info, warn, errRow := nds[0], nds[1], nds[2]
	if info.Level != "Info" || info.Message != "login ok" || info.Fields != nil {
		t.Errorf("info row = %+v", info)
	}
	if warn.Level != "Warn" || warn.Message != "slow page" || warn.TimeStamp != "2026-10-08T00:40:05.000Z" || warn.Fields["ms"] != float64(5200) {
		t.Errorf("warn row = %+v", warn)
	}
	if errRow.Level != "Error" || errRow.Message != "row not found" {
		t.Errorf("error row = %+v", errRow)
	}
}

func TestPlainLinesAreInfoAndKeepTheirText(t *testing.T) {
	var got []string
	for _, r := range rowsOfKind(parseJobLog(logFull).Rows, KindPlain) {
		got = append(got, r.Level+"|"+r.Message)
	}
	if want := []string{"Info|a plain untyped line", "Info|[trace] some other bracket line"}; !slices.Equal(got, want) {
		t.Errorf("plain rows = %v, want %v", got, want)
	}
}

func TestPythonTracebackIsErrorThroughItsExceptionLineAndNoFurther(t *testing.T) {
	rows := parseJobLog(logFull).Rows
	tb := rowsOfKind(rows, KindTraceback)
	for _, r := range tb {
		if r.Level != "Error" {
			t.Errorf("traceback row %q level = %s, want Error", r.Message, r.Level)
		}
	}
	if last := tb[len(tb)-1].Message; last != "ValueError: boom" {
		t.Errorf("last traceback row = %q, want the exception line", last)
	}
	if lvl := rowsOfKind(rows, KindAdapterResult)[0].Level; lvl != "Info" {
		t.Errorf("adapter-result level = %s, want Info (traceback must end at the exception line)", lvl)
	}
}

func TestLevelNormalization(t *testing.T) {
	level := func(l any) string {
		return parseJobLog(nd(map[string]any{"level": l, "message": "m"})).Rows[0].Level
	}
	for in, want := range map[any]string{"DEBUG": "Debug", "warning": "Warn", "error": "Error", "NOTICE": "Notice", 7: "Info"} {
		if got := level(in); got != want {
			t.Errorf("level(%v) = %q, want %q", in, got, want)
		}
	}
	if got := parseJobLog(nd(map[string]any{"message": "m"})).Rows[0].Level; got != "Info" {
		t.Errorf("absent level = %q, want Info", got)
	}
}

func TestUntimedLinesInheritThePreviousRowsTimestamp(t *testing.T) {
	text := strings.Join([]string{
		nd(map[string]any{"message": "a", "timestamp": "2026-10-08T00:40:00.000Z"}),
		"[apex-adapter] midway note",
		nd(map[string]any{"message": "b", "timestamp": "2026-10-08T00:41:00.000Z"}),
		logFooter,
	}, "\n")
	want := []string{"2026-10-08T00:40:00.000Z", "2026-10-08T00:40:00.000Z", "2026-10-08T00:41:00.000Z", "2026-10-08T00:41:00.000Z"}
	if got := stamps(parseJobLog(text).Rows); !slices.Equal(got, want) {
		t.Errorf("timestamps = %v, want %v", got, want)
	}
}

func TestLeadingUntimedRowsTakeTheFirstTimestampThatFollows(t *testing.T) {
	rows := parseJobLog(strings.Join([]string{logHeader, "[apex-adapter] boot", nd(map[string]any{"message": "a"})}, "\n")).Rows
	for i, s := range stamps(rows) {
		if s != "2026-10-08T00:40:00.000Z" {
			t.Errorf("row %d timestamp = %q, want the first timestamp that follows", i, s)
		}
	}
}

func TestLogWithNoTimestampsKeepsEmptyOnes(t *testing.T) {
	if got := stamps(parseJobLog(logHeader + "\nplain").Rows); !slices.Equal(got, []string{"", ""}) {
		t.Errorf("timestamps = %q, want empty", got)
	}
}

func TestFooterParsesExitCodeAndFlags(t *testing.T) {
	got := parseJobLog(logFull).Footer
	if got == nil || *got.ExitCode != 21 || got.TimedOut || got.Cancelled || got.Suspended {
		t.Errorf("footer = %+v, want exit 21 and no flags", got)
	}
	f := parseJobLog("=== apex job " + logJob + " finished: exit_code=None timed_out=True cancelled=True suspended=True ===").Footer
	if f == nil || f.ExitCode != nil || !f.TimedOut || !f.Cancelled || !f.Suspended {
		t.Errorf("footer = %+v, want null exit code and all flags", f)
	}
}

func TestMissingFooterIsNilAndRowsStillParse(t *testing.T) {
	p := parseJobLog(logHeader + "\n" + nd(map[string]any{"message": "a"}))
	if p.Footer != nil || len(p.Rows) != 2 {
		t.Errorf("parsed = %+v, want no footer and 2 rows", p)
	}
}

func TestAdapterResultParsesFieldsAndDashPlaceholders(t *testing.T) {
	want := &AdapterResult{Result: "FAILED", Reason: sp("business_failure"), Exit: new(21), Outcomes: []string{"not_found", "retry"}}
	if got := parseJobLog(logFull).AdapterResult; !reflect.DeepEqual(got, want) {
		t.Errorf("adapterResult = %+v, want %+v", got, want)
	}
	dash := parseJobLog("[apex-adapter] result=OK reason=- exit=- outcomes=-")
	if got := fmt.Sprint(jsonOf(t, dash.AdapterResult)); got != `{"result":"OK","reason":null,"exit":null,"outcomes":[]}` {
		t.Errorf("adapterResult = %s", got)
	}
	if dash.Rows[0].Kind != KindAdapterResult {
		t.Errorf("kind = %s, want adapter-result", dash.Rows[0].Kind)
	}
}

func TestBrokenNDJSONAndJSONWithoutAMessageArePlainLines(t *testing.T) {
	rows := parseJobLog(`{"v":1,"level":"ERROR","message":"cut off` + "\n" + `{"v":1,"level":"ERROR"}`).Rows
	for i, r := range rows {
		if r.Kind != KindPlain || r.Level != "Info" {
			t.Errorf("row %d = %s/%s, want plain/Info", i, r.Kind, r.Level)
		}
	}
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}

func TestNonObjectFieldsIsDropped(t *testing.T) {
	if f := parseJobLog(nd(map[string]any{"message": "m", "fields": "x"})).Rows[0].Fields; f != nil {
		t.Errorf("fields = %v, want dropped", f)
	}
}

func TestEmptyLogParsesToNothing(t *testing.T) {
	p := parseJobLog("")
	if len(p.Rows) != 0 || p.Rows == nil || p.Footer != nil || p.AdapterResult != nil {
		t.Errorf("parsed = %+v, want an empty, non-nil result", p)
	}
}

func TestCRLFLineEndings(t *testing.T) {
	p := parseJobLog(logHeader + "\r\n" + nd(map[string]any{"message": "a"}) + "\r\n" + logFooter + "\r\n")
	if want := []LineKind{KindHeader, KindNDJSON, KindFooter}; !slices.Equal(kinds(p.Rows), want) || *p.Footer.ExitCode != 21 {
		t.Errorf("kinds = %v footer = %+v", kinds(p.Rows), p.Footer)
	}
}
