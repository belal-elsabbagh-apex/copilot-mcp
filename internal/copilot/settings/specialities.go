package settings

// Specialities domain: crawl order types -> specialities, and the additive write/sync plan for
// the `specialties` / `referred-providers` / `referred-facilities` catalog sections.
//
// "Specialties", their "referred (referring) providers", and "referred facilities" are not
// standalone endpoints — they are nested inside each outbound order type's /specialities
// response. We crawl every order type once per Source and slice the three lists out of the
// result, so the three derived sections (and the sync planner) share a single pass.
//
// Sync side: two additive ops, both verified against captured HARs:
//   - create: a prod-only speciality (with its facilities/providers) ->
//     POST /api/v1/settings/orders/outbound/types/{preTypeUid}/specialities
//   - merge:  a speciality present in both, with prod-only facilities/providers ->
//     PUT  /api/v1/settings/specialities/{preSpecialityUid}  (full-replace body, so we send the
//     EXISTING pre-prod speciality + only the prod-only additions)
//
// Crucial: referredFacilities[].payersProviderId[].payerUid is a real foreign key to a payer, and
// payer UIDs differ per env — so we REMAP payerUid prod->pre (payers matched by name), never
// strip it. Own-record UIDs (specialityUid, referredFacilityUid, timestamps) ARE stripped on
// create (the BE assigns fresh ones).

import (
	"context"
	"fmt"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"golang.org/x/sync/errgroup"
)

// crawledType is one order type with its full speciality objects (referredFacilities, etc.).
type crawledType struct {
	typeUID      string
	name         string
	specialities []map[string]any
}

type specialityTree struct {
	types []crawledType
}

// specialityTree crawls every order type's specialities, memoized per Source.
func (s *Source) specialityTree(ctx context.Context) (specialityTree, error) {
	return s.specs.get(func() (specialityTree, error) {
		types, err := s.orderTypes(ctx)
		if err != nil {
			return specialityTree{}, err
		}
		// Every type's specialities GET is independent of every other type's.
		crawled, err := fanOut(ctx, types, func(ctx context.Context, t orderType) (crawledType, error) {
			r, err := s.get(ctx, fmt.Sprintf("/api/v1/settings/orders/outbound/types/%s/specialities", t.typeUID))
			if err != nil {
				return crawledType{}, err
			}
			if r.Status >= 400 {
				return crawledType{}, fmt.Errorf("GET specialities(%s) -> %d", t.name, r.Status)
			}
			return crawledType{t.typeUID, t.name, records(shared.Prop(r.Data, "data"))}, nil
		})
		return specialityTree{crawled}, err
	})
}

// ---- flattened crawl, used by the diff-path derived sections ---------------

func deriveSpecialties(ctx context.Context, s *Source) ([]any, error) {
	tree, err := s.specialityTree(ctx)
	if err != nil {
		return nil, err
	}
	out := []any{}
	for _, t := range tree.types {
		for _, sp := range t.specialities {
			out = append(out, map[string]any{"type": t.name, "name": shared.Str(sp, "name")})
		}
	}
	return out, nil
}

// deriveNested flattens one nested list (referredProviders / referredFacilities) of every
// speciality.
func deriveNested(field string) func(context.Context, *Source) ([]any, error) {
	return func(ctx context.Context, s *Source) ([]any, error) {
		tree, err := s.specialityTree(ctx)
		if err != nil {
			return nil, err
		}
		out := []any{}
		for _, t := range tree.types {
			for _, sp := range t.specialities {
				for _, rec := range records(sp[field]) {
					out = append(out, rec)
				}
			}
		}
		return out, nil
	}
}

// ---- pluggable syncer --------------------------------------------------------

type specialitiesSyncer struct{}

func (specialitiesSyncer) Domain() string { return "specialities" }

func (specialitiesSyncer) Crawl(ctx context.Context, prod, pre *Source) (Planner, error) {
	var prodTree, preTree specialityTree
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { prodTree, err = prod.specialityTree(gctx); return })
	g.Go(func() (err error) { preTree, err = pre.specialityTree(gctx); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return func(p PayerMaps) SyncPlanResult {
		actions, skipped := planSpecialitySync(prodTree, preTree, p.ProdToPre)
		return SyncPlanResult{
			Actions:           actions,
			Skipped:           skipped,
			PayerLinkFindings: auditPayerLinks(prodTree, preTree, p.ProdNameByUID, p.PreNameByUID),
		}
	}, nil
}
