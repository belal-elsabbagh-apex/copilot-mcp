package settings

// stripOwnUids drops own-record UIDs + timestamps (the BE assigns fresh ones on create). Shared by
// the specialities and orders write-body builders.
func stripOwnUids(rec map[string]any) map[string]any {
	out := make(map[string]any, len(rec))
	for k, v := range rec {
		if !isNoiseKey(k) {
			out[k] = v
		}
	}
	return out
}

// anySlice widens a typed slice so it can sit in a request body next to decoded JSON arrays.
func anySlice[T any](s []T) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

// records keeps the elements of a decoded JSON array that are objects.
func records(v any) []map[string]any {
	var out []map[string]any
	for _, x := range asArray(v) {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
