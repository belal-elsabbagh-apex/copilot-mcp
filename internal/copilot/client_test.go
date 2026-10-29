package copilot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

func rows(rs ...any) any { return map[string]any{"data": rs} }

func TestFilterOrdersReturnsRowsAndTotal(t *testing.T) {
	f := queued("", Response{Status: 200, Data: map[string]any{"data": []any{map[string]any{"orderUid": "u1"}}, "totalNumberOfElements": float64(42)}})
	page, err := FilterOrders(t.Context(), f, OrderFilterBody{PageSize: 50, PageNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if f.paths()[0] != "POST /api/v1/orders/filter" {
		t.Errorf("call = %v", f.paths())
	}
	if len(page.Rows) != 1 || page.Rows[0].Str("orderUid") != "u1" || page.Total == nil || *page.Total != 42 {
		t.Errorf("page = %+v", page)
	}
	b, _ := json.Marshal(f.calls[0].Body.JSON)
	if string(b) != `{"pageSize":50,"pageNumber":1}` {
		t.Errorf("body = %s, want paging only (zero dimensions omitted)", b)
	}
}

func TestFilterOrdersOmitsTotalWhenNotNumeric(t *testing.T) {
	page, err := FilterOrders(t.Context(), queued("", Response{Status: 200, Data: rows()}), OrderFilterBody{})
	if err != nil || page.Total != nil || len(page.Rows) != 0 {
		t.Errorf("page = %+v, err = %v", page, err)
	}
}

func TestFilterOrdersErrorsOnNon2xx(t *testing.T) {
	_, err := FilterOrders(t.Context(), queued("", Response{Status: 500, Text: "boom"}), OrderFilterBody{})
	if err == nil || !strings.Contains(err.Error(), "/orders/filter failed 500") {
		t.Errorf("err = %v", err)
	}
}

func TestCategoryStatsPassesResponseThrough(t *testing.T) {
	f := queued("", Response{Status: 200, Data: map[string]any{"For Review": map[string]any{"new": 3.0}}})
	out, err := CategoryStats(t.Context(), f, FilterDimensions{Type: "Outbound Referral"})
	if err != nil || !reflect.DeepEqual(out, map[string]any{"For Review": map[string]any{"new": 3.0}}) {
		t.Errorf("out = %v, err = %v", out, err)
	}
	if f.paths()[0] != "POST /api/v1/orders/category/stats" {
		t.Errorf("call = %v", f.paths())
	}
	if _, err := CategoryStats(t.Context(), queued("", Response{Status: 404, Text: "nope"}), FilterDimensions{}); err == nil || !strings.Contains(err.Error(), "failed 404") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchOrder(t *testing.T) {
	o, err := FetchOrder(t.Context(), queued("", Response{Status: 200, Data: rows(map[string]any{"orderUid": "u1"})}), "u1")
	if err != nil || o.Str("orderUid") != "u1" {
		t.Errorf("o = %v, err = %v", o, err)
	}
	if _, err := FetchOrder(t.Context(), queued("", Response{Status: 200, Data: rows()}), "missing"); err == nil || !strings.Contains(err.Error(), "not found in this env") {
		t.Errorf("err = %v, want not found", err)
	}
	if _, err := FetchOrder(t.Context(), queued("", Response{Status: 500, Text: "boom"}), "u1"); err == nil || !strings.Contains(err.Error(), "/orders/filter failed 500") {
		t.Errorf("err = %v", err)
	}
}

func TestVerifySwallowsNotFoundAndHTTPErrors(t *testing.T) {
	v := Verify(t.Context(), queued("", Response{Status: 200, Data: rows(map[string]any{"status": "forReview"})}), "u1")
	if v == nil || v.Status.V != "forReview" {
		t.Errorf("v = %+v", v)
	}
	if Verify(t.Context(), queued("", Response{Status: 200, Data: rows()}), "x") != nil {
		t.Error("not found should verify to nil")
	}
	if Verify(t.Context(), queued("", Response{Status: 500, Text: "boom"}), "x") != nil {
		t.Error("a non-2xx should verify to nil, not an error")
	}
}

func TestNormalizeOrderIsTheStatusSubset(t *testing.T) {
	o := BeOrder{
		"status": "forReview", "patient": map[string]any{"patientName": "DOE"},
		"insurance": map[string]any{"name": "Acme", "memberId": "M1"}, "requiredAuthorization": false,
		"ICDCodes": []any{map[string]any{"code": "A1"}, map[string]any{}}, "latestProgressNote": map[string]any{},
	}
	b, _ := json.Marshal(NormalizeOrder(o))
	want := `{"status":"forReview","patient":"DOE","insurance":"Acme","memberId":"M1","requiredAuthorization":false,"icds":["A1",""],"cpts":[],"notePresent":true}`
	if string(b) != want {
		t.Errorf("NormalizeOrder = %s\nwant %s", b, want)
	}
}

func TestReqWithRefreshRetriesOnceOnMissingToken(t *testing.T) {
	f := queued("", Response{Status: 403, Text: "missing_token"}, Response{Status: 200}, Response{Status: 200, Data: map[string]any{"ok": true}})
	r, err := ReqWithRefresh(t.Context(), f, "PUT", "/api/v1/orders/u1", ReqBody{JSON: map[string]any{"a": 1}})
	if err != nil || r.Status != 200 {
		t.Fatalf("r = %+v, err = %v", r, err)
	}
	want := []string{"PUT /api/v1/orders/u1", "GET /api/v1/physician/refresh", "PUT /api/v1/orders/u1"}
	if !slices.Equal(f.paths(), want) {
		t.Errorf("calls = %v, want %v", f.paths(), want)
	}
}

func TestReqWithRefreshDoesNotRetryOtherForbiddens(t *testing.T) {
	f := queued("", Response{Status: 403, Text: "forbidden"})
	r, _ := ReqWithRefresh(t.Context(), f, "PUT", "/x", ReqBody{})
	if r.Status != 403 || len(f.calls) != 1 {
		t.Errorf("status = %d, calls = %v; want one call", r.Status, f.paths())
	}
}

func TestSubmitOrderRetriesThenVerifies(t *testing.T) {
	noWaits(t)
	f := queued(config.PreProd,
		Response{Status: 403, Text: "missing_token"}, Response{Status: 200}, Response{Status: 200},
		Response{Status: 200, Data: rows(map[string]any{"status": "inProgress"})})
	v, err := SubmitOrder(t.Context(), f, "u1")
	if err != nil || v == nil || v.Status.V != "inProgress" {
		t.Fatalf("v = %+v, err = %v", v, err)
	}
	want := []string{"POST /api/v1/orders/u1/submit", "GET /api/v1/physician/refresh", "POST /api/v1/orders/u1/submit", "POST /api/v1/orders/filter"}
	if !slices.Equal(f.paths(), want) {
		t.Errorf("calls = %v, want %v", f.paths(), want)
	}
}

func TestSubmitOrderRefusesNonPreProdBeforeAnyHTTP(t *testing.T) {
	for _, env := range []config.Env{config.Prod, ""} {
		f := queued(env)
		if _, err := SubmitOrder(t.Context(), f, "u1"); err == nil || !strings.Contains(err.Error(), "pre_prod-only") || len(f.calls) != 0 {
			t.Errorf("env %q: err = %v, calls = %v; want refusal with no HTTP", env, err, f.paths())
		}
	}
}

func TestAssertPreProdClientNamesTheTag(t *testing.T) {
	if err := AssertPreProdClient(queued(config.PreProd), "x"); err != nil {
		t.Error(err)
	}
	if err := AssertPreProdClient(queued(config.Prod), "x"); err == nil || !strings.Contains(err.Error(), "tagged 'prod'") {
		t.Errorf("err = %v", err)
	}
	if err := AssertPreProdClient(queued(""), "x"); err == nil || !strings.Contains(err.Error(), "untagged") {
		t.Errorf("err = %v", err)
	}
}

func TestNewHTTPClientTagsEnv(t *testing.T) {
	if newHTTPClient("http://x", config.PreProd, nil).Env() != config.PreProd || newHTTPClient("http://x", "", nil).Env() != "" {
		t.Error("MakeClient must carry its env tag (untagged stays untagged)")
	}
}

func TestNewHTTPClientParsesBodiesAndRoundTripsCookies(t *testing.T) {
	var gotCookie, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc"})
			w.Write([]byte(`{"ok":true}`))
		case "/text":
			w.Write([]byte("not json"))
		case "/echo":
			gotCookie, gotType = r.Header.Get("Cookie"), r.Header.Get("Content-Type")
		default:
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	c := newHTTPClient(srv.URL, config.Prod, nil)
	if r, _ := c.Req(t.Context(), "GET", "/json", ReqBody{}); !reflect.DeepEqual(r.Data, map[string]any{"ok": true}) {
		t.Errorf("json data = %#v", r.Data)
	}
	if r, _ := c.Req(t.Context(), "GET", "/text", ReqBody{}); r.Data != "not json" {
		t.Errorf("text data = %#v, want the raw text", r.Data)
	}
	if r, _ := c.Req(t.Context(), "DELETE", "/empty", ReqBody{}); r.Data != nil {
		t.Errorf("empty body data = %#v, want nil", r.Data)
	}
	c.Req(t.Context(), "POST", "/echo", ReqBody{JSON: map[string]any{"a": "<&>"}})
	if gotCookie != "sid=abc" || gotType != "application/json" {
		t.Errorf("cookie = %q, content-type = %q", gotCookie, gotType)
	}
}

func TestToMDY(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "1/2/1955": "01/02/1955", "1955-06-15": "06/15/1955", "Jun 15, 1955": "06/15/1955",
	} {
		if got, err := ToMDY(in); err != nil || got != want {
			t.Errorf("ToMDY(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"2006-1-2": "01/02/2006", "1-2-2006": "01/02/2006", "01/02/06": "01/02/2006",
		"06/15/55": "06/15/1955", "6/15/49": "06/15/2049", "6/15/50": "06/15/1950", "6/15/00": "06/15/2000",
	} {
		if got, err := ToMDY(in); err != nil || got != want {
			t.Errorf("ToMDY(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// Non-empty but blank is an error (TS: new Date("") is invalid), not "no date".
	if _, err := ToMDY("  "); err == nil || !strings.Contains(err.Error(), "unrecognized date") {
		t.Errorf("whitespace-only err = %v", err)
	}
	if _, err := ToMDY("garbage"); err == nil || !strings.Contains(err.Error(), "unrecognized date garbage") {
		t.Errorf("err = %v", err)
	}
}

func TestNormalizeOrderKeepsNullDistinctFromAbsent(t *testing.T) {
	o := BeOrder{"status": nil, "insurance": nil, "submissionStatus": nil, "appointmentDate": nil, "orderType": map[string]any{"name": nil}}
	b, _ := json.Marshal(NormalizeOrder(o))
	want := `{"status":null,"submissionStatus":null,"type":null,"appointmentDate":null,"icds":[],"cpts":[],"notePresent":false}`
	if string(b) != want {
		t.Errorf("NormalizeOrder = %s\nwant %s", b, want)
	}
}
