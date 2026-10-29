package settings

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

// These sections are real catalog entries — reused rather than a fake catalog so the fetch paths
// are real, verifiable GETs. location-regions always 500s to test error isolation.
var locationSections = []string{"locations", "location-groups", "location-regions"}

func pick(t *testing.T, keys ...string) []SettingsSection {
	t.Helper()
	out, err := selectSections(keys, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func locationsBE(env config.Env, byPath map[string]any) *fakeBE {
	return &fakeBE{env: env, handle: func(method, path string, body any) copilot.Response {
		if path == "/api/v1/settings/locations/regions" {
			return copilot.Response{Status: 500, Text: "server error"}
		}
		if data, ok := byPath[path]; ok {
			return respond(200, data)
		}
		return respond(200, []any{})
	}}
}

func TestGetSettingsFetchesEverySectionPreservingCatalogOrderAndIsolatingErrors(t *testing.T) {
	be := locationsBE(config.Prod, map[string]any{
		"/api/v1/settings/locations":        j(t, `[{"name":"OSSM"}]`),
		"/api/v1/settings/locations/groups": j(t, `[{"name":"Group A"}]`),
	})
	out := getSettings(t.Context(), be, pick(t, locationSections...), GetSettingsOpts{Env: config.Prod})
	var keys []string
	for _, s := range out.Sections {
		keys = append(keys, s.Key)
	}
	if !slices.Equal(keys, locationSections) {
		t.Fatalf("keys = %v, want catalog order %v", keys, locationSections)
	}
	if asJSON(t, out.Sections[0].Data) != `[{"name":"OSSM"}]` || out.Sections[0].Error != "" {
		t.Errorf("locations = %+v", out.Sections[0])
	}
	if asJSON(t, out.Sections[1].Data) != `[{"name":"Group A"}]` {
		t.Errorf("groups = %+v", out.Sections[1])
	}
	// location-regions 500s — isolated to its own entry, doesn't affect the others.
	if r := out.Sections[2]; !strings.Contains(r.Error, "500") || r.Data != nil {
		t.Errorf("regions = %+v, want an error and no data", r)
	}
	if out.SectionsFetched != 3 || out.Account != "(default)" || out.Env != config.Prod || out.Base != be.Base() {
		t.Errorf("result = %+v", out)
	}
}

func TestGetSettingsReportsProgressOncePerSectionIncludingTheOneThatErrored(t *testing.T) {
	be := locationsBE(config.Prod, nil)
	var mu sync.Mutex
	var steps []string
	var labels []string
	getSettings(t.Context(), be, pick(t, locationSections...), GetSettingsOpts{Env: config.Prod,
		OnProgress: func(done, total int, label string) {
			mu.Lock()
			defer mu.Unlock()
			steps = append(steps, fmt.Sprintf("%d/%d", done, total))
			labels = append(labels, label)
		}})
	if !slices.Equal(steps, []string{"1/3", "2/3", "3/3"}) {
		t.Errorf("steps = %v", steps)
	}
	// Labels name the section that just finished, in completion order (not catalog order).
	if slices.Sort(labels); !slices.Equal(labels, []string{"Location groups", "Location regions", "Locations"}) {
		t.Errorf("labels = %v", labels)
	}
}

func TestGetSettingsNormalizedStripsNoiseAndRawKeepsIt(t *testing.T) {
	be := locationsBE(config.PreProd, map[string]any{
		"/api/v1/settings/locations": j(t, `[{"locationUid":"u1","name":"OSSM COR","email":"dummy@x.dev","active":true}]`),
	})
	sections := pick(t, "locations")
	norm := getSettings(t.Context(), be, sections, GetSettingsOpts{Env: config.PreProd, Normalized: true}).Sections[0]
	if got := asJSON(t, norm); got != `{"key":"locations","label":"Locations","tags":["locations"],"kind":"list","count":1,"data":[{"active":true,"name":"OSSM COR"}]}` {
		t.Errorf("normalized = %s", got)
	}
	raw := getSettings(t.Context(), be, sections, GetSettingsOpts{Env: config.PreProd}).Sections[0]
	if !strings.Contains(asJSON(t, raw), `"locationUid":"u1"`) || !strings.Contains(asJSON(t, raw), `"email":"dummy@x.dev"`) {
		t.Errorf("raw = %s, want real UIDs and the ignored field kept", asJSON(t, raw))
	}
}

func TestGetSettingsJSONShapeForObjectNullMissingAndFailedSections(t *testing.T) {
	be := &fakeBE{env: config.Prod, handle: routes(map[string]any{
		"GET /api/v1/settings/orders/outbound":            j(t, `{"autoSubmit":true}`),
		"GET /api/v1/settings/auto-finish-document-rules": nil, // JSON null payload
		"GET /api/v1/settings/fileManager":                j(t, `{"data":{}}`),
	})}
	out := getSettings(t.Context(), be, pick(t, "file-manager", "orders-outbound", "auto-finish-document-rules", "eligibility-visibility"), GetSettingsOpts{Env: config.Prod})
	want := []string{
		`{"key":"file-manager","label":"File manager settings","tags":["file-manager"],"kind":"object"}`, // envelope step absent -> data omitted
		`{"key":"orders-outbound","label":"Outbound order settings","tags":["orders"],"kind":"object","data":{"autoSubmit":true}}`,
		`{"key":"auto-finish-document-rules","label":"Auto-finish document rules","tags":["documents"],"kind":"object","data":null}`,
	}
	for i, w := range want {
		if got := asJSON(t, out.Sections[i]); got != w {
			t.Errorf("section %d = %s\nwant       %s", i, got, w)
		}
	}
	if got := asJSON(t, out.Sections[3]); !strings.HasPrefix(got, `{"key":"eligibility-visibility"`) || !strings.Contains(got, `"error":"GET /api/v1/settings/eligibility/eligibilityVisibility -> 404: `) || strings.Contains(got, `"data"`) {
		t.Errorf("failed section = %s", got)
	}
}

func TestDiffSettingsDiffsEverySectionAndAFetchErrorDoesNotAffectTheOthers(t *testing.T) {
	data := map[string]any{"/api/v1/settings/locations": j(t, `[{"name":"OSSM"}]`)}
	prod, pre := locationsBE(config.Prod, data), locationsBE(config.PreProd, data)
	out := diffSettings(t.Context(), prod, pre, pick(t, locationSections...), DiffSettingsOpts{IncludeUnchanged: true})
	if len(out.Sections) != 3 || out.Sections[0].Key != "locations" || !out.Sections[0].Equal { // identical in both envs
		t.Fatalf("sections = %+v", out.Sections)
	}
	if r := out.Sections[2]; r.Equal || !strings.Contains(r.Error, "500") { // 500 -> error, not equal
		t.Errorf("regions = %+v", r)
	}
	if out.SectionsCompared != 3 || out.SectionsWithDiff != 1 || out.ProdBase != prod.Base() || out.PreProdBase != pre.Base() {
		t.Errorf("result = %+v", out)
	}
}

func TestDiffSettingsOmitsUnchangedSectionsUnlessAsked(t *testing.T) {
	prod := locationsBE(config.Prod, map[string]any{"/api/v1/settings/locations": j(t, `[{"name":"OSSM"}]`), "/api/v1/settings/locations/groups": j(t, `[{"name":"G"}]`)})
	pre := locationsBE(config.PreProd, map[string]any{"/api/v1/settings/locations": j(t, `[{"name":"OSSM"}]`)})
	out := diffSettings(t.Context(), prod, pre, pick(t, "locations", "location-groups"), DiffSettingsOpts{Profile: "ossm"})
	if len(out.Sections) != 1 || out.Sections[0].Key != "location-groups" || out.SectionsWithDiff != 1 || out.Account != "ossm" {
		t.Errorf("result = %+v", out)
	}
	if got := asJSON(t, out.Sections[0]); got != `{"key":"location-groups","label":"Location groups","kind":"list","equal":false,"onlyInProd":[{"name":"G"}],"onlyInPreProd":[],"changed":[]}` {
		t.Errorf("list section JSON = %s", got)
	}
}

func TestDiffSettingsObjectAndErrorSectionJSONShapes(t *testing.T) {
	prod := &fakeBE{env: config.Prod, handle: routes(map[string]any{"GET /api/v1/settings/orders/outbound": j(t, `{"sendFax":true,"ruleUid":"p"}`)})}
	pre := &fakeBE{env: config.PreProd, handle: routes(map[string]any{"GET /api/v1/settings/orders/outbound": j(t, `{"sendFax":false,"ruleUid":"q"}`)})}
	out := diffSettings(t.Context(), prod, pre, pick(t, "orders-outbound", "locations"), DiffSettingsOpts{})
	want := []string{
		`{"key":"orders-outbound","label":"Outbound order settings","kind":"object","equal":false,"diffs":[{"path":"sendFax","prod":true,"preprod":false}]}`,
		`{"key":"locations","label":"Locations","kind":"list","equal":false,"error":"GET /api/v1/settings/locations -> 404: {\"error\":\"no route GET /api/v1/settings/locations\"}"}`,
	}
	for i, w := range want {
		if got := asJSON(t, out.Sections[i]); got != w {
			t.Errorf("section %d = %s\nwant       %s", i, got, w)
		}
	}
}

func TestDiffSettingsReportsProgressOncePerSectionCompared(t *testing.T) {
	be := func(env config.Env) *fakeBE { return locationsBE(env, nil) }
	var mu sync.Mutex
	var steps []string
	diffSettings(t.Context(), be(config.Prod), be(config.PreProd), pick(t, locationSections...), DiffSettingsOpts{
		OnProgress: func(done, total int, _ string) {
			mu.Lock()
			defer mu.Unlock()
			steps = append(steps, fmt.Sprintf("%d/%d", done, total))
		}})
	if !slices.Equal(steps, []string{"1/3", "2/3", "3/3"}) {
		t.Errorf("steps = %v", steps)
	}
}

// The three speciality-derived sections and the orders section all start from the order-type
// list, and the three speciality ones share one crawl: the type list and each type's specialities
// are fetched once per env, however many sections are selected.
func TestDiffSettingsSharesOneCrawlAcrossDerivedSections(t *testing.T) {
	handler := func(method, path string, _ any) copilot.Response {
		switch {
		case path == typesPath:
			return respond(200, j(t, `[{"typeUid":"t1","name":"Imaging"},{"typeUid":"t2","name":"Lab"}]`))
		case strings.HasSuffix(path, "/specialities"):
			return respond(200, j(t, `{"data":[{"name":"MRI","referredProviders":[{"name":"Dr A"}],"referredFacilities":[{"name":"RadNet"}]}]}`))
		case strings.Contains(path, "/names?"):
			return respond(200, j(t, `{"data":[{"name":"Brain MRI"}]}`))
		}
		return respond(404, nil)
	}
	prod, pre := &fakeBE{env: config.Prod, handle: handler}, &fakeBE{env: config.PreProd, handle: handler}
	out := diffSettings(t.Context(), prod, pre, pick(t, "specialties", "referred-providers", "referred-facilities", "orders"), DiffSettingsOpts{IncludeUnchanged: true})
	for _, s := range out.Sections {
		if !s.Equal || s.Error != "" {
			t.Errorf("%s = %+v, want equal", s.Key, s)
		}
	}
	for _, be := range []*fakeBE{prod, pre} {
		if n := be.count("GET", typesPath); n != 1 {
			t.Errorf("%s: order-type list fetched %d times, want once", be.env, n)
		}
		if n := be.count("GET", typesPath+"/t1/specialities"); n != 1 {
			t.Errorf("%s: t1 specialities fetched %d times, want once", be.env, n)
		}
		if n := be.count("GET", typesPath+"/t1/names"); n != 1 {
			t.Errorf("%s: t1 names fetched %d times, want once", be.env, n)
		}
	}
}

func TestDerivedSectionsFlattenTheCrawl(t *testing.T) {
	be := &fakeBE{env: config.Prod, handle: func(method, path string, _ any) copilot.Response {
		if path == typesPath {
			return respond(200, j(t, `[{"typeUid":"t1","name":"Imaging"},{"name":"no uid, skipped"}]`))
		}
		return respond(200, j(t, `{"data":[{"name":"MRI","referredProviders":[{"name":"Dr A"},"junk"],"referredFacilities":[{"name":"RadNet"}]}]}`))
	}}
	out := getSettings(t.Context(), be, pick(t, "specialties", "referred-providers", "referred-facilities"), GetSettingsOpts{Env: config.Prod})
	want := []string{`[{"name":"MRI","type":"Imaging"}]`, `[{"name":"Dr A"}]`, `[{"name":"RadNet"}]`}
	for i, w := range want {
		if got := asJSON(t, out.Sections[i].Data); got != w {
			t.Errorf("%s = %s, want %s", out.Sections[i].Key, got, w)
		}
	}
}

// Every selected section x both envs used to fan out unbounded. Reads against one client are
// capped at readConcurrency, whatever the section count.
func TestReadsAgainstOneClientAreBoundedByTheSemaphore(t *testing.T) {
	be := &fakeBE{env: config.Prod, delay: 5 * time.Millisecond, handle: func(string, string, any) copilot.Response { return respond(200, []any{}) }}
	all, err := selectSections(nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// Many sections, each one a leaf GET: 19 sections would otherwise all be in flight together.
	getSettings(t.Context(), be, append(all, all...), GetSettingsOpts{Env: config.Prod})
	if peak := be.maxInflight.Load(); peak > readConcurrency || peak < 2 {
		t.Errorf("peak in-flight = %d, want between 2 and %d", peak, readConcurrency)
	}
}

func TestSourceGetHonoursContextCancellationWhileWaitingForASlot(t *testing.T) {
	be := &fakeBE{env: config.Prod, handle: func(string, string, any) copilot.Response { return respond(200, nil) }}
	src := NewSource(be)
	for range readConcurrency {
		src.sem <- struct{}{} // all slots taken
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := src.get(ctx, "/x"); err == nil {
		t.Error("get with every slot taken and a cancelled ctx must fail, not block")
	}
	if len(be.calls) != 0 {
		t.Errorf("calls = %v, want none", be.calls)
	}
}
