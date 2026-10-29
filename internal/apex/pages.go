package apex

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// pageWindow is how many pages after the first are in flight at once. ponytail: unmeasured; the
// shared dataSem still caps total concurrent GETs at fanOut.
const pageWindow = 4

// pageResult is one decoded page: a bare-array route sets bare; otherwise items/totalPages.
type pageResult[T any] struct {
	bare       []T
	isBare     bool
	items      []T
	hasItems   bool
	totalPages *int
}

func fetchPage[T any](ctx context.Context, env config.Env, path string, query map[string]string, page, pageSize int, folder string) (pageResult[T], error) {
	q := url.Values{}
	for k, v := range query {
		q.Set(k, v)
	}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	raw, err := apexRequest(ctx, env, http.MethodGet, path, reqOpts{Query: q, Folder: folder})
	if err != nil {
		return pageResult[T]{}, err
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		var all []T
		err := json.Unmarshal(raw, &all)
		return pageResult[T]{bare: all, isBare: true}, err
	}
	var pg struct {
		Items      *[]T `json:"items"`
		TotalPages *int `json:"total_pages"`
	}
	if json.Valid(raw) {
		if err := json.Unmarshal(raw, &pg); err != nil {
			return pageResult[T]{}, err
		}
	}
	res := pageResult[T]{totalPages: pg.TotalPages}
	if pg.Items != nil {
		res.items, res.hasItems = *pg.Items, true
	}
	return res, nil
}

// apexPages returns the rows of a paged route, at most limit. Page 1 is fetched first (it carries
// total_pages); pages 2..N are then independent and fetched pageWindow at a time, results kept in
// page order. A page that comes back empty ends the walk, as in the sequential original.
// Bare-array routes (workflow versions, review-queue, artifacts) come back as-is.
func apexPages[T any](ctx context.Context, env config.Env, path string, query map[string]string, limit int, folder string) ([]T, error) {
	rows := []T{}
	if limit <= 0 {
		return rows, nil
	}
	pageSize := maxPageSize
	if v, err := strconv.Atoi(query["page_size"]); err == nil {
		pageSize = min(v, maxPageSize)
	}
	pageSize = min(pageSize, limit)

	first, err := fetchPage[T](ctx, env, path, query, 1, pageSize, folder)
	if err != nil {
		return nil, err
	}
	if first.isBare {
		return first.bare[:min(len(first.bare), limit)], nil
	}
	rows = append(rows, first.items...)
	if !first.hasItems || len(first.items) == 0 || first.totalPages == nil || len(rows) >= limit {
		return rows[:min(len(rows), limit)], nil
	}
	// Full pages would reach limit after ceil(limit/pageSize) pages; never ask for more.
	last := min(*first.totalPages, (limit+max(pageSize, 1)-1)/max(pageSize, 1))
	pages := make([]int, 0, max(0, last-1))
	for p := 2; p <= last; p++ {
		pages = append(pages, p)
	}
	rest, err := shared.MapOrdered(ctx, pages, pageWindow, func(ctx context.Context, _ int, page int) (pageResult[T], error) {
		return fetchPage[T](ctx, env, path, query, page, pageSize, folder)
	})
	if err != nil {
		return nil, err
	}
	for _, pg := range rest {
		if !pg.hasItems || len(pg.items) == 0 {
			break
		}
		rows = append(rows, pg.items...)
	}
	return rows[:min(len(rows), limit)], nil
}
