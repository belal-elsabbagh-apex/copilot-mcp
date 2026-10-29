package copilot

import (
	"reflect"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

var prodCDN = &CDNContext{Origin: "https://cdn.prod.ehrcopilotbe.com", Account: "ossm"}

func TestCDNFromOrderParsesOriginAndAccount(t *testing.T) {
	o := BeOrder{"authScreenshotFileUrl": "https://cdn.prod.ehrcopilotbe.com/ossm/orders/a1f504e2/screenshots/authScreen.pdf"}
	if got := CDNFromOrder(o); !reflect.DeepEqual(got, prodCDN) {
		t.Errorf("got %+v", got)
	}
	o = BeOrder{"authorizationResultURL": "https://cdn.prod.ehrcopilotbe.com/kafri/medicalAuthorizations/x/summary.pdf"}
	if got := CDNFromOrder(o); got == nil || got.Account != "kafri" {
		t.Errorf("fallback to authorizationResultURL: got %+v", got)
	}
	if CDNFromOrder(BeOrder{}) != nil {
		t.Error("no CDN url should give nil")
	}
}

func TestCDNOriginFromBaseSwapsBeForCDN(t *testing.T) {
	for in, want := range map[string]string{
		"https://be.prod.ehrcopilotbe.com":     "https://cdn.prod.ehrcopilotbe.com",
		"https://be.pre-prod.ehrcopilotbe.com": "https://cdn.pre-prod.ehrcopilotbe.com",
		"https://api.example.com":              "",
		"not a url":                            "",
	} {
		if got := CDNOriginFromBase(in); got != want {
			t.Errorf("CDNOriginFromBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveCDNPrefersTheDeterministicPairing(t *testing.T) {
	if got := ResolveCDN(BeOrder{}, "https://be.prod.ehrcopilotbe.com", "ossm"); !reflect.DeepEqual(got, prodCDN) {
		t.Errorf("got %+v", got)
	}
	o := BeOrder{"authScreenshotFileUrl": "https://cdn.prod.ehrcopilotbe.com/kafri/orders/x/screenshots/authScreen.pdf"}
	if got := ResolveCDN(o, "", ""); got == nil || got.Account != "kafri" {
		t.Errorf("fallback: got %+v", got)
	}
}

func TestSummaryURLFor(t *testing.T) {
	const base = "https://cdn.prod.ehrcopilotbe.com/ossm/medicalAuthorizations/"
	for _, c := range []struct {
		auth map[string]any
		cdn  *CDNContext
		want string
	}{
		{map[string]any{"uid": "0d26bf01"}, prodCDN, base + "0d26bf01/summary.pdf"},
		{map[string]any{"medicalAuthorizationUid": "abc"}, prodCDN, base + "abc/summary.pdf"},
		{map[string]any{"id": "zzz"}, prodCDN, base + "zzz/summary.pdf"},
		{map[string]any{"uid": "x", "summaryUrl": "https://cdn/x/summary.pdf"}, prodCDN, "https://cdn/x/summary.pdf"},
		{map[string]any{"uid": "x"}, nil, ""},
		{map[string]any{}, prodCDN, ""},
	} {
		if got := SummaryURLFor(c.auth, c.cdn); got != c.want {
			t.Errorf("SummaryURLFor(%v) = %q, want %q", c.auth, got, c.want)
		}
	}
}

func docsClient(listing Response) *fakeClient {
	f := queued(config.Prod, listing)
	f.handle = func(c call) Response { return listing }
	return f
}

func TestOrderDocumentsBuildsScreenshotAndSummaryLinks(t *testing.T) {
	// base is stub.invalid (not a be. host) so the CDN comes from the stamped order url.
	o := BeOrder{"hasAuthScreenshot": true, "authScreenshotFileUrl": "https://cdn.prod.ehrcopilotbe.com/ossm/orders/u1/screenshots/authScreen.pdf"}
	f := docsClient(Response{Status: 200, Data: map[string]any{"data": []any{map[string]any{"uid": "m1"}, "junk", map[string]any{}}}})
	got := OrderDocuments(t.Context(), f, o, "u1", "")
	want := []OrderDocument{
		{"authScreenshot", "https://cdn.prod.ehrcopilotbe.com/ossm/orders/u1/screenshots/authScreen.pdf"},
		{"authSummary", "https://cdn.prod.ehrcopilotbe.com/ossm/medicalAuthorizations/m1/summary.pdf"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("docs = %v, want %v", got, want)
	}
	if f.paths()[0] != "GET /api/v1/medicalAuthorizations?orderUid=u1" {
		t.Errorf("call = %v", f.paths())
	}
}

func TestOrderDocumentsOmitsScreenshotWithoutTheExistenceFlag(t *testing.T) {
	o := BeOrder{"authScreenshotFileUrl": "https://cdn.prod.ehrcopilotbe.com/ossm/orders/u1/screenshots/authScreen.pdf"}
	got := OrderDocuments(t.Context(), docsClient(Response{Status: 200, Data: rows()}), o, "u1", "")
	if len(got) != 0 {
		t.Errorf("docs = %v; the stamped url exists even when the file does not — hasAuthScreenshot is the signal", got)
	}
}

func TestOrderDocumentsFallsBackToTheOrderResultURLWhenTheListingIsEmptyOrFails(t *testing.T) {
	o := BeOrder{"authorizationResultURL": "https://cdn.prod.ehrcopilotbe.com/ossm/medicalAuthorizations/r/summary.pdf"}
	want := []OrderDocument{{"authSummary", "https://cdn.prod.ehrcopilotbe.com/ossm/medicalAuthorizations/r/summary.pdf"}}
	for _, listing := range []Response{{Status: 200, Data: rows()}, {Status: 500, Text: "down"}} {
		if got := OrderDocuments(t.Context(), docsClient(listing), o, "u1", ""); !reflect.DeepEqual(got, want) {
			t.Errorf("docs = %v, want %v", got, want)
		}
	}
}

func TestOrderDocumentsReturnsAnEmptyNonNilSlice(t *testing.T) {
	if got := OrderDocuments(t.Context(), docsClient(Response{Status: 200, Data: rows()}), BeOrder{}, "u1", ""); got == nil || len(got) != 0 {
		t.Errorf("docs = %#v, want [] (never null at the JSON edge)", got)
	}
}
