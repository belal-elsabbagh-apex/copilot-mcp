package shared

import (
	"context"

	"golang.org/x/sync/singleflight"
)

// SingleFlight runs fn once per key at a time; concurrent callers share its result. fn runs
// detached from the first caller's cancellation (every caller waits on its own ctx), so one
// cancelled call never fails the others sharing it. Callers that need to invalidate an in-flight
// call (a reset) use g.Forget(key).
func SingleFlight[T any](ctx context.Context, g *singleflight.Group, key string, fn func(context.Context) (T, error)) (T, error) {
	ch := g.DoChan(key, func() (any, error) { return fn(context.WithoutCancel(ctx)) })
	select {
	case r := <-ch:
		v, _ := r.Val.(T)
		return v, r.Err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
