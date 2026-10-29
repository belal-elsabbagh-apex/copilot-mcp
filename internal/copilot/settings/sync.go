package settings

// Settings sync (plan/apply) — the write-side counterpart to DiffSettings. ADDITIVELY copy
// settings that exist in PROD but are missing in PRE-PROD into pre-prod. Additive only — never
// overwrites or deletes existing pre-prod settings; pre-prod only. Split into two operations:
//   - PlanSettingsSync:  read-only — compute the planned actions, each with a stable id.
//   - ApplySettingsSync: re-plans server-side (never accepts bodies from the caller) and executes
//     only the actions the caller selected by id / all=true.
//
// Section dispatch is driven by each catalog section's own Sync (a SectionSyncer) — a section with
// no sync capability is reported under skippedSections automatically. Adding a new syncable domain
// means writing its SectionSyncer and attaching it to the relevant section(s); this file needs no
// changes.

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// syncScope is what plan and apply both take: apply re-runs the exact plan computation rather
// than accepting bodies from the caller.
type syncScope struct {
	profile    string
	sections   []string
	tags       []string
	emr        string
	onProgress shared.StepProgress
}

// syncPlan is the crawled + planned state shared by plan and apply.
type syncPlan struct {
	account         string
	prodBase        string
	preProdBase     string
	actions         []PlannedSyncAction
	skipped         []SyncSkip
	skippedSections []SkippedSection
	findings        []PayerLinkFinding
	pre             copilot.Client // logged-in pre-prod client; nil when nothing syncable was selected (no login done)
}

func buildSyncPlan(ctx context.Context, scope syncScope) (syncPlan, error) {
	// Validate + resolve the selection up front (unknown tag/section fails fast, no login).
	chosen, err := selectSections(scope.sections, scope.tags, scope.emr)
	if err != nil {
		return syncPlan{}, err
	}

	// Collect the distinct syncers referenced by the chosen sections — several sections can share
	// one syncer (e.g. specialties/referred-providers/referred-facilities all point at the same
	// specialities syncer), so it only runs once no matter how many of them are chosen.
	var syncers []SectionSyncer
	skippedSections := []SkippedSection{}
	for _, s := range chosen {
		if s.Sync == nil {
			skippedSections = append(skippedSections, SkippedSection{Key: s.Key, Reason: "no write mapping yet — additive sync not implemented"})
			continue
		}
		if !hasDomain(syncers, s.Sync.Domain()) {
			syncers = append(syncers, s.Sync)
		}
	}

	prodAuth, err := config.ResolveAuth(scope.profile, config.Prod)
	if err != nil {
		return syncPlan{}, err
	}
	preAuth, err := config.ResolveAuth(scope.profile, config.PreProd)
	if err != nil {
		return syncPlan{}, err
	}
	plan := syncPlan{
		account: account(scope.profile), prodBase: prodAuth.BE, preProdBase: preAuth.BE,
		actions: []PlannedSyncAction{}, skipped: []SyncSkip{}, skippedSections: skippedSections,
		findings: []PayerLinkFinding{},
	}
	if len(syncers) == 0 {
		return plan, nil // nothing syncable selected — report skipped only
	}

	prod, pre, err := connectPair(ctx, scope.profile)
	if err != nil {
		return syncPlan{}, err
	}
	results, err := runSyncers(ctx, prod, pre, syncers, scope.onProgress)
	if err != nil {
		return syncPlan{}, err
	}
	var actions []SyncAction
	for _, r := range results {
		actions = append(actions, r.Actions...)
		plan.skipped = append(plan.skipped, r.Skipped...)
		plan.findings = append(plan.findings, r.PayerLinkFindings...)
	}
	plan.actions = assignActionIDs(actions)
	plan.pre = pre
	return plan, nil
}

func hasDomain(syncers []SectionSyncer, domain string) bool {
	for _, s := range syncers {
		if s.Domain() == domain {
			return true
		}
	}
	return false
}

// runSyncers crawls every syncer's domain and the payer maps concurrently (the crawls do not
// depend on the payer maps; only the pure planning does), then plans each domain as soon as both
// its crawl and the payer maps have landed. Results are in syncer order. Reads are bounded by the
// two Sources' semaphores.
func runSyncers(ctx context.Context, prodClient, preClient copilot.Client, syncers []SectionSyncer, onProgress shared.StepProgress) ([]SyncPlanResult, error) {
	prod, pre := NewSource(prodClient), NewSource(preClient)

	var payers PayerMaps
	payersReady := make(chan struct{})
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		payers, err = buildPayerMaps(gctx, prod, pre)
		if err == nil {
			close(payersReady)
		}
		return err
	})

	// A syncer's crawl is a full cross-env crawl of its domain (minutes for specialities) —
	// report each one as it lands so a multi-domain plan isn't a silent wait.
	results := make([]SyncPlanResult, len(syncers))
	var mu sync.Mutex
	planned := 0
	for i, syncer := range syncers {
		g.Go(func() error {
			planner, err := syncer.Crawl(gctx, prod, pre)
			if err != nil {
				return err
			}
			select {
			case <-payersReady:
			case <-gctx.Done():
				return gctx.Err()
			}
			results[i] = planner(payers)
			mu.Lock()
			defer mu.Unlock()
			planned++
			onProgress.Step(planned, len(syncers), fmt.Sprintf("planned %s (%d action(s))", syncer.Domain(), len(results[i].Actions)))
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

// PlanSettingsSync computes — without writing — the additive actions that would sync prod ->
// pre-prod for the selected sections. Read-only; ApplySettingsSync executes a reviewed selection.
func PlanSettingsSync(ctx context.Context, opts PlanSettingsSyncOpts) (PlanSettingsSyncResult, error) {
	plan, err := buildSyncPlan(ctx, syncScope{opts.Profile, opts.Sections, opts.Tags, opts.EMR, opts.OnProgress})
	if err != nil {
		return PlanSettingsSyncResult{}, err
	}
	actions := make([]PlannedActionSummary, len(plan.actions))
	for i, a := range plan.actions {
		actions[i] = toActionSummary(a, opts.IncludeBodies)
	}
	return PlanSettingsSyncResult{
		Account: plan.account, ProdBase: plan.prodBase, PreProdBase: plan.preProdBase,
		ActionCount: len(plan.actions), Actions: actions, Skipped: plan.skipped,
		SkippedSections: plan.skippedSections, PayerLinkFindings: plan.findings,
	}, nil
}

// ApplySettingsSync re-plans server-side (same computation + scoping args as PlanSettingsSync —
// bodies are never accepted from the caller) and executes only the selected actions against
// PRE-PROD. Additive only. Requires ActionIDs XOR All=true; a stale id (state drifted since
// planning) matches nothing and lands in UnmatchedIDs instead of executing something unreviewed.
func ApplySettingsSync(ctx context.Context, opts ApplySettingsSyncOpts) (ApplySettingsSyncResult, error) {
	// Reject a missing/ambiguous selection BEFORE any login so the bad call is free.
	if err := assertApplyFilter(opts.ApplyFilter); err != nil {
		return ApplySettingsSyncResult{}, err
	}
	plan, err := buildSyncPlan(ctx, syncScope{opts.Profile, opts.Sections, opts.Tags, opts.EMR, opts.OnProgress})
	if err != nil {
		return ApplySettingsSyncResult{}, err
	}
	selected, notSelected, unmatchedIDs, err := selectPlannedActions(plan.actions, opts.ApplyFilter)
	if err != nil {
		return ApplySettingsSyncResult{}, err
	}
	executed, err := executeActions(ctx, plan.pre, selected, opts)
	if err != nil {
		return ApplySettingsSyncResult{}, err
	}
	skipped := make([]NotSelectedAction, len(notSelected))
	for i, a := range notSelected {
		skipped[i] = NotSelectedAction{ID: a.ID, Op: a.Op, TypeName: a.TypeName, ItemName: a.ItemName}
	}
	return ApplySettingsSyncResult{
		Account: plan.account, ProdBase: plan.prodBase, PreProdBase: plan.preProdBase,
		PlannedCount: len(plan.actions), Executed: executed, NotSelected: skipped, UnmatchedIDs: unmatchedIDs,
		Skipped: plan.skipped, SkippedSections: plan.skippedSections,
	}, nil
}

// executeActions writes the selected actions to PRE-PROD, one at a time and in plan order — the
// writes are the audit trail, so they stay sequential. Additive: POST new specialities/orders / PUT
// existing-plus-additions (specialities merge only).
func executeActions(ctx context.Context, pre copilot.Client, selected []PlannedSyncAction, opts ApplySettingsSyncOpts) ([]ExecutedAction, error) {
	executed := []ExecutedAction{}
	if len(selected) == 0 {
		return executed, nil
	}
	if err := copilot.AssertPreProdClient(pre, "apply_settings_sync"); err != nil {
		return nil, err
	}
	for i, a := range selected {
		r, err := pre.Req(ctx, a.Method, a.Path, copilot.ReqBody{JSON: a.Body})
		if err != nil {
			return nil, err
		}
		opts.OnProgress.Step(i+1, len(selected), fmt.Sprintf("%s %s '%s'", a.Op, a.ItemKind, a.ItemName))
		ok := r.Status < 400
		if opts.OnWrite != nil {
			opts.OnWrite(
				fmt.Sprintf("apply_settings_sync %s %s '%s' (type '%s')", a.Op, a.ItemKind, a.ItemName, a.TypeName),
				map[string]any{"id": a.ID, "method": a.Method, "path": a.Path, "status": r.Status, "ok": ok},
			)
		}
		executed = append(executed, ExecutedAction{
			ID: a.ID, Op: a.Op, ItemKind: a.ItemKind, TypeName: a.TypeName, ItemName: a.ItemName,
			Method: a.Method, Path: a.Path, Status: r.Status, OK: ok, Warnings: a.Warnings,
		})
	}
	return executed, nil
}
