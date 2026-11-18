package copilot

import (
	"reflect"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

const (
	uHeart  = "11111111-1111-1111-1111-111111111111"
	uRegalC = "22222222-2222-2222-2222-222222222222"
	uRegalD = "33333333-3333-3333-3333-333333333333"
	uValley = "44444444-4444-4444-4444-444444444444"
)

// optionsClient serves the two option routes from synthetic data. "Cardiology" exists under two
// order types (one speciality; facilities unioned); "Heart Clinic" is under both.
func optionsClient() *fakeClient {
	fac := func(name, uid string) map[string]any { return map[string]any{"name": name, "referredFacilityUid": uid} }
	spec := func(name string, facs ...any) map[string]any {
		return map[string]any{"name": name, "specialityUid": "s-" + name, "referredFacilities": facs}
	}
	tree := []any{
		map[string]any{"name": "Referral", "typeUid": "t1", "specialities": []any{
			spec("Cardiology", fac("Heart Clinic", uHeart), fac("Regal Cardiology", uRegalC)),
			spec("Dermatology", fac("Regal Derm", uRegalD)),
		}},
		map[string]any{"name": "Imaging", "typeUid": "t2", "specialities": []any{
			spec("cardiology", fac("Heart Clinic", uHeart), fac("Valley", uValley)),
		}},
	}
	insurances := []any{
		map[string]any{"name": "CenCal"}, map[string]any{"name": "Medi-Cal"},
		map[string]any{"name": "Medicare"}, map[string]any{"name": "Medicare Advantage"},
	}
	return &fakeClient{env: config.Prod, handle: func(c call) Response {
		if strings.HasSuffix(c.Path, "/orders/locations") {
			return Response{Status: 200, Data: map[string]any{"locations": []any{"Arbor", "Vallarta", "Womens Health", "San Luis Obispo Casa", "San Luis Obispo Clinic"}}}
		}
		data := tree
		if strings.Contains(c.Path, "authorizationInsuranceNames") {
			data = insurances
		}
		return Response{Status: 200, Data: map[string]any{"data": data}}
	}}
}

func TestResolveOrderFilters(t *testing.T) {
	cases := []struct {
		name      string
		in        OrderFilterDimensions
		wantLoc   []string
		wantIns   []string
		wantRef   []string
		wantNotes []string
		wantErr   string
		wantCalls int
	}{
		{
			name: "location exact is case-insensitive", in: OrderFilterDimensions{Locations: []string{"vallarta"}},
			wantLoc: []string{"Vallarta"}, wantNotes: []string{"locations: vallarta → Vallarta"}, wantCalls: 1,
		},
		{
			name: "locations unique substring, union", in: OrderFilterDimensions{Locations: []string{"womens", "ARBOR"}},
			wantLoc: []string{"Womens Health", "Arbor"}, wantCalls: 1,
			wantNotes: []string{"locations: womens → Womens Health", "locations: ARBOR → Arbor"},
		},
		{name: "location ambiguous lists candidates", in: OrderFilterDimensions{Locations: []string{"san luis"}}, wantErr: "ambiguous; matches: San Luis Obispo Casa, San Luis Obispo Clinic", wantCalls: 1},
		{name: "location unknown lists every name", in: OrderFilterDimensions{Locations: []string{"Lompoc"}}, wantErr: "available: Arbor, Vallarta, Womens Health", wantCalls: 1},
		{
			name: "locations then insurances notes, one call each", in: OrderFilterDimensions{Locations: []string{"arbor"}, Insurances: []string{"cencal"}},
			wantLoc: []string{"Arbor"}, wantIns: []string{"CenCal"}, wantCalls: 2,
			wantNotes: []string{"locations: arbor → Arbor", "insurances: cencal → CenCal"},
		},
		{name: "nothing to resolve", in: OrderFilterDimensions{OrderType: []string{"x"}}},
		{name: "uids only make no extra calls", in: OrderFilterDimensions{ReferredTo: []string{uHeart}}, wantRef: []string{uHeart}},
		{
			name: "insurance exact is case-insensitive", in: OrderFilterDimensions{Insurances: []string{"cencal"}},
			wantIns: []string{"CenCal"}, wantNotes: []string{"insurances: cencal → CenCal"}, wantCalls: 1,
		},
		{
			name: "insurance exact beats a longer name containing it", in: OrderFilterDimensions{Insurances: []string{"medicare"}},
			wantIns: []string{"Medicare"}, wantNotes: []string{"insurances: medicare → Medicare"}, wantCalls: 1,
		},
		{
			name: "insurance unique substring", in: OrderFilterDimensions{Insurances: []string{"advantage", "CENCAL"}},
			wantIns: []string{"Medicare Advantage", "CenCal"}, wantCalls: 1,
			wantNotes: []string{"insurances: advantage → Medicare Advantage", "insurances: CENCAL → CenCal"},
		},
		{name: "insurance ambiguous lists candidates", in: OrderFilterDimensions{Insurances: []string{"medi"}}, wantErr: "ambiguous; matches: Medi-Cal, Medicare, Medicare Advantage", wantCalls: 1},
		{name: "insurance unknown lists every name", in: OrderFilterDimensions{Insurances: []string{"cencal health"}}, wantErr: "available: CenCal, Medi-Cal, Medicare, Medicare Advantage", wantCalls: 1},
		{
			name: "speciality unions equal names across order types", in: OrderFilterDimensions{Specialities: []string{"cardio"}},
			wantRef: []string{uHeart, uRegalC, uValley}, wantCalls: 1,
			wantNotes: []string{"specialities: cardio → Cardiology (3 facilities)"},
		},
		{name: "speciality unknown", in: OrderFilterDimensions{Specialities: []string{"podiatry"}}, wantErr: "available: Cardiology, Dermatology", wantCalls: 1},
		{
			name: "referredTo name expands to every containing facility", in: OrderFilterDimensions{ReferredTo: []string{"REGAL"}},
			wantRef: []string{uRegalC, uRegalD}, wantNotes: []string{"referredTo: REGAL → 2 facilities"}, wantCalls: 1,
		},
		{
			name: "referredTo uid and name union, deduped", in: OrderFilterDimensions{ReferredTo: []string{uHeart, "heart"}},
			wantRef: []string{uHeart}, wantNotes: []string{"referredTo: heart → 1 facilities"}, wantCalls: 1,
		},
		{name: "referredTo name matches nothing", in: OrderFilterDimensions{ReferredTo: []string{"nowhere"}}, wantErr: "matches no facility name", wantCalls: 1},
		{
			name: "specialities and referredTo intersect", in: OrderFilterDimensions{Specialities: []string{"cardiology"}, ReferredTo: []string{"regal"}},
			wantRef: []string{uRegalC}, wantCalls: 1,
			wantNotes: []string{"specialities: cardiology → Cardiology (3 facilities)", "referredTo: regal → 2 facilities", "referredTo ∩ specialities → 1 facilities"},
		},
		{name: "empty intersection is an error", in: OrderFilterDimensions{Specialities: []string{"dermatology"}, ReferredTo: []string{uHeart}}, wantErr: "share no facility", wantCalls: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := optionsClient()
			got, notes, err := ResolveOrderFilters(t.Context(), f, c.in)
			if len(f.calls) != c.wantCalls {
				t.Errorf("option calls = %d (%v), want %d", len(f.calls), f.paths(), c.wantCalls)
			}
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Locations, orNil(c.wantLoc, c.in.Locations)) {
				t.Errorf("locations = %v, want %v", got.Locations, c.wantLoc)
			}
			if !reflect.DeepEqual(got.Insurances, orNil(c.wantIns, c.in.Insurances)) || !reflect.DeepEqual(got.ReferredTo, c.wantRef) {
				t.Errorf("insurances = %v, referredTo = %v; want %v, %v", got.Insurances, got.ReferredTo, c.wantIns, c.wantRef)
			}
			if got.Specialities != nil {
				t.Errorf("Specialities = %v, want consumed", got.Specialities)
			}
			if !reflect.DeepEqual(notes, c.wantNotes) {
				t.Errorf("notes = %v, want %v", notes, c.wantNotes)
			}
		})
	}
}

// orNil is the expected resolved list: the resolved list, or the untouched input when none expected.
func orNil(want, in []string) []string {
	if want != nil {
		return want
	}
	return in
}

func TestFetchLocationsReadsThePlainLocationsObject(t *testing.T) {
	f := optionsClient()
	got, err := FetchLocations(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0] != "Arbor" || len(f.paths()) != 1 || f.paths()[0] != "GET /api/v1/orders/locations" {
		t.Errorf("got %v via %v", got, f.paths())
	}
}

func TestFetchSpecialitiesFlattensTheHierarchyWithItsOrderType(t *testing.T) {
	got, err := FetchSpecialities(t.Context(), optionsClient())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].OrderType != "Imaging" || got[0].SpecialityUID != "s-Cardiology" || len(got[0].Facilities) != 2 || got[0].Facilities[1].ReferredFacilityUID != uRegalC {
		t.Errorf("got %+v", got)
	}
}

func TestFetchSpecialitiesRequestsTheEncodedOrderMode(t *testing.T) {
	f := optionsClient()
	if _, err := FetchSpecialities(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	want := "GET /api/v1/orders/types/hierarchy?orderMode=%5B%22orders_only_mode%22%2C%22pcp_notes_mode%22%5D"
	if len(f.paths()) != 1 || f.paths()[0] != want {
		t.Errorf("paths = %v, want %s", f.paths(), want)
	}
}
