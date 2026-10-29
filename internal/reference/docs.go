package reference

import (
	"embed"
	"encoding/json"
)

//go:embed data/*.json
var data embed.FS

// The doc blobs are embedded as JSON (not rebuilt as Go maps) so key order — which Go maps cannot
// keep — matches the TS constants the resources used to serve. NOTE: marshal them with
// json.Encoder.SetEscapeHTML(false); the default escapes "<ASK-USER>" to \u003c, unlike JSON.stringify.
var (
	// QueueItemSchema documents the base SpecificContent fields (ported from
	// packages/core/src/schema.ts baseConfigSchema). Documentation only — portals extend it.
	QueueItemSchema = mustJSON("queue_item_schema.json")
	// ResultContract is the result.json contract every portal .cjs writes (from the
	// submissions-contract skill).
	ResultContract = mustJSON("result_contract.json")
	// OrderLifecycle documents the statuses the mirror walks through and the error codes it tolerates.
	OrderLifecycle = mustJSON("order_lifecycle.json")
	// SafetyRules are the invariants that keep test/dev activity from touching prod.
	SafetyRules = mustJSON("safety_rules.json")
	// ConfigGuide is everything an agent needs to write a working config from scratch when the
	// server starts without one. Mirrors copilot-mcp.config.example.json + config.go's schema —
	// keep the three in sync. Placeholders only; never put real values here.
	ConfigGuide = mustJSON("config_guide.json")
)

func mustJSON(name string) json.RawMessage {
	b, err := data.ReadFile("data/" + name)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(b)
}
