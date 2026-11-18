package copilot

// The values search_orders / get_order_category_stats filters resolve against: the account's
// insurance names and its speciality -> facility tree, exactly what the Copilot FE offers. The BE
// matches insurances by exact (case-insensitive) name and has no speciality param at all — a
// speciality is just the set of its facility uids sent as `referredTo` — so friendly names are
// resolved here, before the request body is built.

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// FacilityOption is one referred facility under a speciality.
type FacilityOption struct {
	Name                string `json:"name"`
	ReferredFacilityUID string `json:"referredFacilityUid"`
}

// SpecialityOption is one speciality of one order type, with every facility under it.
type SpecialityOption struct {
	Name          string           `json:"name"`
	SpecialityUID string           `json:"specialityUid"`
	OrderType     string           `json:"orderType"`
	Facilities    []FacilityOption `json:"facilities"`
}

// uuidRe is a full 8-4-4-4-12 hex UUID: a referredTo value of this shape is a uid, not a name.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func getRows(ctx context.Context, c Client, path string) ([]any, error) {
	r, err := c.Req(ctx, "GET", path, ReqBody{})
	if err != nil {
		return nil, err
	}
	if r.Status >= 400 {
		return nil, fmt.Errorf("%s failed %d: %s", path, r.Status, shared.SliceUTF16(r.Text, 300))
	}
	return shared.EnvelopeRows(r.Data), nil
}

// FetchInsuranceNames is GET /orders/authorizationInsuranceNames, the FE's insurance option list.
func FetchInsuranceNames(ctx context.Context, c Client) ([]string, error) {
	rows, err := getRows(ctx, c, "/api/v1/orders/authorizationInsuranceNames")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if n := shared.Str(row, "name"); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// FetchLocations is GET /orders/locations, the FE's location option list (`{locations: string[]}`,
// not the usual data envelope).
func FetchLocations(ctx context.Context, c Client) ([]string, error) {
	r, err := c.Req(ctx, "GET", "/api/v1/orders/locations", ReqBody{})
	if err != nil {
		return nil, err
	}
	if r.Status >= 400 {
		return nil, fmt.Errorf("/api/v1/orders/locations failed %d: %s", r.Status, shared.SliceUTF16(r.Text, 300))
	}
	rows, _ := shared.Prop(r.Data, "locations").([]any)
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		if n, _ := row.(string); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// FetchSpecialities is GET /orders/types/hierarchy, flattened to one entry per (order type,
// speciality) with its facilities.
func FetchSpecialities(ctx context.Context, c Client) ([]SpecialityOption, error) {
	rows, err := getRows(ctx, c, "/api/v1/orders/types/hierarchy?orderMode="+shared.URIComponent(OrderMode))
	if err != nil {
		return nil, err
	}
	var out []SpecialityOption
	for _, typ := range rows {
		specs, _ := shared.Prop(typ, "specialities").([]any)
		for _, sp := range specs {
			opt := SpecialityOption{
				Name: shared.Str(sp, "name"), SpecialityUID: shared.Str(sp, "specialityUid"),
				OrderType: shared.Str(typ, "name"), Facilities: []FacilityOption{},
			}
			facs, _ := shared.Prop(sp, "referredFacilities").([]any)
			for _, f := range facs {
				opt.Facilities = append(opt.Facilities, FacilityOption{Name: shared.Str(f, "name"), ReferredFacilityUID: shared.Str(f, "referredFacilityUid")})
			}
			out = append(out, opt)
		}
	}
	return out, nil
}

// pickName resolves one user value to a registry name: a case-insensitive exact match wins, else
// the ONE name containing the value. Zero matches lists every name; several list the candidates.
func pickName(kind, value string, names []string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", shared.Expectedf("%s: empty value", kind)
	}
	if i := slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, value) }); i >= 0 {
		return names[i], nil
	}
	lv := strings.ToLower(value)
	hits := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return !strings.Contains(strings.ToLower(n), lv) })
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", shared.Expectedf("%s %q matches nothing; available: %s", kind, value, strings.Join(names, ", "))
	}
	return "", shared.Expectedf("%s %q is ambiguous; matches: %s", kind, value, strings.Join(hits, ", "))
}

// dedupe drops repeated values, keeping first-seen order.
func dedupe(v []string) []string {
	seen := make(map[string]struct{}, len(v))
	out := make([]string, 0, len(v))
	for _, s := range v {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// resolveNames maps each value to its registry name; kind is the singular for errors, field the
// plural used in the resolved notes.
func resolveNames(kind, field string, values, names []string) ([]string, []string, error) {
	var out, notes []string
	for _, v := range values {
		n, err := pickName(kind, v, names)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, n)
		notes = append(notes, fmt.Sprintf("%s: %s → %s", field, v, n))
	}
	return dedupe(out), notes, nil
}

// resolveSpecialities maps speciality names to the union of their facility uids. Equal names
// across order types are one speciality.
func resolveSpecialities(values []string, tree []SpecialityOption) ([]string, []string, error) {
	var names []string
	facs := map[string][]string{}
	for _, sp := range tree {
		k := strings.ToLower(sp.Name)
		if _, ok := facs[k]; !ok {
			names = append(names, sp.Name)
			facs[k] = nil
		}
		for _, f := range sp.Facilities {
			facs[k] = append(facs[k], f.ReferredFacilityUID)
		}
	}
	var uids, notes []string
	for _, v := range values {
		n, err := pickName("speciality", v, names)
		if err != nil {
			return nil, nil, err
		}
		own := dedupe(facs[strings.ToLower(n)])
		if len(own) == 0 {
			return nil, nil, shared.Expectedf("speciality %q has no facilities", n)
		}
		uids = append(uids, own...)
		notes = append(notes, fmt.Sprintf("specialities: %s → %s (%d facilities)", v, n, len(own)))
	}
	return dedupe(uids), notes, nil
}

// resolveReferredTo passes full UUIDs through and expands any other value to every facility whose
// name contains it (case-insensitive).
func resolveReferredTo(values []string, tree []SpecialityOption) ([]string, []string, error) {
	var uids, notes []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, nil, shared.Expectedf("referredTo: empty value")
		}
		if uuidRe.MatchString(v) {
			uids = append(uids, v)
			continue
		}
		lv := strings.ToLower(v)
		var hits []string
		for _, sp := range tree {
			for _, f := range sp.Facilities {
				if strings.Contains(strings.ToLower(f.Name), lv) {
					hits = append(hits, f.ReferredFacilityUID)
				}
			}
		}
		hits = dedupe(hits)
		if len(hits) == 0 {
			return nil, nil, shared.Expectedf("referredTo %q is not a full uid and matches no facility name (see get_order_filter_options)", v)
		}
		uids = append(uids, hits...)
		notes = append(notes, fmt.Sprintf("referredTo: %s → %d facilities", v, len(hits)))
	}
	return dedupe(uids), notes, nil
}

// ResolveOrderFilters rewrites the friendly locations / insurances / specialities / referredTo inputs into the
// exact values the BE matches (registry-spelled locations and insurances, facility uids; Specialities is
// consumed). The option routes are fetched only when some value needs them. The notes say, one per
// input value, what was sent.
func ResolveOrderFilters(ctx context.Context, c Client, a OrderFilterDimensions) (OrderFilterDimensions, []string, error) {
	var notes []string
	if len(a.Locations) > 0 {
		names, err := FetchLocations(ctx, c)
		if err != nil {
			return a, nil, err
		}
		if a.Locations, notes, err = resolveNames("location", "locations", a.Locations, names); err != nil {
			return a, nil, err
		}
	}
	if len(a.Insurances) > 0 {
		names, err := FetchInsuranceNames(ctx, c)
		if err != nil {
			return a, nil, err
		}
		var insNotes []string
		if a.Insurances, insNotes, err = resolveNames("insurance", "insurances", a.Insurances, names); err != nil {
			return a, nil, err
		}
		notes = append(notes, insNotes...)
	}
	if len(a.Specialities) == 0 && !slices.ContainsFunc(a.ReferredTo, func(v string) bool { return !uuidRe.MatchString(strings.TrimSpace(v)) }) {
		return a, notes, nil
	}
	tree, err := FetchSpecialities(ctx, c)
	if err != nil {
		return a, nil, err
	}
	specUIDs, specNotes, err := resolveSpecialities(a.Specialities, tree)
	if err != nil {
		return a, nil, err
	}
	refUIDs, refNotes, err := resolveReferredTo(a.ReferredTo, tree)
	if err != nil {
		return a, nil, err
	}
	notes = append(notes, specNotes...)
	notes = append(notes, refNotes...)
	a.Specialities, a.ReferredTo = nil, specUIDs
	if len(refUIDs) > 0 {
		a.ReferredTo = refUIDs
	}
	if len(specUIDs) > 0 && len(refUIDs) > 0 {
		a.ReferredTo = slices.DeleteFunc(slices.Clone(refUIDs), func(u string) bool { return !slices.Contains(specUIDs, u) })
		if len(a.ReferredTo) == 0 {
			return a, nil, shared.Expectedf("referredTo and specialities share no facility")
		}
		notes = append(notes, fmt.Sprintf("referredTo ∩ specialities → %d facilities", len(a.ReferredTo)))
	}
	return a, notes, nil
}

// OrderFilterOptions is the get_order_filter_options result.
type OrderFilterOptions struct {
	Env          config.Env         `json:"env"`
	Profile      string             `json:"profile"`
	Locations    []string           `json:"locations"`
	Insurances   []string           `json:"insurances"`
	Specialities []SpecialityOption `json:"specialities"`
}

// GetOrderFilterOptions logs in and reads the option routes.
func GetOrderFilterOptions(ctx context.Context, env config.Env, profile string) (OrderFilterOptions, error) {
	s, err := Connect(ctx, env, profile)
	if err != nil {
		return OrderFilterOptions{}, err
	}
	locs, err := FetchLocations(ctx, s.Client)
	if err != nil {
		return OrderFilterOptions{}, err
	}
	ins, err := FetchInsuranceNames(ctx, s.Client)
	if err != nil {
		return OrderFilterOptions{}, err
	}
	specs, err := FetchSpecialities(ctx, s.Client)
	if err != nil {
		return OrderFilterOptions{}, err
	}
	if specs == nil {
		specs = []SpecialityOption{}
	}
	return OrderFilterOptions{Env: env, Profile: profile, Locations: locs, Insurances: ins, Specialities: specs}, nil
}
