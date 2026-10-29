package settings

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

var noIgnore = map[string]bool{}

func hasPath(diffs []FieldDiff, path string) bool {
	return slices.ContainsFunc(diffs, func(d FieldDiff) bool { return d.Path == path })
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // like JSON.stringify, so error text with "->" reads naturally
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func TestStripNoiseDropsUIDAndTimestampKeysRecursively(t *testing.T) {
	got := stripNoise(j(t, `{"ruleUid":"abc","name":"Consultation Referral","createdAt":"2026-03-31T19:27:40.691Z",
		"updatedAt":"2026-03-31T19:27:40.691Z","nested":{"specialityUid":"x","value":3}}`), noIgnore)
	if g, want := asJSON(t, got), `{"name":"Consultation Referral","nested":{"value":3}}`; g != want {
		t.Errorf("stripNoise = %s, want %s", g, want)
	}
}

func TestStripNoiseAppliesPerSectionIgnoreFields(t *testing.T) {
	got := stripNoise(j(t, `{"location":"OSSM COR","clinicLogo":"https://cdn.prod.example/x.png","faxNumber":"1"}`), toSet([]string{"clinicLogo"}))
	if g, want := asJSON(t, got), `{"faxNumber":"1","location":"OSSM COR"}`; g != want {
		t.Errorf("stripNoise = %s, want %s", g, want)
	}
}

func TestStripNoiseSortsArraysSoOrderDoesNotMatter(t *testing.T) {
	a := stripNoise(j(t, `{"cats":["Orders","Billing","Other"]}`), noIgnore)
	b := stripNoise(j(t, `{"cats":["Other","Orders","Billing"]}`), noIgnore)
	if asJSON(t, a) != asJSON(t, b) {
		t.Errorf("%s != %s", asJSON(t, a), asJSON(t, b))
	}
}

func TestStripNoiseKeepsKeysWhoseNameOnlyContainsUid(t *testing.T) {
	got := stripNoise(j(t, `{"uidCount":2,"guidance":"x","payerUID":"p","Uid":"q"}`), noIgnore)
	if g, want := asJSON(t, got), `{"guidance":"x","uidCount":2}`; g != want {
		t.Errorf("stripNoise = %s, want %s (only a trailing uid, any case, is noise)", g, want)
	}
}

func TestDiffObjectsFindsAFlippedFlagAndAnAddedKey(t *testing.T) {
	prod := stripNoise(j(t, `{"sendFaxAfterAuth":true,"extractAllICDCodes":false}`), noIgnore)
	pre := stripNoise(j(t, `{"sendFaxAfterAuth":false,"extractAllICDCodes":false,"newFlag":true}`), noIgnore)
	diffs := diffObjects(prod, pre, "")
	if !hasPath(diffs, "sendFaxAfterAuth") || !hasPath(diffs, "newFlag") || hasPath(diffs, "extractAllICDCodes") {
		t.Fatalf("diffs = %s", asJSON(t, diffs))
	}
	for _, d := range diffs {
		if d.Path == "sendFaxAfterAuth" && (d.Prod != true || d.Preprod != false) {
			t.Errorf("flag diff = %+v, want prod true / preprod false", d)
		}
	}
}

func TestDiffObjectsIdenticalObjectsProduceNoDiffs(t *testing.T) {
	v := stripNoise(j(t, `{"a":1,"b":{"c":[3,1,2]}}`), noIgnore)
	if got := diffObjects(v, v, ""); len(got) != 0 || got == nil {
		t.Errorf("diffObjects = %#v, want an empty non-nil slice (it marshals as [])", got)
	}
}

func TestDiffObjectsNestedPathsAndRootMismatch(t *testing.T) {
	diffs := diffObjects(j(t, `{"a":{"b":1}}`), j(t, `{"a":{"b":2}}`), "")
	if !hasPath(diffs, "a.b") {
		t.Errorf("diffs = %s, want a.b", asJSON(t, diffs))
	}
	if root := diffObjects(1.0, "x", ""); len(root) != 1 || root[0].Path != "(root)" {
		t.Errorf("root = %+v, want one (root) diff", root)
	}
}

// A key absent on one side is not the same as null there, and the absent side is omitted from the
// JSON (TS `undefined`), while null is kept.
func TestDiffObjectsDistinguishesAbsentFromNull(t *testing.T) {
	diffs := diffObjects(j(t, `{"a":null}`), j(t, `{}`), "")
	if got, want := asJSON(t, diffs), `[{"path":"a","prod":null}]`; got != want {
		t.Errorf("diffs = %s, want %s", got, want)
	}
	if diffs := diffObjects(j(t, `{}`), j(t, `{"a":1}`), ""); asJSON(t, diffs) != `[{"path":"a","preprod":1}]` {
		t.Errorf("diffs = %s", asJSON(t, diffs))
	}
}

func TestDiffListMatchesByKeyAndIgnoresDifferingUIDs(t *testing.T) {
	prod := j(t, `[{"typeUid":"prod-uid-1","name":"Follow-up Visit","autoSubmit":false}]`).([]any)
	pre := j(t, `[{"typeUid":"pre-uid-9","name":"Follow-up Visit","autoSubmit":true}]`).([]any)
	d := diffList(prod, pre, "name", nil)
	if len(d.OnlyInProd) != 0 || len(d.OnlyInPreProd) != 0 || len(d.Changed) != 1 {
		t.Fatalf("diff = %+v", d)
	}
	c := d.Changed[0]
	if c.Key != "Follow-up Visit" || !hasPath(c.Diffs, "autoSubmit") || hasPath(c.Diffs, "typeUid") {
		t.Errorf("changed = %+v, want autoSubmit only (the UID difference is noise)", c)
	}
}

func TestDiffListReportsItemsPresentInOnlyOneEnv(t *testing.T) {
	d := diffList(j(t, `[{"name":"A"},{"name":"B"}]`).([]any), j(t, `[{"name":"A"},{"name":"C"}]`).([]any), "name", nil)
	if asJSON(t, d.OnlyInProd) != `[{"name":"B"}]` || asJSON(t, d.OnlyInPreProd) != `[{"name":"C"}]` || len(d.Changed) != 0 {
		t.Errorf("diff = %s / %s / %s", asJSON(t, d.OnlyInProd), asJSON(t, d.OnlyInPreProd), asJSON(t, d.Changed))
	}
}

func TestDiffListFallsBackToTheWholeItemWhenTheMatchKeyIsMissing(t *testing.T) {
	d := diffList(j(t, `[{"name":"A"},{"other":1}]`).([]any), j(t, `[{"name":"A"},{"other":2}]`).([]any), "name", nil)
	if len(d.OnlyInProd) != 1 || len(d.OnlyInPreProd) != 1 || len(d.Changed) != 0 {
		t.Errorf("a keyless item can only match an identical one: %+v", d)
	}
}

func TestDiffListContentSetIgnoresUIDNoise(t *testing.T) {
	prod := j(t, `[{"ruleUid":"p1","category":"Clearance"},{"ruleUid":"p2","category":"Orders"}]`).([]any)
	pre := j(t, `[{"ruleUid":"q1","category":"Clearance"},{"ruleUid":"q9","category":"Billing"}]`).([]any)
	d := diffList(prod, pre, "", nil)
	// "Clearance" exists in both once UIDs are stripped -> not reported.
	if asJSON(t, d.OnlyInProd) != `[{"category":"Orders"}]` || asJSON(t, d.OnlyInPreProd) != `[{"category":"Billing"}]` || len(d.Changed) != 0 {
		t.Errorf("diff = %s / %s", asJSON(t, d.OnlyInProd), asJSON(t, d.OnlyInPreProd))
	}
}

func TestDiffListResultsMarshalAsEmptyArraysNotNull(t *testing.T) {
	d := diffList(nil, nil, "name", nil)
	if got := asJSON(t, d.OnlyInProd) + asJSON(t, d.OnlyInPreProd) + asJSON(t, d.Changed); got != "[][][]" {
		t.Errorf("got %s", got)
	}
}

func TestNormalizeSectionValueAppliesTheSectionIgnoreSet(t *testing.T) {
	got := normalizeSectionValue(&SettingsSection{Ignore: []string{"email"}}, // mirrors the locations section
		j(t, `[{"locationUid":"u1","name":"OSSM COR","email":"dummy@x.dev","active":true}]`))
	if g, want := asJSON(t, got), `[{"active":true,"name":"OSSM COR"}]`; g != want {
		t.Errorf("got %s, want %s", g, want)
	}
}
