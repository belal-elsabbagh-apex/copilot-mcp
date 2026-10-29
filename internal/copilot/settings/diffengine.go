package settings

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// noiseKeys are fields that differ across envs by construction and carry no semantic meaning.
// specialityName is the parent speciality's name, echoed onto every facility/provider row by the
// GET — verified (via a captured HAR of the Settings UI's "edit specialty" PUT) that the real UI
// never resends it. source is a GET-only field the create/merge write schema rejects outright
// (verified live: POST .../specialities 400s with "referredFacilities[0].source is not allowed"
// until it is stripped).
var noiseKeys = map[string]bool{
	"createdAt":      true,
	"updatedAt":      true,
	"updatedBy":      true,
	"specialityName": true,
	"source":         true,
}

// isNoiseKey: a noise field, or anything ending in "uid" (case-insensitive).
func isNoiseKey(k string) bool {
	return noiseKeys[k] || strings.HasSuffix(strings.ToLower(k), "uid")
}

// stripNoise recursively drops env-specific fields and sorts keys/arrays so two semantically
// equal values normalize to byte-identical JSON regardless of UID/timestamp/order noise.
func stripNoise(value any, ignore map[string]bool) any {
	switch v := value.(type) {
	case []any:
		type entry struct {
			canon string
			val   any
		}
		entries := make([]entry, len(v))
		for i, x := range v {
			n := stripNoise(x, ignore)
			entries[i] = entry{canonical(n), n}
		}
		slices.SortStableFunc(entries, func(a, b entry) int { return strings.Compare(a.canon, b.canon) })
		out := make([]any, len(entries))
		for i, e := range entries {
			out[i] = e.val
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			if isNoiseKey(k) || ignore[k] {
				continue
			}
			out[k] = stripNoise(x, ignore)
		}
		return out
	}
	return value
}

func toSet(keys []string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// field reads a key off a record, reporting an absent key as undef (not null).
func field(m map[string]any, k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	return undef
}

// diffObjects deep-walks two NORMALIZED values into flat path-level diffs. Records recurse key by
// key; arrays/primitives/type-mismatches are reported whole at their path. The result is never
// nil.
func diffObjects(a, b any, base string) []FieldDiff {
	if canonical(a) == canonical(b) {
		return []FieldDiff{}
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if !aok || !bok {
		return []FieldDiff{{Path: pathOrRoot(base), Prod: a, Preprod: b}}
	}
	keys := slices.Sorted(maps.Keys(am))
	for k := range bm {
		if _, ok := am[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	out := []FieldDiff{}
	for _, k := range keys {
		path := k
		if base != "" {
			path = base + "." + k
		}
		out = append(out, diffObjects(field(am, k), field(bm, k), path)...)
	}
	return out
}

func pathOrRoot(base string) string {
	if base == "" {
		return "(root)"
	}
	return base
}

// keyOf is the item's match key: its matchKey field stringified, else its whole canonical form.
func keyOf(item any, matchKey string) string {
	m, _ := item.(map[string]any)
	switch v := m[matchKey].(type) {
	case nil:
		return canonical(item)
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return canonical(v)
	}
}

// orderedIndex is a Map that keeps first-insertion order; re-setting a key replaces the value but
// keeps the original position (JS Map semantics).
type orderedIndex struct {
	keys []string
	vals map[string]any
}

func indexBy(items []any, keyFn func(any) string) orderedIndex {
	ix := orderedIndex{vals: make(map[string]any, len(items))}
	for _, it := range items {
		k := keyFn(it)
		if _, seen := ix.vals[k]; !seen {
			ix.keys = append(ix.keys, k)
		}
		ix.vals[k] = it
	}
	return ix
}

func (ix orderedIndex) has(k string) bool { _, ok := ix.vals[k]; return ok }

func normalizeAll(items []any, ignore map[string]bool) []any {
	out := make([]any, len(items))
	for i, x := range items {
		out[i] = stripNoise(x, ignore)
	}
	return out
}

// diffList diffs two lists after normalizing each element. With a matchKey, items are paired by
// that field and field-diffed; without one, items are compared as a content set.
func diffList(prodArr, preArr []any, matchKey string, ignoreFields []string) ListDiff {
	ignore := toSet(ignoreFields)
	pN, qN := normalizeAll(prodArr, ignore), normalizeAll(preArr, ignore)
	out := ListDiff{OnlyInProd: []any{}, OnlyInPreProd: []any{}, Changed: []ChangedItem{}}

	keyFn := canonical
	if matchKey != "" {
		keyFn = func(it any) string { return keyOf(it, matchKey) }
	}
	pMap, qMap := indexBy(pN, keyFn), indexBy(qN, keyFn)
	for _, k := range pMap.keys {
		it := pMap.vals[k]
		if !qMap.has(k) {
			out.OnlyInProd = append(out.OnlyInProd, it)
			continue
		}
		if matchKey == "" {
			continue // same canonical form on both sides
		}
		if diffs := diffObjects(it, qMap.vals[k], ""); len(diffs) > 0 {
			out.Changed = append(out.Changed, ChangedItem{Key: k, Diffs: diffs})
		}
	}
	for _, k := range qMap.keys {
		if !pMap.has(k) {
			out.OnlyInPreProd = append(out.OnlyInPreProd, qMap.vals[k])
		}
	}
	return out
}

// normalizeSectionValue is stripNoise with a section's per-section ignore set — the same
// normalization the diff applies to each side, so get_settings' normalized output is
// byte-comparable with what diff_settings compared.
func normalizeSectionValue(section *SettingsSection, data any) any {
	return stripNoise(data, toSet(section.Ignore))
}
