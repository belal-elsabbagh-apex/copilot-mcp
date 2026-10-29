package sweep

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

// fakeBE serves login plus /orders/filter pages from pages (page i -> pages[i], empty beyond).
type fakeBE struct {
	mu        sync.Mutex
	requested []int
	failPage  int // -1 = none
}

func useFakeBE(t *testing.T, pages [][]map[string]any) *fakeBE {
	t.Helper()
	be := &fakeBE{failPage: -1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/orders/filter" {
			claims, _ := json.Marshal(map[string]any{"id": 1, "timestamp": time.Now().Add(time.Hour).UnixMilli()})
			fmt.Fprintf(w, `{"token":"h.%s.s"}`, b64(claims))
			return
		}
		var body struct{ PageNumber int }
		_ = json.NewDecoder(r.Body).Decode(&body)
		be.mu.Lock()
		be.requested = append(be.requested, body.PageNumber)
		fail := be.failPage == body.PageNumber
		be.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		rows := []map[string]any{}
		if body.PageNumber < len(pages) {
			rows = pages[body.PageNumber]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
	}))
	t.Cleanup(srv.Close)

	creds := map[string]any{"be": srv.URL, "email": "e@example.com", "password": "pw"}
	cfg, _ := json.Marshal(map[string]any{
		"copilot": map[string]any{"sessionCache": false, "prod": creds, "pre_prod": creds},
		"uipath":  map[string]any{"orchestratorUrl": "https://cloud.uipath.com/o/t/orchestrator_", "bearer": "x"},
	})
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COPILOT_MCP_CONFIG", path)
	config.ResetForTest()
	copilot.ResetSessionCacheForTest()
	t.Cleanup(func() { config.ResetForTest(); copilot.ResetSessionCacheForTest() })
	return be
}

func (be *fakeBE) pagesRequested() []int {
	be.mu.Lock()
	defer be.mu.Unlock()
	return slices.Sorted(slices.Values(be.requested))
}

func order(uid, status string, extra map[string]any) map[string]any {
	o := map[string]any{"orderUid": uid, "status": status, "orderType": map[string]any{"name": "Outbound Referral"}}
	for k, v := range extra {
		o[k] = v
	}
	return o
}

func TestFindStuckOrdersFlagsOnlyConfiguredStatusesCaseInsensitively(t *testing.T) {
	useFakeBE(t, [][]map[string]any{{order("a", "inProgress", nil), order("b", "completed", nil), order("c", "PENDING", nil)}})
	res, err := FindStuckOrders(t.Context(), Args{Env: config.Prod})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 3 || res.Found != 2 || res.Stuck[0].OrderUID.V != "a" || res.Stuck[1].OrderUID.V != "c" {
		t.Errorf("result = scanned %d found %d stuck %+v, want 3 scanned and a,c flagged", res.Scanned, res.Found, res.Stuck)
	}
	if want := []string{"inprogress", "incomplete", "pending"}; !slices.Equal(res.Statuses, want) {
		t.Errorf("statuses = %v, want lowercased defaults %v", res.Statuses, want)
	}
}

func TestFindStuckOrdersOlderThanHoursNeedsAParsableAgeAtLeastThatOld(t *testing.T) {
	prev := now
	now = func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = prev })
	useFakeBE(t, [][]map[string]any{{
		order("old", "pending", map[string]any{"orderDate": "2026-08-18T12:00:00Z"}),
		order("fresh", "pending", map[string]any{"orderDate": "2026-08-20T11:00:00Z"}),
		order("undated", "pending", nil),
	}})
	res, err := FindStuckOrders(t.Context(), Args{Env: config.Prod, OlderThanHours: 24})
	if err != nil {
		t.Fatal(err)
	}
	if res.Found != 1 || res.Stuck[0].OrderUID.V != "old" || *res.Stuck[0].AgeHours != 48 {
		t.Errorf("stuck = %+v, want only 'old' at 48h", res.Stuck)
	}
}

func TestFindStuckOrdersStopsAtTheFirstEmptyPageAndDiscardsLaterOnes(t *testing.T) {
	be := useFakeBE(t, [][]map[string]any{{order("a", "pending", nil)}, {order("b", "pending", nil)}})
	var steps []string
	res, err := FindStuckOrders(t.Context(), Args{Env: config.Prod, ScanPages: 20, OnProgress: func(done, total int, label string) {
		steps = append(steps, fmt.Sprintf("%d/%d %s", done, total, label))
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 2 || res.Found != 2 {
		t.Errorf("scanned/found = %d/%d, want 2/2", res.Scanned, res.Found)
	}
	// Page 2 is the first empty one; the window prefetches at most pages 0..5.
	if got := be.pagesRequested(); got[0] != 0 || got[len(got)-1] > 5 {
		t.Errorf("pages requested = %v, want 0.. within the window of 4 past the current page", got)
	}
	if want := []string{"1/20 page 1: 1 scanned, 1 stuck", "2/20 page 2: 2 scanned, 2 stuck"}; !slices.Equal(steps, want) {
		t.Errorf("progress = %v, want %v", steps, want)
	}
}

func TestFindStuckOrdersSurfacesAPageErrorWhenItsTurnComes(t *testing.T) {
	be := useFakeBE(t, [][]map[string]any{{order("a", "pending", nil)}, {order("b", "pending", nil)}})
	be.failPage = 1
	if _, err := FindStuckOrders(t.Context(), Args{Env: config.Prod}); err == nil {
		t.Error("err = nil, want the page-1 failure")
	}
}

func TestFindStuckOrdersResultMarshalsEmptyStuckAsArrayAndOmitsAbsentFields(t *testing.T) {
	useFakeBE(t, [][]map[string]any{{map[string]any{"orderUid": "a", "status": "pending"}}})
	res, err := FindStuckOrders(t.Context(), Args{Env: config.Prod})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if want := `{"env":"prod","scanned":1,"statuses":["inprogress","incomplete","pending"],"found":1,"stuck":[{"orderUid":"a","status":"pending","ageHours":null}]}`; string(b) != want {
		t.Errorf("json = %s\nwant   %s", b, want)
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestParseDateReadsTheBEDisplayFormatAsLocalTime(t *testing.T) {
	got, ok := parseDate("Oct 14, 2025")
	if want := time.Date(2025, 10, 14, 0, 0, 0, 0, time.Local); !ok || !got.Equal(want) {
		t.Errorf("parseDate = %v, %v; want %v", got, ok, want)
	}
	got, ok = parseDate("2025-10-14T08:30:00")
	if want := time.Date(2025, 10, 14, 8, 30, 0, 0, time.Local); !ok || !got.Equal(want) {
		t.Errorf("zoneless date-time = %v, %v; want local %v", got, ok, want)
	}
	if _, ok := parseDate("not a date"); ok {
		t.Error("garbage must not parse")
	}
}

func TestStuckOrderKeepsNullInsuranceDistinctFromAbsent(t *testing.T) {
	o := copilot.BeOrder{"insurance": map[string]any{"name": nil}}
	b, _ := json.Marshal(StuckOrder{Insurance: o.Pass("insurance", "name"), Facility: o.Pass("referredFacility", "name")})
	if want := `{"insurance":null,"ageHours":null}`; string(b) != want {
		t.Errorf("got %s want %s", b, want)
	}
}
