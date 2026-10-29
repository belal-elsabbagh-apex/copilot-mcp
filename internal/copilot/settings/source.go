package settings

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// readConcurrency bounds one operation's in-flight reads against one client. The TS fanned out
// unbounded (every section x both envs, every order type); the BE is one tenant's API, so a cap
// keeps a 19-section diff from opening ~50 sockets at once.
const readConcurrency = 8

const typesPath = "/api/v1/settings/orders/outbound/types"

// once memoizes one computation, error included, for the lifetime of its Source.
type once[T any] struct {
	once sync.Once
	val  T
	err  error
}

func (o *once[T]) get(f func() (T, error)) (T, error) {
	o.once.Do(func() { o.val, o.err = f() })
	return o.val, o.err
}

// Source is ONE operation's read access to ONE logged-in client: it owns the read semaphore and
// the memos of the fetches several sections share (the order-type list and the speciality /
// order-name crawls). The TS memoized these in a WeakMap keyed by client, which was per-operation
// in effect because connect() returns a fresh client per call; here that scoping is explicit.
// A Source must never be reused across operations — plan and apply each build their own, so
// apply always re-plans against live state.
type Source struct {
	client copilot.Client
	sem    chan struct{}

	types once[[]orderType]
	specs once[specialityTree]
	names once[orderNameTree]
}

// NewSource wraps a logged-in client for one operation.
func NewSource(c copilot.Client) *Source {
	return &Source{client: c, sem: make(chan struct{}, readConcurrency)}
}

// Client is the underlying client (for the write path).
func (s *Source) Client() copilot.Client { return s.client }

// get is the only read path: it holds one of the semaphore slots for the duration of the request.
// Callers fanning out must not hold a slot themselves while waiting on children, or nested
// fan-outs would deadlock — only leaf requests acquire.
func (s *Source) get(ctx context.Context, path string) (copilot.Response, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return copilot.Response{}, ctx.Err()
	}
	defer func() { <-s.sem }()
	return s.client.Req(ctx, "GET", path, copilot.ReqBody{})
}

// fetchSection fetches one catalog section: its Derive crawl, or a plain GET unwrapped from its
// envelope.
func (s *Source) fetchSection(ctx context.Context, sec *SettingsSection) (any, error) {
	if sec.Derive != nil {
		return sec.Derive(ctx, s)
	}
	r, err := s.get(ctx, sec.Path)
	if err != nil {
		return nil, err
	}
	if r.Status >= 400 {
		return nil, fmt.Errorf("GET %s -> %d: %s", sec.Path, r.Status, shared.SliceUTF16(r.Text, 200))
	}
	return getEnvelope(r.Data, sec.Envelope), nil
}

// getEnvelope walks a dot-path into the payload; a missing step yields undef.
func getEnvelope(data any, envelope string) any {
	if envelope == "" {
		return data
	}
	cur := data
	for part := range strings.SplitSeq(envelope, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return undef
		}
		v, ok := m[part]
		if !ok {
			return undef
		}
		cur = v
	}
	return cur
}

// orderType is one outbound order type, reduced to the fields the crawls need.
type orderType struct {
	typeUID string
	name    string
}

// orderTypes lists the outbound order types that have a uid, memoized: both the speciality and the
// order-name crawls start from it.
func (s *Source) orderTypes(ctx context.Context) ([]orderType, error) {
	return s.types.get(func() ([]orderType, error) {
		r, err := s.get(ctx, typesPath)
		if err != nil {
			return nil, err
		}
		if r.Status >= 400 {
			return nil, fmt.Errorf("GET /settings/orders/outbound/types -> %d", r.Status)
		}
		var out []orderType
		for _, t := range asArray(r.Data) {
			if uid, _ := shared.StringProp(t, "typeUid"); uid != "" {
				name, _ := shared.StringProp(t, "name")
				out = append(out, orderType{uid, name})
			}
		}
		return out, nil
	})
}

// fanOut runs f over items concurrently and returns the results in input order. It does not bound
// itself: the bound is the Source's read semaphore, taken by the leaf requests f makes.
func fanOut[T, R any](ctx context.Context, items []T, f func(ctx context.Context, item T) (R, error)) ([]R, error) {
	out := make([]R, len(items))
	g, gctx := errgroup.WithContext(ctx)
	for i, it := range items {
		g.Go(func() error {
			r, err := f(gctx, it)
			out[i] = r
			return err
		})
	}
	return out, g.Wait()
}

func asArray(v any) []any {
	a, _ := v.([]any)
	return a
}
