package server

func settingsPrompts() []promptDef {
	return []promptDef{
		{
			name: "reconcile-settings", title: "Reconcile account settings (prod -> pre-prod)",
			desc: "Compare an account's settings between prod and pre-prod, then additively add the items prod has but pre-prod is missing.",
			args: []promptArg{profileArg()},
			text: reconcileSettingsText,
		},
		{
			name: "inspect-settings", title: "Inspect one env's account settings",
			desc: "Fetch and summarize an account's settings from a single env (prod or pre_prod), optionally scoped to a tag.",
			args: []promptArg{
				profileArg(),
				{name: "env", desc: "Env to inspect (prod | pre_prod)", complete: envNames},
				{name: "tag", desc: "Settings tag to focus on (e.g. 'orders')"},
			},
			text: inspectSettingsText,
		},
		{
			name: "send-mcp-feedback", title: "File feedback or a bug on this MCP server",
			desc: "Turn the user's report about copilot-mcp itself — a bug or general feedback (idea, " +
				"friction, docs gap) — into a GitHub issue on the server's feedback repo.",
			args: []promptArg{
				{name: "summary", desc: "One-line summary of the feedback or bug", required: true, minLen: 1},
				{name: "kind", desc: "bug | feedback (default: feedback)", complete: func() []string { return []string{"bug", "feedback"} }},
			},
			text: sendFeedbackText,
		},
	}
}

func reconcileSettingsText(a map[string]string) string {
	profile := a["profile"]
	return `Reconcile account settings between prod and pre-prod for profile="` + profile + `".` + "\n" +
		"1. Call list_setting_sections to see which sections/tags can be compared.\n" +
		`2. Call diff_settings (profile="` + profile + `") for the relevant tags to find drift, focusing on each section's onlyInProd items (present in prod, missing in pre-prod).` + "\n" +
		`3. Call plan_settings_sync (profile="` + profile + `", same tags/sections scope) to compute the additive actions. It is READ-ONLY and returns each action with a stable id, a one-line summary, and warnings about references dropped for having no pre-prod match by name.` + "\n" +
		"4. Present the planned actions (id, op, type, item, warnings) to the user and ask which to apply. Call out warnings — dropped payer/facility/sub-category links mean the created item will be missing those references.\n" +
		`5. Only after the user explicitly approves, call apply_settings_sync (profile="` + profile + `", same tags/sections scope) with actionIds set to exactly the approved ids — or all=true only if the user explicitly approved everything. It re-plans server-side, writes ADDITIVELY to PRE-PROD only, and never overwrites or deletes. Report executed vs notSelected vs unmatchedIds (an unmatched id means state changed since planning — re-plan and re-review).` + "\n" +
		"Tip: if applying both specialties and orders, apply the specialties/referred-* actions first, then RE-PLAN the orders section — order creates resolve facility references by name against current pre-prod state, so newly created facilities only resolve on a fresh plan.\n" +
		"Never apply without the user reviewing the plan first."
}

func inspectSettingsText(a map[string]string) string {
	profile, env, tag := a["profile"], a["env"], a["tag"]
	envArg := when(env != "", `, env="`+env+`"`)
	tagArg := when(tag != "", `, tags=["`+tag+`"]`)
	envReminder := when(env == "", " get_settings requires an env (prod or pre_prod) — confirm with the user which env to inspect before calling it.")
	return `Inspect Copilot settings for profile="` + profile + `"` + when(env != "", " in "+env) + when(tag != "", " (tag: "+tag+")") + ". Everything here is READ-ONLY.\n" +
		"1. Call list_setting_sections" + when(tag != "", ` with tag="`+tag+`"`) + " to see the available sections; note which are 'derived' (crawled from order types — heavier to fetch).\n" +
		`2. Call get_settings (profile="` + profile + `"` + envArg + tagArg + ") to fetch the sections from that one env.\n" +
		"3. Summarize per section: row counts for list sections, the key fields for object sections, and surface any per-section errors." + envReminder
}

func sendFeedbackText(a map[string]string) string {
	kind, what := "feedback", "feedback"
	if a["kind"] == "bug" {
		kind, what = "bug", "a bug report"
	}
	return "File " + what + ` about the copilot-mcp server itself: "` + a["summary"] + `".` + "\n" +
		"1. Gather the substance from the user/conversation: what happened or what they want changed, expected vs actual, and the related tool name if any. Never include credentials or PHI.\n" +
		`2. Call build_mcp_issue with kind="` + kind + `", a short title, and those details. It returns {repo, title, body, labels, url} and posts NOTHING.` + "\n" +
		"3. If a GitHub MCP server (or gh) with access to that repo is connected, search for an existing open issue on the same topic first — comment there instead of duplicating — else create the issue from the payload's title/body/labels and report its number/URL.\n" +
		"4. If no GitHub tooling is available, give the user the prefilled `url` to open (filing requires their GitHub account to have access to the repo).\n" +
		"Only write to GitHub — take no Copilot/UiPath action."
}
