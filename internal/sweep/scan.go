package sweep

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

type pageResult struct {
	rows []copilot.BeOrder
	err  error
}

// scanPagesOf walks /orders/filter pages 0..pages-1 in order, calling visit for each non-empty
// page, and stops at the first empty page. Up to pageWindow pages are fetched ahead; whatever is
// in flight past the stop is cancelled and discarded. A fetch error is returned when its turn comes.
func scanPagesOf(ctx context.Context, c copilot.Client, pages int, visit func(page int, rows []copilot.BeOrder)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	futures := make([]chan pageResult, pages)
	start := func(p int) {
		if p >= pages || futures[p] != nil {
			return
		}
		ch := make(chan pageResult, 1)
		futures[p] = ch
		go func() {
			res, err := copilot.FilterOrders(ctx, c, copilot.OrderFilterBody{
				PageSize: pageSize, PageNumber: p,
				FilterDimensions: copilot.FilterDimensions{Type: "Outbound Referral", OrderMode: copilot.OrderMode},
			})
			ch <- pageResult{res.Rows, err}
		}()
	}

	for page := range pages {
		for p := page; p < page+pageWindow; p++ {
			start(p)
		}
		r := <-futures[page]
		if r.err != nil {
			return r.err
		}
		if len(r.rows) == 0 {
			return nil
		}
		visit(page, r.rows)
	}
	return nil
}
