package settings

import (
	"fmt"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// SettingsCatalog is the section catalog (derived from the captured HAR), one row per catalog key.
// A new section is a new row, never a new branch.
var SettingsCatalog = []SettingsSection{
	{Key: "file-manager", Label: "File manager settings", Tags: []string{"file-manager"},
		Path: "/api/v1/settings/fileManager", Kind: KindObject, Envelope: "data.fileManagerSettings"},
	{Key: "orders-outbound", Label: "Outbound order settings", Tags: []string{"orders"},
		Path: "/api/v1/settings/orders/outbound", Kind: KindObject},
	{Key: "orders-outbound-types", Label: "Outbound order types", Tags: []string{"orders"},
		Path: typesPath, Kind: KindList, MatchKey: "name"},
	{Key: "auto-finish-document-rules", Label: "Auto-finish document rules", Tags: []string{"documents"},
		Path: "/api/v1/settings/auto-finish-document-rules", Kind: KindObject},
	{Key: "eligibility-visibility", Label: "Eligibility visibility", Tags: []string{"eligibility"},
		Path: "/api/v1/settings/eligibility/eligibilityVisibility", Kind: KindObject},
	{Key: "locations", Label: "Locations", Tags: []string{"locations"},
		Path: "/api/v1/settings/locations", Kind: KindList, MatchKey: "name",
		Ignore: []string{"email"}}, // dummy locations carry a randomized per-env email
	{Key: "location-groups", Label: "Location groups", Tags: []string{"locations"},
		Path: "/api/v1/settings/locations/groups", Kind: KindList, MatchKey: "name"},
	{Key: "location-regions", Label: "Location regions", Tags: []string{"locations"},
		Path: "/api/v1/settings/locations/regions", Kind: KindList, MatchKey: "name"},
	{Key: "location-fax-info", Label: "Location fax info", Tags: []string{"locations"},
		Path: "/api/v1/settings/locationFaxInfo", Kind: KindList, MatchKey: "location",
		Ignore: []string{"clinicLogo"}}, // points at the per-env CDN host
	{Key: "clinic-payers", Label: "Clinic payers", Tags: []string{"payers"},
		Path: "/api/v1/settings/clinic-payers", Kind: KindList, Envelope: "data", MatchKey: "Name",
		Ignore: []string{"providerId"}},
	// The document-* lists have no stable cross-env key -> content-set diff.
	{Key: "document-routing-rules", Label: "Document routing rules", Tags: []string{"documents"},
		Path: "/api/v1/settings/document-routing-rules?pageSize=200&pageNumber=0", Kind: KindList, Envelope: "data"},
	{Key: "document-reviewing-rules-auto", Label: "Document reviewing rules (Auto Review)", Tags: []string{"documents"},
		Path: "/api/v1/settings/document-reviewing-rules/Auto%20Review", Kind: KindList, Envelope: "data"},
	{Key: "document-reviewing-rules-manual", Label: "Document reviewing rules (Manual Review)", Tags: []string{"documents"},
		Path: "/api/v1/settings/document-reviewing-rules/Manual%20Review", Kind: KindList, Envelope: "data"},
	{Key: "referring-entities", Label: "Referring entities", Tags: []string{"providers"},
		Path: "/api/v1/settings/referring-entities", Kind: KindList, Envelope: "data", MatchKey: "name"},
	{Key: "rendering-providers", Label: "Rendering providers (clinic)", Tags: []string{"providers"},
		Path: "/api/v1/clinic/ehr/providers", Kind: KindList, MatchKey: "name"},
	// The next three are crawled from each order type's /specialities (see Derive) and share one
	// syncer.
	{Key: "specialties", Label: "Specialties (across order types)", Tags: []string{"orders"},
		Path: "/api/v1/settings/orders/outbound/types/*/specialities", Kind: KindList, MatchKey: "name",
		Derive: deriveSpecialties, Sync: specialitiesSyncer{}},
	{Key: "referred-providers", Label: "Referred (referring) providers", Tags: []string{"providers"},
		Path: "/api/v1/settings/orders/outbound/types/*/specialities", Kind: KindList, MatchKey: "name",
		Derive: deriveNested("referredProviders"), Sync: specialitiesSyncer{}},
	{Key: "referred-facilities", Label: "Referred facilities", Tags: []string{"providers"},
		Path: "/api/v1/settings/orders/outbound/types/*/specialities", Kind: KindList, MatchKey: "name",
		Derive: deriveNested("referredFacilities"), Sync: specialitiesSyncer{}},
	{Key: "orders", Label: "Orders (across order types)", Tags: []string{"orders"},
		Path: "/api/v1/settings/orders/outbound/types/*/names", Kind: KindList, MatchKey: "name",
		Derive: deriveOrderNames, Sync: ordersSyncer{}},
}

// emrSection is keyed by the account's EMR type (e.g. NEXTGEN), so it is opt-in: only included
// when the caller passes `emr`.
func emrSection(emr string) SettingsSection {
	enc := shared.URIComponent(emr)
	return SettingsSection{
		Key: "emr-details", Label: fmt.Sprintf("EMR details (%s)", emr), Tags: []string{"emr"},
		Path: "/api/v1/settings/emrDetailsSettings/" + enc, Kind: KindObject,
	}
}

// settingTags returns the distinct tags across the sections, in catalog order.
func settingTags(all []SettingsSection) []string {
	tags := []string{}
	for _, s := range all {
		for _, t := range s.Tags {
			if !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
	}
	return tags
}

func catalogWithEMR(emr string) []SettingsSection {
	if emr == "" {
		return slices.Clone(SettingsCatalog)
	}
	return append(slices.Clone(SettingsCatalog), emrSection(emr))
}

func sectionKeys(all []SettingsSection) []string {
	keys := make([]string, len(all))
	for i, s := range all {
		keys[i] = s.Key
	}
	return keys
}

// emrHint names the opt-in EMR section when it was not requested.
func emrHint(emr string) string {
	if emr != "" {
		return ""
	}
	return " (pass `emr` to include emr-details)"
}

// missingFrom is the elements of want that are not in have.
func missingFrom(want, have []string) []string {
	var out []string
	for _, w := range want {
		if !slices.Contains(have, w) {
			out = append(out, w)
		}
	}
	return out
}

// selectSections picks sections from the catalog, narrowing by tag (any match) and/or exact key.
// Both filters apply together (AND). Unknown tags/keys fail with a clear, value-listing error.
func selectSections(keys, tags []string, emr string) ([]SettingsSection, error) {
	picked := catalogWithEMR(emr)
	if len(tags) > 0 {
		valid := settingTags(picked)
		if missing := missingFrom(tags, valid); len(missing) > 0 {
			return nil, fmt.Errorf("unknown tag(s): %s. Known: %s", strings.Join(missing, ", "), strings.Join(valid, ", "))
		}
		picked = slices.DeleteFunc(picked, func(s SettingsSection) bool {
			return !slices.ContainsFunc(s.Tags, func(t string) bool { return slices.Contains(tags, t) })
		})
	}
	if len(keys) > 0 {
		all := sectionKeys(catalogWithEMR(emr))
		if missing := missingFrom(keys, all); len(missing) > 0 {
			return nil, fmt.Errorf("unknown section(s): %s. Known: %s%s", strings.Join(missing, ", "), strings.Join(all, ", "), emrHint(emr))
		}
		picked = slices.DeleteFunc(picked, func(s SettingsSection) bool { return !slices.Contains(keys, s.Key) })
	}
	return picked, nil
}

// ListSettingSections lists the catalog sections diff_settings can compare, optionally narrowed by
// a tag and/or an explicit set of section keys (both applied as AND). Pure: reads the static
// catalog, no creds/network. Unknown tag/keys fail with a clear, value-listing error.
func ListSettingSections(opts ListSettingSectionsOpts) (ListSettingSectionsResult, error) {
	all := catalogWithEMR(opts.EMR)
	allTags := settingTags(all)
	if opts.Tag != "" && !slices.Contains(allTags, opts.Tag) {
		return ListSettingSectionsResult{}, fmt.Errorf("unknown tag: %s. Known: %s", opts.Tag, strings.Join(allTags, ", "))
	}
	known := sectionKeys(all)
	if missing := missingFrom(opts.Sections, known); len(missing) > 0 {
		return ListSettingSectionsResult{}, fmt.Errorf("unknown section(s): %s. Known: %s%s", strings.Join(missing, ", "), strings.Join(known, ", "), emrHint(opts.EMR))
	}
	sections := []SettingSectionInfo{}
	for _, s := range all {
		if (opts.Tag != "" && !slices.Contains(s.Tags, opts.Tag)) ||
			(len(opts.Sections) > 0 && !slices.Contains(opts.Sections, s.Key)) {
			continue
		}
		sections = append(sections, SettingSectionInfo{
			Key: s.Key, Label: s.Label, Tags: slices.Clone(s.Tags), Kind: s.Kind, Derived: s.Derive != nil, MatchKey: s.MatchKey,
		})
	}
	return ListSettingSectionsResult{Tags: allTags, Sections: sections}, nil
}
