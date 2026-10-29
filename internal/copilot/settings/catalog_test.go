package settings

import (
	"slices"
	"strings"
	"testing"
)

func keysOf(sections []SettingSectionInfo) []string {
	keys := make([]string, len(sections))
	for i, s := range sections {
		keys[i] = s.Key
	}
	slices.Sort(keys)
	return keys
}

func catalogSection(t *testing.T, key string) SettingsSection {
	t.Helper()
	for _, s := range SettingsCatalog {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no catalog section %q", key)
	return SettingsSection{}
}

func TestCatalogSectionKeysAreUnique(t *testing.T) {
	keys := sectionKeys(SettingsCatalog)
	if len(slices.Compact(slices.Sorted(slices.Values(keys)))) != len(keys) {
		t.Errorf("duplicate keys in %v", keys)
	}
}

func TestEveryListSectionHasAMatchKeyOrIsIntentionallyContentSet(t *testing.T) {
	for _, s := range SettingsCatalog {
		if s.Kind == KindList && s.MatchKey == "" && !strings.HasPrefix(s.Key, "document-") {
			t.Errorf("%s: list section with no matchKey (only the document-* lists have no stable key)", s.Key)
		}
	}
}

func TestCrawledSectionsCarryADeriveFuncAndPlainOnesDoNot(t *testing.T) {
	for _, key := range []string{"specialties", "referred-providers", "referred-facilities", "orders"} {
		if catalogSection(t, key).Derive == nil {
			t.Errorf("%s has no Derive", key)
		}
	}
	if catalogSection(t, "rendering-providers").Derive != nil {
		t.Error("rendering-providers is a plain endpoint, not crawled")
	}
}

func TestEverySectionHasTagsAndSyncSectionsShareTheirDomain(t *testing.T) {
	for _, s := range SettingsCatalog {
		if len(s.Tags) == 0 {
			t.Errorf("%s has no tags", s.Key)
		}
		if s.Sync != nil && s.Derive == nil {
			t.Errorf("%s: a syncable section is a crawled one", s.Key)
		}
	}
	for _, key := range []string{"specialties", "referred-providers", "referred-facilities"} {
		if d := catalogSection(t, key).Sync.Domain(); d != "specialities" {
			t.Errorf("%s domain = %q, want specialities (one syncer, planned once)", key, d)
		}
	}
	if d := catalogSection(t, "orders").Sync.Domain(); d != "orders" {
		t.Errorf("orders domain = %q", d)
	}
}

func TestTheProviderDirectorySectionsShareTheProvidersTag(t *testing.T) {
	for _, key := range []string{"rendering-providers", "referred-providers", "referred-facilities", "referring-entities"} {
		if !slices.Contains(catalogSection(t, key).Tags, "providers") {
			t.Errorf("%s lacks the providers tag", key)
		}
	}
}

func TestSettingTagsAreDistinctInCatalogOrder(t *testing.T) {
	tags := settingTags(SettingsCatalog)
	if len(slices.Compact(slices.Sorted(slices.Values(tags)))) != len(tags) || !slices.Contains(tags, "orders") || !slices.Contains(tags, "providers") {
		t.Errorf("tags = %v", tags)
	}
	if tags[0] != "file-manager" {
		t.Errorf("tags[0] = %q, want the first catalog entry's tag", tags[0])
	}
}

func TestListSettingSectionsListsEverythingWithDerivedFlags(t *testing.T) {
	res, err := ListSettingSections(ListSettingSectionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sections) != len(SettingsCatalog) || !slices.Equal(res.Tags, settingTags(SettingsCatalog)) {
		t.Fatalf("sections = %d, tags = %v", len(res.Sections), res.Tags)
	}
	for _, s := range res.Sections {
		switch s.Key {
		case "specialties":
			if !s.Derived || !slices.Equal(s.Tags, []string{"orders"}) || s.Label == "" {
				t.Errorf("specialties = %+v", s)
			}
		case "locations":
			if s.Derived {
				t.Error("locations is a plain GET, not derived")
			}
		}
	}
}

func TestListSettingSectionsTagFilterNarrowsTheListing(t *testing.T) {
	res, err := ListSettingSections(ListSettingSectionsOpts{Tag: "providers"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"referred-facilities", "referred-providers", "referring-entities", "rendering-providers"}
	if got := keysOf(res.Sections); !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

func TestListSettingSectionsEMRSectionAppearsOnlyWhenRequested(t *testing.T) {
	without, _ := ListSettingSections(ListSettingSectionsOpts{})
	if slices.Contains(keysOf(without.Sections), "emr-details") {
		t.Error("emr-details must be opt-in")
	}
	with, err := ListSettingSections(ListSettingSectionsOpts{EMR: "NEXTGEN"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(keysOf(with.Sections), "emr-details") || !slices.Contains(with.Tags, "emr") {
		t.Errorf("keys = %v, tags = %v", keysOf(with.Sections), with.Tags)
	}
}

func TestListSettingSectionsRejectsUnknownTagAndKeysWithTheKnownValues(t *testing.T) {
	_, err := ListSettingSections(ListSettingSectionsOpts{Tag: "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown tag: nope. Known: file-manager") {
		t.Errorf("err = %v", err)
	}
	_, err = ListSettingSections(ListSettingSectionsOpts{Sections: []string{"nope"}})
	if err == nil || !strings.Contains(err.Error(), "unknown section(s): nope. Known: ") || !strings.HasSuffix(err.Error(), "(pass `emr` to include emr-details)") {
		t.Errorf("err = %v", err)
	}
	_, err = ListSettingSections(ListSettingSectionsOpts{Sections: []string{"nope"}, EMR: "X"})
	if err == nil || strings.Contains(err.Error(), "pass `emr`") {
		t.Errorf("the emr hint is only for callers who did not pass emr: %v", err)
	}
}

func TestListSettingSectionsSectionsFilterNarrowsToExactKeys(t *testing.T) {
	res, err := ListSettingSections(ListSettingSectionsOpts{Sections: []string{"locations", "orders-outbound"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(res.Sections); !slices.Equal(got, []string{"locations", "orders-outbound"}) {
		t.Errorf("keys = %v", got)
	}
}

func TestListSettingSectionsExposesMatchKeyOnListSectionsOnly(t *testing.T) {
	res, _ := ListSettingSections(ListSettingSectionsOpts{})
	for _, s := range res.Sections {
		if s.Key == "locations" && s.MatchKey != "name" {
			t.Errorf("locations matchKey = %q", s.MatchKey)
		}
		if s.Key == "orders-outbound" && s.MatchKey != "" {
			t.Errorf("orders-outbound matchKey = %q", s.MatchKey)
		}
	}
	if got := asJSON(t, res.Sections[1]); strings.Contains(got, "matchKey") {
		t.Errorf("an object section must omit matchKey: %s", got)
	}
}

func TestSelectSectionsAppliesTagsAndKeysAsAnAND(t *testing.T) {
	got, err := selectSections([]string{"locations", "orders"}, []string{"orders"}, "")
	if err != nil || len(got) != 1 || got[0].Key != "orders" {
		t.Errorf("got %v, %v; want only the orders section", got, err)
	}
	if _, err := selectSections(nil, []string{"nope"}, ""); err == nil || !strings.Contains(err.Error(), "unknown tag(s): nope. Known: ") {
		t.Errorf("err = %v", err)
	}
	all, _ := selectSections(nil, nil, "")
	if len(all) != len(SettingsCatalog) {
		t.Errorf("no filters must select the whole catalog, got %d", len(all))
	}
}

func TestSelectSectionsDoesNotMutateTheCatalog(t *testing.T) {
	before := len(SettingsCatalog)
	_, _ = selectSections([]string{"locations"}, []string{"locations"}, "NEXTGEN")
	if len(SettingsCatalog) != before || slices.Contains(sectionKeys(SettingsCatalog), "emr-details") {
		t.Error("selectSections leaked into the shared catalog")
	}
}

func TestEMRSectionEscapesTheEMRTypeInThePath(t *testing.T) {
	s := emrSection("NEXT GEN/1")
	if s.Path != "/api/v1/settings/emrDetailsSettings/NEXT%20GEN%2F1" || s.Label != "EMR details (NEXT GEN/1)" {
		t.Errorf("section = %+v", s)
	}
}
