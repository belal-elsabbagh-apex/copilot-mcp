package server

import (
	"strings"
)

// when returns s if cond holds, else "" — the JS `${cond ? s : ""}` of the prompt templates.
func when(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

func orderPrompts() []promptDef {
	return []promptDef{
		{
			name: "diagnose-order", title: "Diagnose a Copilot order",
			desc: "Root-cause a failing or stuck order: fetch its detail, trace its UiPath job, and read the logs.",
			args: []promptArg{
				{name: "orderUid", desc: "The orderUid to diagnose", required: true, minLen: 8},
				{name: "env", desc: "Env the order/job lives in (prod | pre_prod)", complete: envNames},
			},
			text: diagnoseOrderText,
		},
		{
			name: "clone-and-verify-order", title: "Clone a prod order to pre-prod and verify",
			desc: "Pick a cloneable prod order, read it, mint an equivalent pre-prod order, and — if a " +
				"reference doesn't resolve — diagnose via the settings tools instead of guessing.",
			args: []promptArg{
				{name: "uid", desc: "A specific prod orderUid to clone; omit to pick one"},
				profileArg(),
			},
			text: cloneAndVerifyText,
		},
		{
			name: "triage-stuck-orders", title: "Triage stuck orders",
			desc: "Find orders sitting in a non-terminal status and analyze each to decide remediation.",
			args: []promptArg{profileArg()},
			text: triageStuckText,
		},
		{
			name: "report-faulted-jobs", title: "Report faulted orchestrator jobs as GitHub issues",
			desc: "Find faulted jobs on one orchestrator (uipath or apex; prod by default) and file each as a GitHub issue on the RPA repo, " +
				"commenting on an existing issue when the same fault is already filed. Requires a connected GitHub MCP server.",
			args: []promptArg{
				{name: "orchestrator", desc: "Orchestrator to scan (uipath | apex) (required)", required: true, minLen: 1, complete: orchestratorNames},
				{name: "env", desc: "Env to scan (prod | pre_prod); defaults to prod", complete: envNames},
				{name: "since", desc: "ISO lower bound on job creation time (e.g. last 24h)"},
				{name: "top", desc: "Max recent jobs to scan (default 50)"},
				{name: "repo", desc: "Target repo owner/name (default Apex-Medical-AI-Inc/RPAPlaywright)"},
			},
			text: reportFaultedText,
		},
	}
}

func diagnoseOrderText(a map[string]string) string {
	orderUID, env := a["orderUid"], a["env"]
	envArg := when(env != "", `, env="`+env+`"`)
	envReminder := when(env == "", " Both get_order and find_order_queue_items require an env (prod or pre_prod) — confirm with the user which env the order is in before calling them.")
	return "Diagnose Copilot order " + orderUID + when(env != "", " (env: "+env+")") + ". Work through it step by step:\n" +
		"1. Call get_order with orderUid=\"" + orderUID + "\"" + envArg + " to read its current normalized state; note its creationDate.\n" +
		"2. Call find_order_queue_items with orderUid=\"" + orderUID + "\"" + envArg + " and since=<that creationDate> — omit " +
		"orchestrator to search every orchestrator — to find the queue item(s) and their jobIds (keyed per " +
		"orchestrator). If count is 0 and searchErrors is absent, the order never reached any orchestrator — report and " +
		"stop. If searchErrors names an orchestrator, that search failed: report it and retry rather than concluding the " +
		"order never arrived. If items exist but an orchestrator's jobIds list is empty, the order is queued and not yet " +
		"picked up by a worker — report the item statuses and stop.\n" +
		"3. For each orchestrator that has jobIds, call get_job with orchestrator=<that orchestrator>, env" + when(env != "", `="`+env+`"`) + ", " +
		"jobIds=<its ids from step 2> and includeLogDigest=true — ONE call per orchestrator covering the original run " +
		"and every retry. Read state, fault, details, logDigest per job.\n" +
		"4. Derive per-job verdict yourself: job.details.output.out_Result when present, else job.state — Successful -> success, " +
		"Faulted/Stopped -> failure, anything else -> still running or pending. The newest run is the latest " +
		"job.createdAt; retry cadence is each run's createdAt minus the previous (older) run's endedAt.\n" +
		"5. Compare the queue item's specificContent (from step 2) against the order from step 1 — facility, " +
		"speciality, DOS, member name/DOB/ID, insurance, CPT/ICD — and call out any divergence between what Copilot " +
		"sent to UiPath and what the order record says. The full payload is returned for exactly this; only the " +
		"token field is redacted.\n" +
		"6. Only if the digest is not enough, call get_job_logs with the failing jobId and onlyFailures=true " +
		"(add fullMessages=true for a full stack trace, includeVideo=true for the recording).\n" +
		"7. Summarize: order state, whether the automation ran, what failed or is pending, and any payload divergence." +
		envReminder
}

func cloneAndVerifyText(a map[string]string) string {
	uid, profile := a["uid"], a["profile"]
	p := ` (profile="` + profile + `")`
	step1 := "1. Call find_clone_candidates" + p + " to list recent prod orders that will actually clone, and pick one."
	orderUIDRef := "<the chosen uid>"
	if uid != "" {
		step1 = `1. You already have orderUid="` + uid + `". Optionally confirm it is cloneable with find_clone_candidates.`
		orderUIDRef = uid
	}
	return strings.Join([]string{
		"Clone a prod Copilot order into pre-prod and verify it" + p + ". This is a judgment-driven flow, not a fixed script — skip the diagnosis steps entirely on a clean mint, and don't assume every failure is a missing reference (it could be a transient 403, a bad date, etc).",
		step1,
		`2. Call get_order with orderUid="` + orderUIDRef + `", env="prod"` + p + " to read its facility, speciality, order type, order names, insurance, patient, and ICD/CPT detail.",
		`3. Using your own judgment (no automated matcher exists for this), find the PRE-PROD equivalents by NAME: call get_settings (env="pre_prod"` + p + ") or diff_settings" + p + ", scoped to whichever of these sections are relevant — referred-facilities (facility), specialties (speciality), orders-outbound-types (order type), orders (order names).",
		"4. Call create_preprod_order" + p + " with the fields derived in steps 2-3 (patientName, patientBirthDate, insuranceName, typeUid, orderNamesUids, and whichever of specialityUid / referredFacilityUid / referredProviderUid / location / appointmentDate / icdCodes / placeOfService apply). It never accepts a submit flag.",
		"5. If create_preprod_order throws, or its processMessage/verify status shows the order didn't reach forReview: identify which reference is implicated (the error or message should name the field), map it to a settings section per the list in step 3, then diagnose — call list_setting_sections if unsure of exact keys, then diff_settings" + p + " scoped to that section to confirm the item is onlyInProd (present in prod, missing in pre-prod). If confirmed, call plan_settings_sync" + p + " (same section scope) and report the proposed action id(s) + any warnings to the user — do NOT call apply_settings_sync yourself. Suggest: get approval, apply, then retry from step 4.",
		"6. On success, report {prodUid, newUid, verify} — the order should be at forReview.",
		"7. ONLY if the user explicitly authorizes it, call submit_preprod_order" + p + " with the new orderUid as a separate, deliberate step — never implied by a default.",
	}, "\n")
}

func triageStuckText(a map[string]string) string {
	p := ` (profile="` + a["profile"] + `")`
	return "Triage stuck Copilot orders" + p + ".\n" +
		"1. Call find_stuck_orders" + p + " with crossCheck=true to list orders stuck in a non-terminal status; each " +
		"carries orchestrator.{backend,verdict} (omit the orchestrator argument to check every orchestrator).\n" +
		"2. For the stuck orders whose orchestrator.verdict warrants a closer look (job-faulted, job-never-started, " +
		"job-running, job-found, job-successful-order-stuck), call find_order_queue_items per order to get each " +
		"order's jobIds per orchestrator, then diagnose them in ONE get_job call per orchestrator with " +
		"orchestrator=<that orchestrator>, jobIds=[...] (up to 25 ids per call) and includeLogDigest=true to see " +
		"whether each job failed, is still running, or never started. job-never-started means the latest attempt " +
		"was reaped before a worker ran it (e.g. a missing credential) — a platform/config fix, not an order-data " +
		"fix. An \"error: …\" verdict means the lookup failed, not that the order is fine — retry it.\n" +
		"3. Group them by root cause and recommend a remediation per group (retry, re-clone, fix data, escalate).\n" +
		"Do not take any write action without the user's explicit go-ahead."
}

func reportFaultedText(a map[string]string) string {
	orch, repo := a["orchestrator"], a["repo"]
	env := a["env"]
	if env == "" {
		env = "prod"
	}
	repoRef := repo
	if repoRef == "" {
		repoRef = "Apex-Medical-AI-Inc/RPAPlaywright"
	}
	listArgs := []string{`orchestrator="` + orch + `"`, `env="` + env + `"`}
	if since := a["since"]; since != "" {
		listArgs = append(listArgs, `since="`+since+`"`)
	}
	if top := a["top"]; top != "" {
		listArgs = append(listArgs, "top="+top)
	}
	return "File GitHub issues for faulted " + orch + " jobs in " + env + " on " + repoRef + ".\n" +
		"This requires a connected GitHub MCP server with write access to " + repoRef + "; if none is available, stop and say so.\n" +
		"1. Call list_jobs with " + strings.Join(listArgs, ", ") + ", then keep only faulted jobs: state \"Faulted\" on either orchestrator, plus " +
		"\"Stopped\" on uipath and \"Timed Out\" / \"Lost\" on apex. On apex a \"Stopped\" job is a cancellation or a retry " +
		"parent superseded by a later attempt, not a fault — skip it; and when a Faulted job has a later attempt (another " +
		"job whose retryOfJobId is its id), file only the final attempt, and only if it also faulted. If none remain, " +
		"report that and stop.\n" +
		"2. For each faulted job, call build_faulted_job_issue with orchestrator=\"" + orch + "\", env=\"" + env + "\" and that job's id as jobId" + when(repo != "", `, repo="`+repo+`"`) + " to get the issue payload (it does NOT post to GitHub).\n" +
		"3. Using the GitHub MCP server, run the payload's searchQuery to find an existing OPEN issue for this fault (matched by faultSignature):\n" +
		"   - If one exists, add the payload's recurrenceComment to that issue's discussion (do NOT open a duplicate).\n" +
		"   - Otherwise create a new issue with the payload's title, body, and labels.\n" +
		"4. Report a per-job summary: faulted job id -> created issue (#/url) or commented on existing issue (#/url).\n" +
		"Only write to GitHub — never take any orchestrator write action."
}
