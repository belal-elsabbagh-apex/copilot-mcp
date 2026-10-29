package settings

import (
	"fmt"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// ---- plan (pure over crawled trees + payer map) --------------------------

// npiOf reads the NPI. The BE has used both casings for this field across records (see BeFacility
// in the copilot client) — try both.
func npiOf(x any) string {
	if s, ok := shared.NPIField(shared.Pass(x, "NPI")).V.(string); ok {
		return s
	}
	s, _ := shared.NPIField(shared.Pass(x, "npi")).V.(string)
	return s
}

func payerWarning(dropped []string) string {
	return fmt.Sprintf("%d payer link(s) dropped (no matching pre-prod payer): %s", len(dropped), joinUniq(dropped))
}

// joinUniq is `[...new Set(a)].join(", ")`.
func joinUniq(a []string) string { return strings.Join(uniq(a), ", ") }

// uniq drops repeats, keeping first-seen order (a JS `[...new Set(a)]`).
func uniq(a []string) []string {
	out := make([]string, 0, len(a))
	for _, x := range a {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// preNPIEntry is one pre-prod facility/provider record, indexed by NPI regardless of which order
// type or speciality it lives under.
type preNPIEntry struct {
	typeName       string
	specialityName string
	name           string
}

// buildPreNPIIndex indexes every facility/provider NPI already present ANYWHERE in pre-prod —
// across every order type and speciality, not just the one being merged. A prod-only-by-name
// "addition" whose NPI shows up here is the same real-world record under a different name or
// casing, not a genuinely new one (issue #3: an all-caps pre-prod duplicate within the SAME
// speciality, and a same-NPI record living under a completely different speciality elsewhere in
// the tenant, both slipped past exact-string name matching and 400'd the BE's duplicate-NPI
// constraint).
func buildPreNPIIndex(preTree specialityTree) map[string]preNPIEntry {
	index := map[string]preNPIEntry{}
	for _, t := range preTree.types {
		for _, s := range t.specialities {
			specialityName := shared.Str(s, "name")
			for _, field := range []string{"referredFacilities", "referredProviders"} {
				for _, item := range asArray(s[field]) {
					npi := npiOf(item)
					if _, seen := index[npi]; npi != "" && !seen {
						index[npi] = preNPIEntry{t.name, specialityName, shared.Str(item, "name")}
					}
				}
			}
		}
	}
	return index
}

// missingByName returns the prod items missing from pre-prod BY NAME, minus any whose NPI already
// exists somewhere in pre-prod (a same-record duplicate hiding behind a name/casing difference) —
// those are dropped and reported via warnings instead of sent, since the write would 400 on the
// duplicate NPI otherwise.
func missingByName(prodArr, preArr []any, npiIndex map[string]preNPIEntry) (items []any, warnings []string) {
	have := map[string]bool{}
	for _, x := range preArr {
		have[shared.Str(x, "name")] = true
	}
	for _, x := range prodArr {
		name := shared.Str(x, "name")
		if have[name] {
			continue
		}
		npi := npiOf(x)
		if existing, ok := npiIndex[npi]; npi != "" && ok {
			warnings = append(warnings, fmt.Sprintf("'%s' (NPI %s) skipped — already in pre-prod as '%s' under %s / %s",
				name, npi, existing.name, existing.typeName, existing.specialityName))
			continue
		}
		items = append(items, x)
	}
	return items, warnings
}

func typesByName(tree specialityTree) map[string]crawledType {
	m := make(map[string]crawledType, len(tree.types))
	for _, t := range tree.types {
		m[t.name] = t
	}
	return m
}

func specialitiesByName(t crawledType) map[string]map[string]any {
	m := make(map[string]map[string]any, len(t.specialities))
	for _, s := range t.specialities {
		m[shared.Str(s, "name")] = s
	}
	return m
}

// planSpecialitySync matches order types by name, then specialities by name; it emits create
// actions for prod-only specialities and merge actions for specialities whose facilities/providers
// are a superset in prod. Pure — no network. Types/specialities that can't be placed are reported
// in skipped.
func planSpecialitySync(prodTree, preTree specialityTree, payerMap map[string]string) (actions []SyncAction, skipped []SyncSkip) {
	preTypeByName := typesByName(preTree)
	npiIndex := buildPreNPIIndex(preTree)

	for _, pType := range prodTree.types {
		qType, ok := preTypeByName[pType.name]
		if !ok {
			skipped = append(skipped, SyncSkip{TypeName: pType.name, Reason: "order type missing in pre-prod (order types are not auto-created)"})
			continue
		}
		preSpecByName := specialitiesByName(qType)

		for _, pSpec := range pType.specialities {
			sName := shared.Str(pSpec, "name")
			qSpec, ok := preSpecByName[sName]
			if !ok {
				body, droppedPayers := buildSpecialityCreateBody(pSpec, payerMap)
				a := SyncAction{
					Section: "specialties", Op: "create", ItemKind: "speciality", TypeName: pType.name, ItemName: sName,
					Method: "POST", Path: "/api/v1/settings/orders/outbound/types/" + qType.typeUID + "/specialities", Body: body,
				}
				if len(droppedPayers) > 0 {
					a.Warnings = []string{payerWarning(droppedPayers)}
				}
				actions = append(actions, a)
				continue
			}

			missFacs, facWarnings := missingByName(asArray(pSpec["referredFacilities"]), asArray(qSpec["referredFacilities"]), npiIndex)
			missProvs, provWarnings := missingByName(asArray(pSpec["referredProviders"]), asArray(qSpec["referredProviders"]), npiIndex)
			npiWarnings := append(facWarnings, provWarnings...)
			if len(missFacs) == 0 && len(missProvs) == 0 {
				if len(npiWarnings) > 0 {
					skipped = append(skipped, SyncSkip{
						TypeName: pType.name, SpecialityName: sName,
						Reason: "nothing left to merge after dropping NPI duplicates: " + strings.Join(npiWarnings, "; "),
					})
				}
				continue // already in sync (or everything missing was an NPI duplicate)
			}

			preSpecUID := shared.Str(qSpec, "specialityUid")
			if preSpecUID == "" {
				skipped = append(skipped, SyncSkip{
					TypeName: pType.name, SpecialityName: sName,
					Reason: "speciality has no specialityUid in pre-prod (cannot target the update endpoint)",
				})
				continue
			}
			body, droppedPayers := buildMergedSpecialityBody(qSpec, missFacs, missProvs, payerMap)
			var warnings []string
			if len(droppedPayers) > 0 {
				warnings = append(warnings, payerWarning(droppedPayers))
			}
			warnings = append(warnings, npiWarnings...)
			actions = append(actions, SyncAction{
				Section: "specialties", Op: "merge", ItemKind: "speciality", TypeName: pType.name, ItemName: sName,
				Method: "PUT", Path: "/api/v1/settings/specialities/" + preSpecUID, Body: body, Warnings: warnings,
			})
		}
	}
	return actions, skipped
}

// ---- payer-link audit (read-only; facilities/providers that already match by name) --------
// planSpecialitySync only ever ADDS prod-only facilities/providers — a facility/provider that
// already exists (matched by name) in both envs is never looked at again, so payer-link drift on
// shared records is invisible to sync (issue #4). Reconciling an EXISTING record's
// payersProviderId would mean MODIFYING pre-prod settings, not just adding to them — outside
// sync's additive-only contract — so this is audit-only: it reports findings, plan_settings_sync
// surfaces them, nothing here writes anything.

// payerLinkNames lists the payer names linked to one facility/provider row (resolved via the
// row's OWN env's payer list — payer uids are per-env and never expected to match across envs,
// only their names are comparable), plus any payerUid that doesn't resolve to a name at all in
// that env (a dangling reference).
func payerLinkNames(item any, payerNameByUID map[string]string) (names, orphaned []string) {
	names, orphaned = []string{}, []string{}
	for _, l := range asArray(shared.Prop(item, "payersProviderId")) {
		uid := shared.Str(l, "payerUid")
		if uid == "" {
			continue
		}
		if name, ok := payerNameByUID[uid]; ok && name != "" {
			names = append(names, name)
		} else {
			orphaned = append(orphaned, uid)
		}
	}
	return names, orphaned
}

// setDiff is a minus b, deduplicated, never nil.
func setDiff(a, b []string) []string {
	out := []string{}
	for _, x := range uniq(a) {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// auditPayerLinks audits payer links on facilities/providers that exist (matched by name) in BOTH
// envs' crawled speciality trees — exactly the records planSpecialitySync's additive logic never
// inspects. Pure over the same crawled trees + per-env payerUid->name maps the sync plan already
// builds for the specialities domain (no extra requests). Types/specialities not present in both
// envs are skipped here too — planSpecialitySync already reports those via skipped.
func auditPayerLinks(prodTree, preTree specialityTree, prodPayerNameByUID, prePayerNameByUID map[string]string) []PayerLinkFinding {
	preTypeByName := typesByName(preTree)
	findings := []PayerLinkFinding{}
	kinds := []struct{ field, itemKind string }{
		{"referredFacilities", "facility"},
		{"referredProviders", "provider"},
	}

	for _, pType := range prodTree.types {
		qType, ok := preTypeByName[pType.name]
		if !ok {
			continue
		}
		preSpecByName := specialitiesByName(qType)

		for _, pSpec := range pType.specialities {
			qSpec, ok := preSpecByName[shared.Str(pSpec, "name")]
			if !ok {
				continue
			}
			for _, k := range kinds {
				preItemByName := map[string]any{}
				for _, x := range asArray(qSpec[k.field]) {
					preItemByName[shared.Str(x, "name")] = x
				}
				for _, pItem := range asArray(pSpec[k.field]) {
					itemName := shared.Str(pItem, "name")
					qItem, ok := preItemByName[itemName]
					if !ok {
						continue // not shared -- planSpecialitySync's create/merge covers it
					}
					prodNames, prodOrphans := payerLinkNames(pItem, prodPayerNameByUID)
					preNames, preOrphans := payerLinkNames(qItem, prePayerNameByUID)
					f := PayerLinkFinding{
						TypeName: pType.name, SpecialityName: shared.Str(pSpec, "name"), ItemKind: k.itemKind, ItemName: itemName,
						ExtraInPreProd: setDiff(preNames, prodNames), MissingInPreProd: setDiff(prodNames, preNames),
						OrphanedProdPayerUIDs: prodOrphans, OrphanedPreProdPayerUIDs: preOrphans,
					}
					if len(f.ExtraInPreProd)+len(f.MissingInPreProd)+len(prodOrphans)+len(preOrphans) > 0 {
						findings = append(findings, f)
					}
				}
			}
		}
	}
	return findings
}
