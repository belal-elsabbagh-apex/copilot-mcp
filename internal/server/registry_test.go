package server

import (
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// expectedTools is the TS server's full tool list (src/server.test.ts EXPECTED at the v2.0.0 tag). The exact-names
// test below stays red until every tool set has landed; the annotation tests run over whatever is
// registered so they guard each set as it arrives.
var expectedTools = []string{
	"find_clone_candidates", "delete_preprod_order", "create_preprod_order", "submit_preprod_order",
	"build_queue_item", "find_order_queue_items", "pull_queue_item", "list_queue_items", "list_jobs",
	"get_job_logs", "build_faulted_job_issue", "find_stuck_orders", "diff_settings", "list_setting_sections",
	"get_settings", "plan_settings_sync", "apply_settings_sync", "get_order", "search_orders",
	"get_order_category_stats", "get_login_token", "list_clinics", "doctor", "list_queues", "list_processes",
	"list_triggers", "get_job", "get_job_artifacts", "add_queue_item", "delete_queue_item", "start_job",
	"stop_job", "build_mcp_issue",
}

// writeTools mutate state (everything else is read-only). apply_settings_sync writes additively to
// pre-prod only; the orchestrator writes are schema-restricted to pre_prod.
var writeTools = []string{
	"delete_preprod_order", "create_preprod_order", "submit_preprod_order", "apply_settings_sync",
	"add_queue_item", "delete_queue_item", "start_job", "stop_job",
}

// closedWorldTools touch no external service (pure/local); every other tool sets openWorldHint.
var closedWorldTools = []string{"list_setting_sections", "build_mcp_issue"}

func registeredTools(t *testing.T) []*mcp.Tool {
	t.Helper()
	cs, _ := connect(t, newTestServer(t))
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Tools
}

func TestRegistersExactlyTheExpectedTools(t *testing.T) {
	var got []string
	for _, tool := range registeredTools(t) {
		got = append(got, tool.Name)
	}
	slices.Sort(got)
	want := slices.Sorted(slices.Values(expectedTools))
	if !slices.Equal(got, want) {
		t.Errorf("registered tools differ from the TS server:\n missing: %v\n extra:   %v",
			slices.DeleteFunc(slices.Clone(want), func(n string) bool { return slices.Contains(got, n) }),
			slices.DeleteFunc(slices.Clone(got), func(n string) bool { return slices.Contains(want, n) }))
	}
}

func TestEveryToolDeclaresTitleDescriptionAndAllFourAnnotationHints(t *testing.T) {
	for _, tool := range registeredTools(t) {
		a := tool.Annotations
		if tool.Title == "" || len(tool.Description) <= 30 || a == nil {
			t.Errorf("%s: title=%q description len=%d annotations=%v, want all declared", tool.Name, tool.Title, len(tool.Description), a)
			continue
		}
		if a.DestructiveHint == nil || a.OpenWorldHint == nil {
			t.Errorf("%s: destructiveHint/openWorldHint unset, want all four hints present", tool.Name)
			continue
		}
		if want := !slices.Contains(closedWorldTools, tool.Name); *a.OpenWorldHint != want {
			t.Errorf("%s: openWorldHint = %v, want %v", tool.Name, *a.OpenWorldHint, want)
		}
		if want := !slices.Contains(writeTools, tool.Name); a.ReadOnlyHint != want {
			t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, a.ReadOnlyHint, want)
		}
	}
}

func TestReadOnlyToolsAreNonDestructiveAndIdempotent(t *testing.T) {
	for _, tool := range registeredTools(t) {
		a := tool.Annotations
		if !a.ReadOnlyHint {
			continue
		}
		if (a.DestructiveHint != nil && *a.DestructiveHint) || !a.IdempotentHint {
			t.Errorf("%s: read-only tool has destructiveHint=%v idempotentHint=%v, want false/true", tool.Name, a.DestructiveHint, a.IdempotentHint)
		}
	}
}

func TestEveryToolExposesAnObjectInputSchema(t *testing.T) {
	for _, tool := range registeredTools(t) {
		m, ok := tool.InputSchema.(map[string]any)
		if !ok || m["type"] != "object" {
			t.Errorf("%s: input schema = %v, want a JSON object schema", tool.Name, tool.InputSchema)
		}
	}
}
