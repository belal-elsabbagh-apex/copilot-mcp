package orchestrator

import (
	"strings"
	"testing"
)

func TestIsFailureLog(t *testing.T) {
	tests := []struct {
		name  string
		level string
		msg   string
		want  bool
	}{
		{"error level", "Error", "anything", true},
		{"failure language at info level", "Info", "connection refused", true},
		{"benign info", "Info", "all good, processed 3 items", false},
		// Models a robot logging a whole result.json as one Info line: the headline is benign but
		// the huge tail can incidentally contain a generic word like "failed".
		{"failure word deep in a long data blob", "Info",
			`[runner] result.json: { "result": "SUBMITTED", "note": "` + strings.Repeat("x", 250) + ` this step failed" }`, false},
		{"failure word within the scan window", "Info", "ABORT: " + strings.Repeat("x", 150), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFailureLog(JobLog{Level: tt.level, Message: tt.msg}); got != tt.want {
				t.Errorf("IsFailureLog = %v, want %v", got, tt.want)
			}
		})
	}
}
