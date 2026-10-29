package settings

import (
	"fmt"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Action ids + apply selection (pure). Generic over any SyncAction — no section-specific logic
// lives here.

// actionID is the stable, human-readable id for one planned action, derived from its semantic
// fields — stable across plan runs as long as the underlying drift is the same. Colons inside
// names are fine: ids are opaque match tokens, never parsed back apart.
func actionID(a SyncAction) string {
	return fmt.Sprintf("%s:%s:%s:%s", a.Section, a.Op, a.TypeName, a.ItemName)
}

// assignActionIDs assigns ids in plan order. Two actions with identical semantic fields
// (duplicate item names under one type) get a deterministic "#2"/"#3" suffix so every id stays
// unique.
func assignActionIDs(actions []SyncAction) []PlannedSyncAction {
	seen := map[string]int{}
	out := make([]PlannedSyncAction, len(actions))
	for i, a := range actions {
		base := actionID(a)
		seen[base]++
		id := base
		if n := seen[base]; n > 1 {
			id = fmt.Sprintf("%s#%d", base, n)
		}
		out[i] = PlannedSyncAction{SyncAction: a, ID: id}
	}
	return out
}

// assertApplyFilter enforces the apply selection: exactly ONE of actionIds / all=true is required
// — there is deliberately no default "apply everything" (breadth must be an explicit input).
func assertApplyFilter(f ApplyFilter) error {
	if f.All == (len(f.ActionIDs) > 0) {
		return shared.Expectedf("pass exactly one of actionIds (from plan_settings_sync, reviewed with the user) or all=true — refusing to apply without an explicit selection")
	}
	return nil
}

// selectPlannedActions splits the planned actions into the selected subset and the rest; ids that
// match no planned action are returned so the caller can surface state drift since planning.
func selectPlannedActions(actions []PlannedSyncAction, f ApplyFilter) (selected, notSelected []PlannedSyncAction, unmatchedIDs []string, err error) {
	if err := assertApplyFilter(f); err != nil {
		return nil, nil, nil, err
	}
	unmatchedIDs = []string{}
	if f.All {
		return append([]PlannedSyncAction{}, actions...), nil, unmatchedIDs, nil
	}
	want := map[string]bool{}
	for _, id := range f.ActionIDs {
		want[id] = true
	}
	have := map[string]bool{}
	for _, a := range actions {
		have[a.ID] = true
		if want[a.ID] {
			selected = append(selected, a)
		} else {
			notSelected = append(notSelected, a)
		}
	}
	for _, id := range uniq(f.ActionIDs) {
		if !have[id] {
			unmatchedIDs = append(unmatchedIDs, id)
		}
	}
	return selected, notSelected, unmatchedIDs, nil
}

// summarizeActionBody is a one-line description of an action's request body so the plan output
// can omit the (potentially large) body itself.
func summarizeActionBody(a SyncAction) string {
	count := func(field string) int { return len(asArray(a.Body[field])) }
	if a.ItemKind == "speciality" {
		facs, provs := count("referredFacilities"), count("referredProviders")
		if a.Op == "create" {
			return fmt.Sprintf("create speciality '%s' (%d facilities, %d providers)", a.ItemName, facs, provs)
		}
		return fmt.Sprintf("merge speciality '%s' -> %d facilities, %d providers (existing + prod-only additions)", a.ItemName, facs, provs)
	}
	return fmt.Sprintf("create order '%s' (%d CPTs, %d facilities, %d auth + %d referral sub-categories)",
		a.ItemName, count("CPTCodes"), count("facilitiesUids"), count("authSubCategoryUids"), count("referralSubCategoryUids"))
}

// toActionSummary is the plan-output shape for one action: everything needed to review + select
// it, with the request body summarized (includeBodies=true for spot-checking a scoped plan).
func toActionSummary(a PlannedSyncAction, includeBodies bool) PlannedActionSummary {
	s := PlannedActionSummary{
		ID: a.ID, Section: a.Section, Op: a.Op, ItemKind: a.ItemKind, TypeName: a.TypeName, ItemName: a.ItemName,
		Method: a.Method, Path: a.Path, Summary: summarizeActionBody(a.SyncAction), Warnings: a.Warnings,
	}
	if includeBodies {
		s.Body = a.Body
	}
	return s
}
