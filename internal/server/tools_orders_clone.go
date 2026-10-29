package server

import (
	"context"
	"fmt"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- find_clone_candidates -------------------------------------------------

type findCloneCandidatesArgs struct {
	Profile   string `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Max candidates to return"`
	ScanPages int    `json:"scanPages,omitempty" jsonschema:"Pages of 50 recent orders to scan"`
}

// cloneCandidate is the summary of a prod order that will clone to forReview.
type cloneCandidate struct {
	OrderUID           shared.Field `json:"orderUid,omitzero"`
	Status             shared.Field `json:"status,omitzero"`
	Type               shared.Field `json:"type,omitzero"`
	Facility           shared.Field `json:"facility,omitzero"`
	Insurance          shared.Field `json:"insurance,omitzero"`
	HasAppointmentDate bool         `json:"hasAppointmentDate"`
	Icds               int          `json:"icds"`
	Cpts               int          `json:"cpts"`
}

// toCloneCandidate returns the candidate summary of o, or nil when it would get stuck
// 'incomplete': a prod order clones to forReview only with a facility, a type and orderNames.
func toCloneCandidate(o copilot.BeOrder) *cloneCandidate {
	names, _ := o.Get("orderNames").([]any)
	if o.Str("referredFacility", "referredFacilityUid") == "" || o.Str("orderType", "typeUid") == "" || len(names) == 0 {
		return nil
	}
	icds, _ := o.Get("ICDCodes").([]any)
	cpts, _ := o.Get("CPTCodes").([]any)
	return &cloneCandidate{
		OrderUID:           o.Pass("orderUid"),
		Status:             o.Pass("status"),
		Type:               o.Pass("orderType", "name"),
		Facility:           o.Pass("referredFacility", "name"),
		Insurance:          o.Pass("insurance", "name"),
		HasAppointmentDate: truthy(o.Get("appointmentDate")),
		Icds:               len(icds),
		Cpts:               len(cpts),
	}
}

// truthy is JS truthiness for the JSON values an order row can hold.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	}
	return true
}

type cloneCandidatesResult struct {
	Profile    string           `json:"profile"`
	Scanned    int              `json:"scanned"`
	Found      int              `json:"found"`
	Candidates []cloneCandidate `json:"candidates"`
}

// cloneScanWindow is how many order pages are in flight at once: the next page is fetched while
// the current one is being consumed, and the early stop (limit reached / empty page) abandons at
// most one extra page.
const cloneScanWindow = 2

type pageFetch struct {
	rows []copilot.BeOrder
	err  error
}

func findCloneCandidates(ctx context.Context, req *mcp.CallToolRequest, in findCloneCandidatesArgs) (any, error) {
	sess, err := copilot.Connect(ctx, config.Prod, in.Profile)
	if err != nil {
		return nil, err
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel() // abandons a prefetched page the early stop never consumed

	fetch := func(page int) <-chan pageFetch {
		ch := make(chan pageFetch, 1)
		go func() {
			p, err := copilot.FilterOrders(scanCtx, sess.Client, copilot.OrderFilterBody{
				PageSize: 50, PageNumber: page,
				FilterDimensions: copilot.FilterDimensions{Type: "Outbound Referral", OrderMode: copilot.OrderMode},
			})
			ch <- pageFetch{p.Rows, err}
		}()
		return ch
	}
	pending := map[int]<-chan pageFetch{}
	for p := range min(cloneScanWindow, in.ScanPages) {
		pending[p] = fetch(p)
	}

	candidates := []cloneCandidate{}
	scanned := 0
	for page := 0; page < in.ScanPages && len(candidates) < in.Limit; page++ {
		var res pageFetch
		select {
		case res = <-pending[page]:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		delete(pending, page)
		if res.err != nil {
			return nil, res.err
		}
		if len(res.rows) == 0 {
			break
		}
		if next := page + cloneScanWindow; next < in.ScanPages {
			pending[next] = fetch(next)
		}
		for _, o := range res.rows {
			scanned++
			if c := toCloneCandidate(o); c != nil {
				candidates = append(candidates, *c)
			}
			if len(candidates) >= in.Limit {
				break
			}
		}
		reportProgress(ctx, req, float64(page+1), float64(in.ScanPages), fmt.Sprintf("page %d: %d found", page+1, len(candidates)))
	}
	return cloneCandidatesResult{Profile: in.Profile, Scanned: scanned, Found: len(candidates), Candidates: candidates}, nil
}
