package uipath

import (
	"cmp"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
)

// Job OutputArguments normalization (ported from copilot-doctor src/outputSchema.ts). UiPath jobs
// surface their result as an `OutputArguments` JSON string, but the shape varies by automation.
// This normalizes every known shape behind one view so order-UID matching and field rendering work
// uniformly. To support a new shape: add an outputAdapter to adapters — matching + display both
// read the normalized view.

type outputAdapter struct {
	matches func(raw map[string]any) bool
	fields  func(raw map[string]any) orchestrator.Fields
}

// jobOutputOrder is the display priority of schema A's well-known fields.
var jobOutputOrder = []string{"out_OrderUid", "out_Result", "out_Account", "out_QueueItemReference", "out_AuthId"}

// txHiddenFields carry an auth JWT and internal routing data, so they are hidden.
var txHiddenFields = map[string]bool{"token": true, "callbackContext": true}

func specificContent(raw map[string]any) (map[string]any, bool) {
	item, _ := raw["transactionItem"].(map[string]any)
	sc, ok := item["SpecificContent"].(map[string]any)
	return sc, ok
}

// adapters, tried in order. Schema A: flat job output keyed by `out_`-prefixed fields (e.g.
// out_OrderUid). Schema B: a UiPath queue transaction item; order data lives under
// transactionItem.SpecificContent.
var adapters = []outputAdapter{
	{
		matches: func(raw map[string]any) bool {
			for k := range raw {
				if strings.HasPrefix(k, "out_") {
					return true
				}
			}
			return false
		},
		fields: func(raw map[string]any) orchestrator.Fields {
			out := orchestrator.Fields{}
			for k, v := range raw {
				if strings.HasPrefix(k, "out_") {
					out = append(out, orchestrator.Field{Key: k, Value: v})
				}
			}
			slices.SortFunc(out, func(a, b orchestrator.Field) int {
				ia, ib := slices.Index(jobOutputOrder, a.Key), slices.Index(jobOutputOrder, b.Key)
				switch {
				case ia >= 0 && ib >= 0:
					return ia - ib
				case ia >= 0:
					return -1
				case ib >= 0:
					return 1
				}
				return cmp.Compare(a.Key, b.Key)
			})
			return out
		},
	},
	{
		matches: func(raw map[string]any) bool { _, ok := specificContent(raw); return ok },
		fields: func(raw map[string]any) orchestrator.Fields {
			sc, _ := specificContent(raw)
			return sortedFields(sc, func(k string) bool { return !txHiddenFields[k] })
		},
	},
}

func sortedFields(m map[string]any, keep func(string) bool) orchestrator.Fields {
	out := orchestrator.Fields{}
	for k, v := range m {
		if keep(k) {
			out = append(out, orchestrator.Field{Key: k, Value: v})
		}
	}
	slices.SortFunc(out, func(a, b orchestrator.Field) int { return cmp.Compare(a.Key, b.Key) })
	return out
}

// normalizeOutputFields returns the display fields of a parsed OutputArguments object under the
// first adapter that recognizes it (every entry, sorted by key, for an unknown shape).
// ponytail: JS object key order is insertion order; Go maps keep none, so fields outside
// jobOutputOrder sort alphabetically. Upgrade by decoding with an order-preserving token reader if
// the exact upstream order ever matters.
func normalizeOutputFields(raw map[string]any) orchestrator.Fields {
	for _, a := range adapters {
		if a.matches(raw) {
			return a.fields(raw)
		}
	}
	return sortedFields(raw, func(string) bool { return true })
}
