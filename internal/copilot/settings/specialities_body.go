package settings

import "github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"

// ---- payer-link remap (own to this domain) ---------------------------------

// remapPayerLinks rewrites each link's payerUid prod->pre; links with no pre-prod match are
// dropped (and their prod uid reported).
func remapPayerLinks(links any, payerMap map[string]string) (out []any, dropped []string) {
	out = []any{}
	for _, l := range records(links) {
		prodUID := shared.Str(l, "payerUid")
		if prodUID == "" {
			out = append(out, cloneRecord(l)) // link carries no payer ref — keep verbatim
			continue
		}
		preUID, ok := payerMap[prodUID]
		if !ok {
			dropped = append(dropped, prodUID)
			continue
		}
		c := cloneRecord(l)
		c["payerUid"] = preUID
		out = append(out, c)
	}
	return out, dropped
}

func cloneRecord(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// ---- create/merge body builders (pure) --------------------------------------

// echoedParentUIDKey is the parent speciality's own uid, echoed onto every facility/provider row
// by the GET (like specialityName in noiseKeys) — must be dropped, but unlike noiseKeys this can't
// be matched generically: the row's OWN uid (referredFacilityUid / referredProviderUid) also ends
// in "Uid" and must survive.
const echoedParentUIDKey = "specialityUid"

// stripEchoedFields keeps an existing pre-prod facility/provider row as-is on a merge PUT (never
// treated as new, so its own uid must survive) minus the echo fields the GET adds
// (specialityUid/specialityName/createdAt/updatedAt) that the real UI does not resend (verified via
// a captured HAR of the "edit specialty" PUT).
func stripEchoedFields(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	for k, v := range item {
		if !noiseKeys[k] && k != echoedParentUIDKey {
			out[k] = v
		}
	}
	return out
}

// cleanReferralForWrite cleans a prod facility/provider for writing into pre-prod: strips its own
// UIDs and remaps the nested payersProviderId links prod->pre.
func cleanReferralForWrite(item any, payerMap map[string]string) (cleaned map[string]any, droppedPayers []string) {
	rec, _ := item.(map[string]any)
	cleaned = stripOwnUids(rec)
	if links, ok := cleaned["payersProviderId"]; ok {
		cleaned["payersProviderId"], droppedPayers = remapPayerLinks(links, payerMap)
	}
	return cleaned, droppedPayers
}

func cleanMany(items []any, payerMap map[string]string) (out []any, droppedPayers []string) {
	out = []any{}
	for _, it := range items {
		c, dropped := cleanReferralForWrite(it, payerMap)
		out = append(out, c)
		droppedPayers = append(droppedPayers, dropped...)
	}
	return out, droppedPayers
}

// buildSpecialityCreateBody is the POST body to create a whole prod-only speciality under a
// pre-prod type. Matches the verified HAR shape: { name, referredFacilities, referredProviders? }.
func buildSpecialityCreateBody(prodSpeciality any, payerMap map[string]string) (body map[string]any, droppedPayers []string) {
	facs, facDropped := cleanMany(asArray(shared.Prop(prodSpeciality, "referredFacilities")), payerMap)
	provsRaw := asArray(shared.Prop(prodSpeciality, "referredProviders"))
	provs, provDropped := cleanMany(provsRaw, payerMap)
	body = map[string]any{"name": shared.Str(prodSpeciality, "name"), "referredFacilities": facs}
	if len(provsRaw) > 0 {
		body["referredProviders"] = provs
	}
	return body, append(facDropped, provDropped...)
}

// buildMergedSpecialityBody is the PUT body to additively merge prod-only facilities/providers
// into an EXISTING pre-prod speciality. PUT is full-replace, so we keep the existing pre-prod
// facilities/providers verbatim (their referredFacilityUid + already-valid pre-prod payer links)
// and append only the cleaned, payer-remapped prod-only additions.
func buildMergedSpecialityBody(preSpeciality any, prodOnlyFacilities, prodOnlyProviders []any, payerMap map[string]string) (body map[string]any, droppedPayers []string) {
	existing := func(field string) []any {
		out := []any{}
		for _, r := range records(shared.Prop(preSpeciality, field)) {
			out = append(out, stripEchoedFields(r))
		}
		return out
	}
	existingFacs, existingProvs := existing("referredFacilities"), existing("referredProviders")
	newFacs, facDropped := cleanMany(prodOnlyFacilities, payerMap)
	newProvs, provDropped := cleanMany(prodOnlyProviders, payerMap)
	body = map[string]any{
		"name":               shared.Str(preSpeciality, "name"),
		"referredFacilities": append(existingFacs, newFacs...),
	}
	if len(existingProvs)+len(newProvs) > 0 {
		body["referredProviders"] = append(existingProvs, newProvs...)
	}
	return body, append(facDropped, provDropped...)
}
