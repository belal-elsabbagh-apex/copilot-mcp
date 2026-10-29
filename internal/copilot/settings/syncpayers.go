package settings

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"golang.org/x/sync/errgroup"
)

// payerUIDKeys are the candidate keys for a payer's own UID in the clinic-payers list (exact
// field unconfirmed — fall back to any *Uid key). A wrong/empty map drops payer links with a
// warning rather than linking to the wrong payer, so the miss is visible in dry-run output.
var payerUIDKeys = []string{"payerUid", "uid"}

func payerUIDOf(p any) string {
	for _, k := range payerUIDKeys {
		if v := shared.Str(p, k); v != "" {
			return v
		}
	}
	m, _ := p.(map[string]any)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if strings.HasSuffix(strings.ToLower(k), "uid") {
			if v := shared.Str(p, k); v != "" {
				return v
			}
		}
	}
	return ""
}

type payerLookup struct {
	uidByName map[string]string
	nameByUID map[string]string
}

// fetchPayerLookup reads name -> own payerUid AND uid -> name for one env (source: clinic-payers,
// envelope `data`, key `Name`) — one fetch, both directions: buildPayerMaps needs uidByName (to
// link prod payerUid -> pre payerUid by name); auditPayerLinks needs each env's OWN nameByUid (to
// resolve a facility's payer links back to a name for cross-env diffing, and to spot a uid that
// resolves to nothing).
func fetchPayerLookup(ctx context.Context, s *Source) (payerLookup, error) {
	r, err := s.get(ctx, "/api/v1/settings/clinic-payers")
	if err != nil {
		return payerLookup{}, err
	}
	if r.Status >= 400 {
		return payerLookup{}, fmt.Errorf("GET /settings/clinic-payers -> %d", r.Status)
	}
	l := payerLookup{uidByName: map[string]string{}, nameByUID: map[string]string{}}
	for _, p := range asArray(getEnvelope(r.Data, "data")) {
		name, uid := shared.Str(p, "Name"), payerUIDOf(p)
		if name != "" && uid != "" {
			l.uidByName[name] = uid
			l.nameByUID[uid] = name
		}
	}
	return l, nil
}

// buildPayerMaps maps each prod payerUid to the pre-prod payerUid of the payer with the same name,
// plus each env's own uid -> name map (needed to resolve/audit payer links directly, not just
// remap them). Built once and shared by every syncer, since both the specialities and orders
// domains remap payer references prod->pre.
func buildPayerMaps(ctx context.Context, prod, pre *Source) (PayerMaps, error) {
	var prodL, preL payerLookup
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { prodL, err = fetchPayerLookup(gctx, prod); return })
	g.Go(func() (err error) { preL, err = fetchPayerLookup(gctx, pre); return })
	if err := g.Wait(); err != nil {
		return PayerMaps{}, err
	}
	prodToPre := map[string]string{}
	for name, prodUID := range prodL.uidByName {
		if preUID, ok := preL.uidByName[name]; ok {
			prodToPre[prodUID] = preUID
		}
	}
	return PayerMaps{ProdToPre: prodToPre, ProdNameByUID: prodL.nameByUID, PreNameByUID: preL.nameByUID}, nil
}
