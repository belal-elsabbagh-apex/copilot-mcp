package uipath

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// UiPath queue-item serializer: turns the canonical native-typed content from internal/queueitem
// into the AddQueueItem request body. UiPath's robot reads the array fields and callbackContext
// as JSON STRINGS, so those (and only those) are stringified.

var stringifiedFields = []string{"DescriptionOfService", "Diagnoses", "ServiceCode", "Treatments", "Units", "callbackContext"}

// now is the clock behind the item Reference suffix; tests pin it.
var now = time.Now

func serializeContent(content map[string]any) (map[string]any, error) {
	out := maps.Clone(content)
	for _, k := range stringifiedFields {
		v, ok := out[k]
		if !ok {
			continue
		}
		b, err := shared.MarshalJSON(v)
		if err != nil {
			return nil, fmt.Errorf("serialize %s: %w", k, err)
		}
		out[k] = string(b)
	}
	return out, nil
}

// requireQueueFields requires the queue-build-specific UiPath fields (only needed by
// build_queue_item).
func requireQueueFields(u config.UipathConfig) error {
	var missing []string
	for _, f := range []struct{ name, val string }{{"queueUrl", u.QueueURL}, {"addQueueItemPath", u.AddQueueItemPath}, {"folderPath", u.FolderPath}} {
		if f.val == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("uipath config is missing fields required for build_queue_item: %s. "+ //nolint:staticcheck // user-visible wording kept from the TS
		"Add them to your COPILOT_MCP_CONFIG uipath block (see copilot-mcp.config.example.json).", strings.Join(missing, ", "))
}

// buildQueueItemPayload serializes the canonical content into the AddQueueItem wire payload.
// BUILD ONLY: nothing is posted.
func buildQueueItemPayload(_ context.Context, args orchestrator.QueueItemPayloadArgs) (orchestrator.QueueItemPayload, error) {
	cfg, err := config.Uipath()
	if err != nil {
		return orchestrator.QueueItemPayload{}, err
	}
	if err := requireQueueFields(cfg); err != nil {
		return orchestrator.QueueItemPayload{}, err
	}
	content, err := serializeContent(args.Content)
	if err != nil {
		return orchestrator.QueueItemPayload{}, err
	}
	content["queueUrl"] = cfg.QueueURL
	serverURL, ok := cfg.ServerURLByEnv[args.Env]
	if !ok {
		serverURL = args.BeBase
	}
	content["serverURL"] = serverURL
	// Single enforcement point for the IsApproved=false rule.
	guarded, err := orchestrator.GuardQueueItemSafety(content, orchestrator.SafetyForce, orchestrator.QueueSafetyLimits{})
	if err != nil {
		return orchestrator.QueueItemPayload{}, err
	}
	return orchestrator.QueueItemPayload{
		Payload: map[string]any{"itemData": map[string]any{
			"Name":            args.QueueName,
			"Priority":        "Normal",
			"Reference":       fmt.Sprintf("%s-%d", args.OrderUID, now().Unix()),
			"SpecificContent": guarded.SpecificContent,
		}},
		Notes: []string{
			"To submit: POST payload.itemData as JSON to meta.postUrl with headers " +
				"'Authorization: Bearer <your uipath.bearer>' and " +
				fmt.Sprintf("'X-UIPATH-FolderPath: %s'. Never done automatically by this tool.", cfg.FolderPath),
		},
		Meta: map[string]any{"postUrl": base(cfg) + cfg.AddQueueItemPath, "folderPath": cfg.FolderPath},
	}, nil
}
