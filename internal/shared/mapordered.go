package shared

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// MapOrdered runs fn over items concurrently, at most limit at a time, and returns the results in
// input order. The first error cancels the ctx the other calls see and is returned (Promise.all
// semantics); fn calls already running are still awaited so nothing outlives the call.
func MapOrdered[T, R any](ctx context.Context, items []T, limit int, fn func(ctx context.Context, i int, item T) (R, error)) ([]R, error) {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, limit))
	out := make([]R, len(items))
	skipped := false
	for i, item := range items {
		if gctx.Err() != nil {
			skipped = true
			break
		}
		g.Go(func() error {
			r, err := fn(gctx, i, item)
			if err != nil {
				return err
			}
			out[i] = r
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if skipped {
		return nil, ctx.Err()
	}
	return out, nil
}

// MapIsolated runs fn over every input on at most limit goroutines and returns the results in
// input order. fn reports failures through its result type, so one input's failure never
// cancels the others (the per-key isolation Promise.allSettled gave TS).
func MapIsolated[In, Out any](ctx context.Context, in []In, limit int, fn func(context.Context, In) Out) []Out {
	out := make([]Out, len(in))
	var g errgroup.Group
	g.SetLimit(max(1, limit))
	for i, v := range in {
		g.Go(func() error {
			out[i] = fn(ctx, v)
			return nil
		})
	}
	_ = g.Wait()
	return out
}
