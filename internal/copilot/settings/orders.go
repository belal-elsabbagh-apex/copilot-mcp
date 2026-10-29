package settings

// Orders domain: crawl order types -> order names, and the additive write/sync plan for the
// `orders` catalog section (create prod-only orders in pre-prod).
//
// Each outbound order type carries a paged list of "order names" at /types/{uid}/names. They are
// not a standalone endpoint, so — like specialities — we crawl every type once per Source and
// flatten the rows for the diff.
//
// Sync side: each outbound order type holds a list of orders; each order references facilities,
// auth/referral sub-categories, and per-CPT payers. The create endpoint (verified against a
// captured HAR) is
//
//	POST /api/v1/settings/orders/outbound/types/{preTypeUid}/names
//
// with a body of UID *references* (facilitiesUids, authSubCategoryUids, referralSubCategoryUids,
// CPTCodes[].payers map). Those UIDs are per-env, so — exactly like payer links in the
// specialities domain — we REMAP every reference prod->pre by NAME and DROP (with a warning) any
// that has no pre-prod match, rather than copy a prod UID that would dangle or point at the wrong
// entity. Create-only: order names present in both envs are left untouched (there is no verified
// name-update endpoint, and this tool never overwrites).

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// orderNamesQS is the page/sort query the UI always sends; 200 is well above the per-type name
// count.
const orderNamesQS = "?pageSize=200&pageNumber=0&search=&sortAlphabitically=true"

// fetchOrderNames returns the order-name rows for one type (envelope `{ data: [...] }`).
func (s *Source) fetchOrderNames(ctx context.Context, typeUID string) ([]map[string]any, error) {
	r, err := s.get(ctx, fmt.Sprintf("/api/v1/settings/orders/outbound/types/%s/names%s", typeUID, orderNamesQS))
	if err != nil {
		return nil, err
	}
	if r.Status >= 400 {
		return nil, fmt.Errorf("GET names(%s) -> %d", typeUID, r.Status)
	}
	return records(shared.Prop(r.Data, "data")), nil
}

// orderNameType is one order type with its order-name rows.
type orderNameType struct {
	typeUID string
	name    string
	names   []map[string]any
}

type orderNameTree struct {
	types []orderNameType
}

// orderNameTree crawls every order type's names, memoized per Source.
func (s *Source) orderNameTree(ctx context.Context) (orderNameTree, error) {
	return s.names.get(func() (orderNameTree, error) {
		types, err := s.orderTypes(ctx)
		if err != nil {
			return orderNameTree{}, err
		}
		// Every type's names GET is independent of every other type's.
		crawled, err := fanOut(ctx, types, func(ctx context.Context, t orderType) (orderNameType, error) {
			names, err := s.fetchOrderNames(ctx, t.typeUID)
			return orderNameType{t.typeUID, t.name, names}, err
		})
		return orderNameTree{crawled}, err
	})
}

// deriveOrderNames is the flattened crawl used by the diff-path derived section.
func deriveOrderNames(ctx context.Context, s *Source) ([]any, error) {
	tree, err := s.orderNameTree(ctx)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, t := range tree.types {
		for _, n := range t.names {
			out = append(out, n)
		}
	}
	return out, nil
}

// preTypeRefs are the pre-prod name->UID lookups needed to remap one order type's references
// prod->pre.
type preTypeRefs struct {
	facilityUIDByName       map[string]string
	authSubCatUIDByName     map[string]string
	referralSubCatUIDByName map[string]string
}

// fetchFacilityUIDByName reads a pre-prod type's facilities: name -> referredFacilityUid (verified
// endpoint; the fullest source, since it includes facilities not yet referenced by any name).
func (s *Source) fetchFacilityUIDByName(ctx context.Context, typeUID string) (map[string]string, error) {
	r, err := s.get(ctx, "/api/v1/settings/specialities/facilities?typeUid="+typeUID)
	if err != nil {
		return nil, err
	}
	if r.Status >= 400 {
		return nil, fmt.Errorf("GET facilities(%s) -> %d", typeUID, r.Status)
	}
	rows, ok := r.Data.([]any)
	if !ok {
		rows = asArray(shared.Prop(r.Data, "data"))
	}
	m := map[string]string{}
	for _, f := range rows {
		if name, uid := shared.Str(f, "name"), shared.Str(f, "referredFacilityUid"); name != "" && uid != "" {
			m[name] = uid
		}
	}
	return m, nil
}

// harvestSubCatUIDByName: sub-categories have no list endpoint, so harvest name -> subCategoryUid
// from the sub-category objects embedded in the (pre-prod) crawled name rows of one type.
func harvestSubCatUIDByName(names []map[string]any, field string) map[string]string {
	m := map[string]string{}
	for _, n := range names {
		for _, sc := range asArray(n[field]) {
			if name, uid := shared.Str(sc, "subCategoryName"), shared.Str(sc, "subCategoryUid"); name != "" && uid != "" {
				m[name] = uid
			}
		}
	}
	return m
}

// buildPreTypeRefs builds the per-type pre-prod ref maps, keyed by order-type NAME (the
// cross-env match key). Each type's facility lookup is its own GET.
func buildPreTypeRefs(ctx context.Context, pre *Source, preTree orderNameTree) (map[string]preTypeRefs, error) {
	refs, err := fanOut(ctx, preTree.types, func(ctx context.Context, t orderNameType) (preTypeRefs, error) {
		fac, err := pre.fetchFacilityUIDByName(ctx, t.typeUID)
		return preTypeRefs{
			facilityUIDByName:       fac,
			authSubCatUIDByName:     harvestSubCatUIDByName(t.names, "authSubCategories"),
			referralSubCatUIDByName: harvestSubCatUIDByName(t.names, "referralSubCategories"),
		}, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]preTypeRefs, len(refs))
	for i, t := range preTree.types {
		out[t.name] = refs[i]
	}
	return out, nil
}

// remapPayersMap rewrites a CPT's payers map prod->pre: both the map key and each entry's inner
// payerUid are remapped; a payer with no pre-prod match is dropped and reported by payer name.
// dropped is in sorted-key order so plans are deterministic.
func remapPayersMap(payers any, payerMap map[string]string) (out map[string]any, dropped []string) {
	out = map[string]any{}
	m, _ := payers.(map[string]any)
	for _, prodUID := range slices.Sorted(maps.Keys(m)) {
		val := m[prodUID]
		preUID, ok := payerMap[prodUID]
		if !ok {
			dropped = append(dropped, cmp.Or(shared.Str(val, "Name"), prodUID))
			continue
		}
		if rec, isRec := val.(map[string]any); isRec {
			c := cloneRecord(rec)
			c["payerUid"] = preUID
			val = c
		}
		out[preUID] = val
	}
	return out, dropped
}

// remapNamedRefs resolves a list of named reference objects to their pre-prod UIDs by name;
// unmatched names are dropped and returned so the caller can warn.
func remapNamedRefs(items any, nameField string, byName map[string]string) (uids, dropped []string) {
	uids = []string{}
	for _, it := range asArray(items) {
		name := shared.Str(it, nameField)
		if name == "" {
			continue
		}
		if uid, ok := byName[name]; ok {
			uids = append(uids, uid)
		} else {
			dropped = append(dropped, name)
		}
	}
	return uids, dropped
}

// cleanCptForWrite cleans one CPT for the create body: drops its own codeUid, remaps the payers
// map prod->pre.
func cleanCptForWrite(cpt any, payerMap map[string]string) (cleaned map[string]any, dropped []string) {
	rec, _ := cpt.(map[string]any)
	cleaned = stripOwnUids(rec)
	if p, ok := cleaned["payers"]; ok {
		cleaned["payers"], dropped = remapPayersMap(p, payerMap)
	}
	return cleaned, dropped
}

// buildOrderNameCreateBody is the POST body to create a whole prod-only order name under a
// pre-prod type. Matches the verified HAR shape: { name, CPTCodes, facilitiesUids,
// authSubCategoryUids, referralSubCategoryUids, mandatoryICDCodes }. Every UID reference is
// remapped prod->pre by name; unmatched refs/payers are dropped and reported.
func buildOrderNameCreateBody(prodRow any, refs preTypeRefs, payerMap map[string]string) (body map[string]any, droppedPayers, droppedRefs []string) {
	cpts := []any{}
	for _, c := range asArray(shared.Prop(prodRow, "outboundReferralOrderTypeNameCPTCodes")) {
		cleaned, dropped := cleanCptForWrite(c, payerMap)
		cpts = append(cpts, cleaned)
		droppedPayers = append(droppedPayers, dropped...)
	}
	facUIDs, facDropped := remapNamedRefs(shared.Prop(prodRow, "referredFacilities"), "name", refs.facilityUIDByName)
	authUIDs, authDropped := remapNamedRefs(shared.Prop(prodRow, "authSubCategories"), "subCategoryName", refs.authSubCatUIDByName)
	refUIDs, refDropped := remapNamedRefs(shared.Prop(prodRow, "referralSubCategories"), "subCategoryName", refs.referralSubCatUIDByName)
	for _, n := range facDropped {
		droppedRefs = append(droppedRefs, fmt.Sprintf("facility '%s'", n))
	}
	for _, n := range authDropped {
		droppedRefs = append(droppedRefs, fmt.Sprintf("auth sub-category '%s'", n))
	}
	for _, n := range refDropped {
		droppedRefs = append(droppedRefs, fmt.Sprintf("referral sub-category '%s'", n))
	}
	icds := asArray(shared.Prop(prodRow, "mandatoryICDCodes"))
	if icds == nil {
		icds = []any{}
	}
	body = map[string]any{
		"name":                    shared.Str(prodRow, "name"),
		"CPTCodes":                cpts,
		"facilitiesUids":          anySlice(facUIDs),
		"authSubCategoryUids":     anySlice(authUIDs),
		"referralSubCategoryUids": anySlice(refUIDs),
		"mandatoryICDCodes":       icds,
	}
	return body, droppedPayers, droppedRefs
}

func refWarning(dropped []string) string {
	return fmt.Sprintf("%d reference(s) dropped (no matching pre-prod entity by name): %s", len(dropped), joinUniq(dropped))
}

// planOrderNameSync matches order types by name, then order names by name; it emits a create
// action for each prod-only order name. Pure — the per-type pre-prod ref maps and payer map are
// supplied by the caller. Existing (both-env) names are left untouched; types missing in pre-prod
// are reported in skipped.
func planOrderNameSync(prodTree, preTree orderNameTree, preRefsByType map[string]preTypeRefs, payerMap map[string]string) (actions []SyncAction, skipped []SyncSkip) {
	preTypeByName := make(map[string]orderNameType, len(preTree.types))
	for _, t := range preTree.types {
		preTypeByName[t.name] = t
	}

	for _, pType := range prodTree.types {
		qType, ok := preTypeByName[pType.name]
		if !ok {
			skipped = append(skipped, SyncSkip{TypeName: pType.name, Reason: "order type missing in pre-prod (order types are not auto-created)"})
			continue
		}
		refs := preRefsByType[pType.name] // zero value: empty lookups, so every reference drops
		preNames := map[string]bool{}
		for _, n := range qType.names {
			preNames[shared.Str(n, "name")] = true
		}
		for _, pName := range pType.names {
			nm := shared.Str(pName, "name")
			if preNames[nm] {
				continue // present in both — create-only, leave untouched
			}
			body, droppedPayers, droppedRefs := buildOrderNameCreateBody(pName, refs, payerMap)
			var warnings []string
			if len(droppedPayers) > 0 {
				warnings = append(warnings, payerWarning(droppedPayers))
			}
			if len(droppedRefs) > 0 {
				warnings = append(warnings, refWarning(droppedRefs))
			}
			actions = append(actions, SyncAction{
				Section: "orders", Op: "create", ItemKind: "order", TypeName: pType.name, ItemName: nm,
				Method: "POST", Path: "/api/v1/settings/orders/outbound/types/" + qType.typeUID + "/names",
				Body: body, Warnings: warnings,
			})
		}
	}
	return actions, skipped
}

// ---- pluggable syncer --------------------------------------------------------

type ordersSyncer struct{}

func (ordersSyncer) Domain() string { return "orders" }

func (ordersSyncer) Crawl(ctx context.Context, prod, pre *Source) (Planner, error) {
	var prodTree, preTree orderNameTree
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { prodTree, err = prod.orderNameTree(gctx); return })
	g.Go(func() (err error) { preTree, err = pre.orderNameTree(gctx); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	preRefs, err := buildPreTypeRefs(ctx, pre, preTree)
	if err != nil {
		return nil, err
	}
	return func(p PayerMaps) SyncPlanResult {
		actions, skipped := planOrderNameSync(prodTree, preTree, preRefs, p.ProdToPre)
		return SyncPlanResult{Actions: actions, Skipped: skipped}
	}, nil
}
