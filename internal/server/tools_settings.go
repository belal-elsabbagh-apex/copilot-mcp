package server

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot/settings"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerSettingsTools) }

type diffSettingsArgs struct {
	Profile          string   `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	Tags             []string `json:"tags,omitempty" jsonschema:"Tags to diff (e.g. ['orders','providers']); omit for all"`
	Sections         []string `json:"sections,omitempty" jsonschema:"Subset of section keys to diff (e.g. ['orders-outbound']); omit for all"`
	EMR              string   `json:"emr,omitempty" jsonschema:"EMR type (e.g. 'NEXTGEN') to also diff emrDetailsSettings; account-specific"`
	IncludeUnchanged bool     `json:"includeUnchanged,omitempty" jsonschema:"Include sections that are identical across envs (default: only differences)"`
}

type listSettingSectionsArgs struct {
	Tag      string   `json:"tag,omitempty" jsonschema:"Only list sections carrying this tag (e.g. 'orders')"`
	Sections []string `json:"sections,omitempty" jsonschema:"Only list these exact section keys (e.g. ['orders-outbound','locations'])"`
	EMR      string   `json:"emr,omitempty" jsonschema:"Include the opt-in emr-details section for this EMR type"`
}

type getSettingsArgs struct {
	Env        config.Env `json:"env" jsonschema:"Which env to read (required — never assumed)"`
	Profile    string     `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	Tags       []string   `json:"tags,omitempty" jsonschema:"Tags to fetch (e.g. ['orders']); omit for all"`
	Sections   []string   `json:"sections,omitempty" jsonschema:"Exact section keys to fetch; omit for all"`
	EMR        string     `json:"emr,omitempty" jsonschema:"EMR type (e.g. 'NEXTGEN') to include emrDetailsSettings"`
	Normalized bool       `json:"normalized,omitempty" jsonschema:"Strip UID/timestamp noise like diff_settings does (default true). Pass false for raw payloads with real UIDs/timestamps visible (e.g. to copy a UID for another call)."`
}

type planSettingsSyncArgs struct {
	Profile       string   `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	Tags          []string `json:"tags,omitempty" jsonschema:"Tags to plan for (e.g. ['orders'])"`
	Sections      []string `json:"sections,omitempty" jsonschema:"Exact section keys to plan for"`
	EMR           string   `json:"emr,omitempty" jsonschema:"EMR type to include emrDetailsSettings"`
	IncludeBodies bool     `json:"includeBodies,omitempty" jsonschema:"Include full request bodies per action (large; for spot-checking a scoped plan)"`
}

type applySettingsSyncArgs struct {
	Profile   string   `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	Tags      []string `json:"tags,omitempty" jsonschema:"Tags to re-plan (must match the plan call's scope)"`
	Sections  []string `json:"sections,omitempty" jsonschema:"Exact section keys to re-plan (must match the plan call's scope)"`
	EMR       string   `json:"emr,omitempty" jsonschema:"EMR type to include emrDetailsSettings"`
	ActionIDs []string `json:"actionIds,omitempty" jsonschema:"Ids from plan_settings_sync to execute (reviewed with the user)"`
	All       bool     `json:"all,omitempty" jsonschema:"Explicitly apply EVERY planned action (mutually exclusive with actionIds)"`
}

func diffSettings(ctx context.Context, req *mcp.CallToolRequest, in diffSettingsArgs) (any, error) {
	return settings.DiffSettings(ctx, settings.DiffSettingsOpts{
		Profile: in.Profile, Tags: in.Tags, Sections: in.Sections, EMR: in.EMR,
		IncludeUnchanged: in.IncludeUnchanged, OnProgress: stepProgress(ctx, req),
	})
}

func listSettingSections(_ context.Context, _ *mcp.CallToolRequest, in listSettingSectionsArgs) (any, error) {
	return settings.ListSettingSections(settings.ListSettingSectionsOpts{Tag: in.Tag, Sections: in.Sections, EMR: in.EMR})
}

func getSettings(ctx context.Context, req *mcp.CallToolRequest, in getSettingsArgs) (any, error) {
	return settings.GetSettings(ctx, settings.GetSettingsOpts{
		Env: in.Env, Profile: in.Profile, Tags: in.Tags, Sections: in.Sections, EMR: in.EMR,
		Normalized: in.Normalized, OnProgress: stepProgress(ctx, req),
	})
}

func planSettingsSync(ctx context.Context, req *mcp.CallToolRequest, in planSettingsSyncArgs) (any, error) {
	return settings.PlanSettingsSync(ctx, settings.PlanSettingsSyncOpts{
		Profile: in.Profile, Tags: in.Tags, Sections: in.Sections, EMR: in.EMR,
		IncludeBodies: in.IncludeBodies, OnProgress: stepProgress(ctx, req),
	})
}

func (s *Server) applySettingsSync(ctx context.Context, req *mcp.CallToolRequest, in applySettingsSyncArgs) (any, error) {
	return settings.ApplySettingsSync(ctx, settings.ApplySettingsSyncOpts{
		ApplyFilter: settings.ApplyFilter{ActionIDs: in.ActionIDs, All: in.All},
		Profile:     in.Profile, Tags: in.Tags, Sections: in.Sections, EMR: in.EMR,
		// Audit trail: surface every live write to the client log.
		OnWrite:    func(message string, data map[string]any) { s.mcpLog("warning", message, data) },
		OnProgress: stepProgress(ctx, req),
	})
}

func registerSettingsTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:  "diff_settings",
		Title: "Diff Copilot settings prod vs pre-prod",
		Description: "Compare an account's EHR Copilot settings between its PROD and PRE-PROD tenants. " +
			"Logs into both envs, fetches every settings section (file manager, outbound orders + types, " +
			"locations/groups/regions, payers, document routing/reviewing rules, eligibility, rendering " +
			"providers, and — crawled from each order type's specialities — specialties + referred " +
			"providers/facilities), and returns a normalized diff. READ-ONLY — never writes to either env. " +
			"Env-specific noise (UIDs, timestamps, dummy emails, CDN hosts) is stripped, and list " +
			"sections are matched by a semantic key (name) rather than UID, so only real drift shows. " +
			"Unchanged sections are omitted unless includeUnchanged=true. Scope with tags " +
			"(e.g. ['orders']) and/or sections (exact keys) — combined as AND; use list_setting_sections " +
			"to discover valid tags/keys. For a single env's raw values use get_settings; to " +
			"reconcile drift use plan_settings_sync then apply_settings_sync. Returns " +
			"{account, prodBase, preProdBase, sectionsCompared, sectionsWithDiffs, sections:[...]}.",
		Annotations: readOnlyHints(true),
	}, diffSettings,
		MinLen("profile", 1),
		Default("includeUnchanged", false),
	)

	addTool(s, &mcp.Tool{
		Name:  "list_setting_sections",
		Title: "List the settings sections the settings tools can scope to",
		Description: "List the settings sections diff_settings / get_settings / plan_settings_sync can scope to: " +
			"each section's key, label, tags, kind (object/list), whether it is 'derived' " +
			"(crawled from order types — heavier), and (for list sections) its matchKey — the field items " +
			"are matched/scoped by. Use this to drive FINE-GRAINED scoping: pass exact section keys via " +
			"the `sections` param instead of the broader `tags`. Narrow this listing with `tag` and/or " +
			"`sections`. READ-ONLY, no network (reads the static catalog). Returns {tags, sections:[...]}.",
		Annotations: readOnlyHints(false), // pure catalog read — no external service
	}, listSettingSections)

	addTool(s, &mcp.Tool{
		Name:  "get_settings",
		Title: "Get one env's Copilot settings",
		Description: "Fetch an account's EHR Copilot settings from ONE env (prod or pre_prod) — the single-env " +
			"counterpart to diff_settings. Logs into that env only and returns each selected section's " +
			"payload, stripped of env-specific noise (UIDs, timestamps, per-section ignore fields) by " +
			"default (list sections also report a row count). READ-ONLY — never writes. Scope with " +
			"tags and/or sections (exact keys) — combined as AND; use list_setting_sections " +
			"to discover them; crawled sections (specialties / referred-* / orders) are heavier. Pass " +
			"normalized=false for the raw payload with real UIDs/timestamps visible (e.g. to copy a UID " +
			"for another call). Returns " +
			"{account, env, base, sectionsFetched, sections:[{key,label,tags,kind,count?,data|error}]}.",
		Annotations: readOnlyHints(true),
	}, getSettings,
		Enum("env", config.Envs...),
		MinLen("profile", 1),
		Default("normalized", true),
	)

	addTool(s, &mcp.Tool{
		Name:  "plan_settings_sync",
		Title: "Plan an additive prod -> pre-prod settings sync",
		Description: "Compute — WITHOUT WRITING — the additive actions that would copy settings existing in PROD " +
			"but missing in PRE-PROD into pre-prod. READ-ONLY planning step; apply_settings_sync " +
			"executes a reviewed selection. Covers two domains: (1) the outbound order-type SPECIALTIES " +
			"domain (sections specialties / referred-providers / referred-facilities) — creates prod-only " +
			"specialties and merges prod-only facilities/providers into specialties that already exist in " +
			"pre-prod; and (2) the ORDERS domain (section orders) — creates prod-only orders under " +
			"matching order types (create-only). All references (payers, facilities, auth/referral " +
			"sub-categories) are remapped prod->pre by NAME; unmatched ones are dropped with a warning on " +
			"the action. Each action carries a stable id ('section:op:typeName:itemName') to pass to " +
			"apply_settings_sync after user review. Request bodies are summarized, not included " +
			"(includeBodies=true to inspect them — scope with sections first). Sections with no verified " +
			"write endpoint are reported under skippedSections. Also runs a READ-ONLY payer-link audit " +
			"(specialties domain only): a facility/provider that already exists (matched by name) in " +
			"both envs is never touched by the additive sync above, so payer-link drift on it — payers " +
			"linked in pre-prod but not prod, prod but not pre-prod, or a payerUid that resolves to no " +
			"payer at all — was previously invisible. Matched by payer NAME, never by payerUid (payer " +
			"uids are per-env and never expected to match). Findings are reported only, never turned " +
			"into a write action. Returns {account, prodBase, preProdBase, actionCount, " +
			"actions:[{id,op,itemKind,typeName,itemName,method,path,summary,warnings?}], skipped, " +
			"skippedSections, payerLinkFindings:[{typeName,specialityName,itemKind,itemName," +
			"extraInPreProd,missingInPreProd,orphanedProdPayerUids,orphanedPreProdPayerUids}]}.",
		Annotations: readOnlyHints(true), // planning only — apply_settings_sync executes
	}, planSettingsSync,
		MinLen("profile", 1),
		Default("includeBodies", false),
	)

	addTool(s, &mcp.Tool{
		Name:  "apply_settings_sync",
		Title: "Apply planned additive settings sync actions to pre-prod",
		Description: "Execute additive prod -> pre-prod settings sync actions. PRE-PROD ONLY; additive only — " +
			"never overwrites or deletes existing pre-prod settings. SAFETY: it never accepts request " +
			"bodies — it RE-PLANS server-side (same computation and scoping args as plan_settings_sync; " +
			"use the SAME tags/sections scope) and executes only the planned actions you select: pass " +
			"actionIds (from plan_settings_sync, after reviewing with the user) OR all=true to apply " +
			"every planned action — exactly one of the two is required; calls with neither (or both) are " +
			"rejected. If pre-prod changed since planning, a stale id matches nothing and is reported " +
			"under unmatchedIds instead of executing. References are remapped prod->pre by name; " +
			"unmatched refs are dropped with a warning. Every write is logged to the client (audit " +
			"trail). Returns {account, prodBase, preProdBase, plannedCount, executed:[{id,op,itemKind," +
			"typeName,itemName,method,path,status,ok,warnings?}], notSelected, unmatchedIds, skipped, " +
			"skippedSections}.",
		// writes to pre-prod, but ADDITIVE ONLY (never overwrites/deletes) and idempotent: re-applying
		// once names match is a no-op (nothing left to plan).
		Annotations: writeHints(false, true),
	}, s.applySettingsSync,
		MinLen("profile", 1),
		OnProp("actionIds", func(sc *jsonschema.Schema) { sc.Items.MinLength = new(1) }),
	)
}
