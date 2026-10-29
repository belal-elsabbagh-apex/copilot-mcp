package copilot

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildFilterDimensionsDefaultsTypeAndAlwaysSendsOrderMode(t *testing.T) {
	want := `{"type":"Outbound Referral","orderMode":"[\"orders_only_mode\",\"pcp_notes_mode\"]"}`
	if got := asJSON(t, BuildFilterDimensions(OrderFilterDimensions{})); got != want {
		t.Errorf("got %s", got)
	}
	if got := BuildFilterDimensions(OrderFilterDimensions{Type: "PCP Notes"}); got.Type != "PCP Notes" {
		t.Errorf("type = %q, want the caller's", got.Type)
	}
}

func TestBuildFilterDimensionsJSONEncodesArraysAndOmitsEmptyOnes(t *testing.T) {
	d := BuildFilterDimensions(OrderFilterDimensions{Locations: []string{"OSSM COR", "OSSM RIV"}, Insurances: []string{"A & B"}, ReferredTo: []string{}})
	if d.Locations != `["OSSM COR","OSSM RIV"]` || d.Insurances != `["A & B"]` {
		t.Errorf("locations = %s, insurances = %s (no HTML escaping, like JSON.stringify)", d.Locations, d.Insurances)
	}
	if got := asJSON(t, d); strings.Contains(got, "referredTo") {
		t.Errorf("an empty array must be omitted: %s", got)
	}
}

func TestBuildFilterDimensionsSendsExplicitFalseButOmitsUnsetBools(t *testing.T) {
	no, yes := false, true
	got := asJSON(t, BuildFilterDimensions(OrderFilterDimensions{MRN: "12345", HasAuthScreenshot: &yes, SendFax: &no}))
	for _, want := range []string{`"mrn":"12345"`, `"hasAuthScreenshot":true`, `"sendFax":false`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s lacks %s", got, want)
		}
	}
	if strings.Contains(got, "missingNotes") {
		t.Errorf("unset bool leaked: %s", got)
	}
}

func TestBuildFilterBodyPagingDefaultsAndOverrides(t *testing.T) {
	b := BuildFilterBody(OrderFilterDimensions{}, nil, nil)
	if b.PageSize != 100 || b.PageNumber != 1 {
		t.Errorf("defaults = %d/%d", b.PageSize, b.PageNumber)
	}
	size, num := 50, 0
	if b = BuildFilterBody(OrderFilterDimensions{}, &size, &num); b.PageSize != 50 || b.PageNumber != 0 {
		t.Errorf("explicit page 0 must survive: %d/%d", b.PageSize, b.PageNumber)
	}
	if got := asJSON(t, b); !strings.HasPrefix(got, `{"pageSize":50,"pageNumber":0,"type"`) {
		t.Errorf("body = %s", got)
	}
}

func TestSlimOrderMapsAFacilityReferralWithNoPatient(t *testing.T) {
	o := BeOrder{
		"orderUid": "u1", "creationDate": "2026-07-01T00:00:00Z", "status": "forReview", "category": "For Review",
		"orderType": map[string]any{"name": "Consultation Referral"}, "insurance": map[string]any{"name": "OPTUM CARE NETWORK"},
		"speciality": map[string]any{"name": "Cardiology"}, "requiredAuthorization": true, "authStatus": "approved",
		"uploadStatusAuth": "completed", "uploadStatusFax": "not_started",
		"referredFacility": map[string]any{"name": "Regal Medical Group", "NPI": "1234567890", "external": true},
		"patient":          map[string]any{"patientName": "DOE, JANE"},
	}
	want := `{"orderUid":"u1","creationDate":"2026-07-01T00:00:00Z","status":"forReview","category":"For Review","orderType":"Consultation Referral","insurance":"OPTUM CARE NETWORK","speciality":"Cardiology","referredTo":{"name":"Regal Medical Group","npi":"1234567890","external":true},"authRequired":true,"authStatus":"approved","uploadStatusAuth":"completed","uploadStatusFax":"not_started"}`
	if got := asJSON(t, SlimOrder(o)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSlimOrderFallsBackToReferredProvider(t *testing.T) {
	row := SlimOrder(BeOrder{"orderUid": "u2", "referredProvider": map[string]any{"name": "Dr. Smith", "NPI": "9999999999"}})
	if got := asJSON(t, row.ReferredTo); got != `{"name":"Dr. Smith","npi":"9999999999"}` {
		t.Errorf("referredTo = %s", got)
	}
	if SlimOrder(BeOrder{"orderUid": "u3"}).ReferredTo != nil {
		t.Error("no facility or provider: referredTo must be omitted")
	}
}

func TestStripFilterEchoedFields(t *testing.T) {
	ins, typ := "OPTUM", "Consult"
	row := func() SlimOrderRow {
		return SlimOrderRow{Insurance: shared.Field{V: ins, Set: true}, OrderType: shared.Field{V: typ, Set: true}}
	}
	cases := []struct {
		name             string
		args             OrderFilterDimensions
		wantIns, wantTyp bool
	}{
		{"one insurance", OrderFilterDimensions{Insurances: []string{"OPTUM"}}, false, true},
		{"one order type", OrderFilterDimensions{OrderType: []string{"Consult"}}, true, false},
		{"unfiltered", OrderFilterDimensions{}, true, true},
		{"several values", OrderFilterDimensions{Insurances: []string{"A", "B"}, OrderType: []string{"C", "D"}}, true, true},
		{"both single", OrderFilterDimensions{Insurances: []string{"A"}, OrderType: []string{"C"}}, false, false},
	}
	for _, c := range cases {
		got := StripFilterEchoedFields(row(), c.args)
		if got.Insurance.Set != c.wantIns || got.OrderType.Set != c.wantTyp {
			t.Errorf("%s: insurance kept = %v, orderType kept = %v", c.name, got.Insurance.Set, got.OrderType.Set)
		}
	}
}

func TestSearchOrdersLogsInPostsTheBuiltBodyAndMapsRows(t *testing.T) {
	be := setup(t, map[string]any{"sessionCache": false})
	be.handler = func(w http.ResponseWriter, p string) {
		if p == "/api/v1/orders/filter" {
			w.Write([]byte(`{"data":[{"orderUid":"u1","status":"forReview","insurance":{"name":"OPTUM CARE NETWORK"},"referredFacility":{"name":"Regal","NPI":"111"}}],"totalNumberOfElements":1}`))
			return
		}
		defaultHandler(w, p)
	}
	out, err := SearchOrders(t.Context(), SearchOrdersArgs{Env: config.PreProd, Profile: "ossm", OrderFilterDimensions: OrderFilterDimensions{Insurances: []string{"OPTUM CARE NETWORK"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.paths(), []string{loginPath, "/api/v1/orders/filter"}) {
		t.Fatalf("paths = %v", be.paths())
	}
	if be.bodies[1]["insurances"] != `["OPTUM CARE NETWORK"]` || be.bodies[1]["type"] != "Outbound Referral" {
		t.Errorf("body = %v", be.bodies[1])
	}
	want := `{"env":"pre_prod","profile":"ossm","pageSize":100,"pageNumber":1,"totalNumberOfElements":1,"count":1,"rows":[{"orderUid":"u1","status":"forReview","referredTo":{"name":"Regal","npi":"111"}}]}`
	if got := asJSON(t, out); got != want {
		t.Errorf("got  %s\nwant %s (insurance is echoed by the filter, so stripped)", got, want)
	}
}

func TestGetOrderCategoryStatsPostsNoPagingFields(t *testing.T) {
	be := setup(t, map[string]any{"sessionCache": false})
	be.handler = func(w http.ResponseWriter, p string) {
		if p == "/api/v1/orders/category/stats" {
			w.Write([]byte(`{"For Review":{"new":2}}`))
			return
		}
		defaultHandler(w, p)
	}
	out, err := GetOrderCategoryStats(t.Context(), CategoryStatsArgs{Env: config.PreProd, Profile: "ossm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := be.bodies[1]["pageSize"]; ok || be.reqs[1] != "/api/v1/orders/category/stats" {
		t.Errorf("reqs = %v, body = %v", be.reqs, be.bodies[1])
	}
	if !reflect.DeepEqual(out, map[string]any{"For Review": map[string]any{"new": 2.0}}) {
		t.Errorf("out = %v", out)
	}
}

func TestSlimOrderReferredToFollowsJSNullishCoalescing(t *testing.T) {
	cases := map[string]struct {
		order BeOrder
		want  string
	}{
		"numeric NPI becomes a string": {BeOrder{"referredFacility": map[string]any{"NPI": float64(1234567893)}}, `{"npi":"1234567893"}`},
		"null last operand stays null": {BeOrder{"referredFacility": map[string]any{"NPI": nil}, "referredProvider": map[string]any{"NPI": nil}}, `{"npi":null}`},
		"absent NPI is omitted":        {BeOrder{"referredFacility": map[string]any{"name": "A"}}, `{"name":"A"}`},
		"provider fills nullish name":  {BeOrder{"referredFacility": map[string]any{"name": nil}, "referredProvider": map[string]any{"name": "P"}}, `{"name":"P"}`},
	}
	for name, c := range cases {
		b, err := json.Marshal(SlimOrder(c.order).ReferredTo)
		if err != nil || string(b) != c.want {
			t.Errorf("%s: got %s (%v), want %s", name, b, err, c.want)
		}
	}
}
