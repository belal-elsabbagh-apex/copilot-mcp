package settings

// Read-only orchestration: DiffSettings (cross-env) and GetSettings (single-env).

import (
	"cmp"
	"context"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// account is the output label for a profile ("" = none).
func account(profile string) string { return cmp.Or(profile, "(default)") }

// connectPair logs into prod and pre-prod for the profile concurrently.
func connectPair(ctx context.Context, profile string) (prod, pre copilot.Client, err error) {
	var p, q copilot.Session
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { p, err = copilot.Connect(gctx, config.Prod, profile); return })
	g.Go(func() (err error) { q, err = copilot.Connect(gctx, config.PreProd, profile); return })
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return p.Client, q.Client, nil
}

// sectionCounter reports progress. Section fetches run concurrently, so "which step are we on" is
// a completion count, not an index. Calls to the reporter are serialized.
func sectionCounter(total int, onProgress shared.StepProgress) func(label string) {
	var mu sync.Mutex
	done := 0
	return func(label string) {
		mu.Lock()
		defer mu.Unlock()
		done++
		onProgress.Step(done, total, label)
	}
}

// fetchBoth fetches one section from both envs concurrently. It deliberately takes no
// cancel-on-first-error context: the Source memos capture the ctx of whoever reaches them first,
// and a sibling section sharing a crawl must not inherit this section's cancellation.
func fetchBoth(ctx context.Context, prod, pre *Source, sec *SettingsSection) (p, q any, err error) {
	var perr, qerr error
	var wg sync.WaitGroup
	wg.Go(func() { p, perr = prod.fetchSection(ctx, sec) })
	wg.Go(func() { q, qerr = pre.fetchSection(ctx, sec) })
	wg.Wait()
	if perr != nil {
		return p, q, perr
	}
	return p, q, qerr
}

func diffSection(sec *SettingsSection, prodData, preData any) SectionResult {
	r := SectionResult{Key: sec.Key, Label: sec.Label, Kind: sec.Kind}
	if sec.Kind == KindObject {
		ignore := toSet(sec.Ignore)
		r.Diffs = diffObjects(stripNoise(prodData, ignore), stripNoise(preData, ignore), "")
		r.Equal = len(r.Diffs) == 0
		return r
	}
	prodList, _ := prodData.([]any)
	preList, _ := preData.([]any)
	ld := diffList(prodList, preList, sec.MatchKey, sec.Ignore)
	r.OnlyInProd, r.OnlyInPreProd, r.Changed = ld.OnlyInProd, ld.OnlyInPreProd, ld.Changed
	r.Equal = len(ld.OnlyInProd)+len(ld.OnlyInPreProd)+len(ld.Changed) == 0
	return r
}

// DiffSettings logs into both envs for the account, fetches every selected settings section, and
// returns the normalized prod<->pre-prod diff.
func DiffSettings(ctx context.Context, opts DiffSettingsOpts) (DiffSettingsResult, error) {
	// Select first so an unknown tag/section fails fast (no needless login).
	chosen, err := selectSections(opts.Sections, opts.Tags, opts.EMR)
	if err != nil {
		return DiffSettingsResult{}, err
	}
	prod, pre, err := connectPair(ctx, opts.Profile)
	if err != nil {
		return DiffSettingsResult{}, err
	}
	return diffSettings(ctx, prod, pre, chosen, opts), nil
}

func diffSettings(ctx context.Context, prodClient, preClient copilot.Client, chosen []SettingsSection, opts DiffSettingsOpts) DiffSettingsResult {
	prod, pre := NewSource(prodClient), NewSource(preClient)
	// Every section's prod/pre-prod fetch is independent of every other section's — run them all
	// concurrently instead of paying each section's round-trip in turn (leaf reads are bounded by
	// each Source's semaphore). Results keep catalog order, and one section's failure never blocks
	// the rest (it is caught per section). A section crawl is the classic many-small-steps tool:
	// report each one as it lands (completion order, not catalog order) so a 20-section diff isn't
	// a silent minute.
	done := sectionCounter(len(chosen), opts.OnProgress)
	results := make([]SectionResult, len(chosen))
	var wg sync.WaitGroup
	for i := range chosen {
		sec := &chosen[i]
		wg.Go(func() {
			defer done(sec.Label)
			p, q, err := fetchBoth(ctx, prod, pre, sec)
			if err != nil {
				results[i] = SectionResult{Key: sec.Key, Label: sec.Label, Kind: sec.Kind, Error: err.Error()}
				return
			}
			results[i] = diffSection(sec, p, q)
		})
	}
	wg.Wait()

	shown := []SectionResult{}
	withDiffs := 0
	for _, r := range results {
		if !r.Equal {
			withDiffs++
		}
		if !r.Equal || opts.IncludeUnchanged {
			shown = append(shown, r)
		}
	}
	return DiffSettingsResult{
		Account: account(opts.Profile), ProdBase: prodClient.Base(), PreProdBase: preClient.Base(),
		SectionsCompared: len(chosen), SectionsWithDiff: withDiffs, Sections: shown,
	}
}

// GetSettings fetches the selected settings sections from ONE env — the single-env counterpart to
// DiffSettings. Read-only. With Normalized, stripped like DiffSettings (same noise-stripping);
// otherwise the raw payload with real UIDs/timestamps visible.
func GetSettings(ctx context.Context, opts GetSettingsOpts) (GetSettingsResult, error) {
	// Select first so an unknown tag/section fails fast (no needless login).
	chosen, err := selectSections(opts.Sections, opts.Tags, opts.EMR)
	if err != nil {
		return GetSettingsResult{}, err
	}
	s, err := copilot.Connect(ctx, opts.Env, opts.Profile)
	if err != nil {
		return GetSettingsResult{}, err
	}
	return getSettings(ctx, s.Client, chosen, opts), nil
}

func getSettings(ctx context.Context, client copilot.Client, chosen []SettingsSection, opts GetSettingsOpts) GetSettingsResult {
	src := NewSource(client)
	// Same independence as DiffSettings — fetch every section concurrently.
	done := sectionCounter(len(chosen), opts.OnProgress)
	sections := make([]GetSettingsSection, len(chosen))
	var wg sync.WaitGroup
	for i := range chosen {
		sec := &chosen[i]
		wg.Go(func() {
			defer done(sec.Label)
			out := GetSettingsSection{Key: sec.Key, Label: sec.Label, Tags: sec.Tags, Kind: sec.Kind}
			raw, err := src.fetchSection(ctx, sec)
			if err != nil {
				out.Error = err.Error()
				sections[i] = out
				return
			}
			out.Data = raw
			if opts.Normalized {
				out.Data = normalizeSectionValue(sec, raw)
			}
			if list, ok := out.Data.([]any); ok {
				n := len(list)
				out.Count = &n
			}
			sections[i] = out
		})
	}
	wg.Wait()
	return GetSettingsResult{
		Account: account(opts.Profile), Env: opts.Env, Base: client.Base(),
		SectionsFetched: len(chosen), Sections: sections,
	}
}
