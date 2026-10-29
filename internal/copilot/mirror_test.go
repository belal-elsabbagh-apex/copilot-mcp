package copilot

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

func baseSpec() MintSpec {
	return MintSpec{
		PatientName: "DOE, JOHN", PatientBirthDate: "01/02/1980", PatientPhoneNumber: "5551234567",
		InsuranceName: "Acme Health", InsuranceMemberID: "M123", Location: "Main St", TypeUID: "type-1",
		ReferredFacilityUID: "fac-1", OrderNamesUIDs: []string{"name-1"},
	}
}

// happy answers every call in the mint sequence with a generic success; /process answers from
// procMsgs (last one repeating).
func happy(procMsgs ...string) *fakeClient {
	n := 0
	return &fakeClient{env: config.PreProd, handle: func(c call) Response {
		switch {
		case c.Path == "/api/v1/orders":
			return Response{Status: 200, Data: map[string]any{"order": map[string]any{"orderUid": "new-uid"}}}
		case strings.HasSuffix(c.Path, "/process"):
			msg := procMsgs[min(n, len(procMsgs)-1)]
			n++
			return Response{Status: 200, Data: map[string]any{"msg": msg}}
		case c.Path == "/api/v1/orders/filter":
			return Response{Status: 200, Data: rows(map[string]any{"orderUid": "new-uid", "status": "forReview"})}
		}
		return Response{Status: 200, Data: map[string]any{}}
	}}
}

func TestMintRefusesProdAndUntaggedClientsBeforeAnyHTTP(t *testing.T) {
	for _, env := range []config.Env{config.Prod, ""} {
		f := happy("forReview")
		f.env = env
		if _, err := MintPreprodOrder(t.Context(), f, baseSpec(), nil); err == nil || !strings.Contains(err.Error(), "pre_prod-only") || len(f.calls) != 0 {
			t.Errorf("env %q: err = %v, calls = %v; want refusal with no HTTP", env, err, f.paths())
		}
	}
}

func TestMintReportsEachMilestoneAndStopsAtForReview(t *testing.T) {
	noWaits(t)
	f := happy("forReview")
	var msgs []string
	res, err := MintPreprodOrder(t.Context(), f, baseSpec(), func(m string) { msgs = append(msgs, m) })
	if err != nil || res.NewUID != "new-uid" || res.Verify == nil {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	for _, want := range []string{"new draft: new-uid", "PUT patient: ok", "PUT orderNames: ok", "/note/upload: ok", "verify: done",
		"(sentBy: left to FE default — clinicProvider not mapped)"} {
		if !slices.Contains(msgs, want) {
			t.Errorf("progress lacks %q; got %v", want, msgs)
		}
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c.Path, "/submit") {
			t.Fatal("the mint engine must never submit")
		}
	}
	if res.ProcessMessage == nil || *res.ProcessMessage != "forReview" {
		t.Errorf("ProcessMessage = %v", res.ProcessMessage)
	}
}

func TestMintWorksWithoutAProgressCallback(t *testing.T) {
	noWaits(t)
	if _, err := MintPreprodOrder(t.Context(), happy("forReview"), baseSpec(), nil); err != nil {
		t.Fatal(err)
	}
}

// put bodies by label, in call order, so the sequence invariants are assertable.
func putKeys(f *fakeClient) []string {
	var out []string
	for _, c := range f.calls {
		if c.Method != "PUT" {
			continue
		}
		m, _ := c.Body.JSON.(map[string]any)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		out = append(out, strings.Join(keys, ","))
	}
	return out
}

func TestMintSequenceInvariants(t *testing.T) {
	noWaits(t)
	spec := baseSpec()
	spec.PlaceOfService = "11"
	spec.ReferredProviderUID = "prov-1"
	spec.AppointmentDate = "03/04/2026"
	spec.ICDCodes = []MintICD{{Code: "A1", Description: "d"}}
	f := happy("incomplete", "incomplete", "forReview")
	if _, err := MintPreprodOrder(t.Context(), f, spec, nil); err != nil {
		t.Fatal(err)
	}
	paths := f.paths()
	idx := func(prefix string) int {
		return slices.IndexFunc(paths, func(p string) bool { return strings.HasPrefix(p, prefix) })
	}

	puts := putKeys(f)
	last := puts[len(puts)-1]
	if last != "placeOfService" {
		t.Errorf("last PUT = %q, want placeOfService LAST (post-forReview)", last)
	}
	if puts[len(puts)-2] != "requiredAuthorization" {
		t.Errorf("second-to-last PUT = %q, want requiredAuthorization first", puts[len(puts)-2])
	}
	// order names come after the type PUT and re-send speciality/provider/facility as null, since
	// names RESET them; the real facility and provider PUTs come after the names.
	typ, names := slices.Index(puts, "orderDate,referredFacilityUid,referredProviderUid,specialityUid,typeUid"), slices.Index(puts, "orderNamesUids,referredFacilityUid,referredProviderUid,specialityUid")
	fac, prov := slices.Index(puts, "referredFacilityUid"), slices.Index(puts, "referredProviderUid")
	if typ < 0 || names <= typ || fac <= names || prov <= fac {
		t.Errorf("order of type/names/facility/provider = %d/%d/%d/%d; puts = %v", typ, names, fac, prov, puts)
	}
	if idx("POST /api/v1/orders/new-uid/note/upload") < 0 {
		t.Error("note upload missing")
	}
	// 2nd /process happens right after the appointment date PUT because the first said "incomplete".
	var procs int
	for _, p := range paths {
		if strings.HasSuffix(p, "/process") {
			procs++
		}
	}
	if procs < 3 {
		t.Errorf("/process calls = %d, want first + after-appointment + final loop", procs)
	}
}

func TestMintWireBodiesOmitEmptySentinels(t *testing.T) {
	noWaits(t)
	f := happy("forReview")
	spec := baseSpec()
	spec.PatientName, spec.InsuranceName, spec.TypeUID, spec.PatientBirthDate = "", "", "", ""
	if _, err := MintPreprodOrder(t.Context(), f, spec, nil); err != nil {
		t.Fatal(err)
	}
	b0, _ := json.Marshal(f.calls[1].Body.JSON) // PUT patient
	if string(b0) != `{"emrPatientId":"","patientBirthDate":null}` {
		t.Errorf("patient body = %s, want name omitted and birth date null", b0)
	}
	b1, _ := json.Marshal(f.calls[3].Body.JSON) // PUT insurance
	if string(b1) != `{"insurance":{"memberId":"M123"}}` {
		t.Errorf("insurance body = %s", b1)
	}
	b2, _ := json.Marshal(f.calls[5].Body.JSON) // PUT type — specialityUid null, typeUid omitted
	if strings.Contains(string(b2), "typeUid") || !strings.Contains(string(b2), `"specialityUid":null`) {
		t.Errorf("type body = %s", b2)
	}
}

func TestMintTreatsE6001AsAlreadyAtForReview(t *testing.T) {
	noWaits(t)
	f := happy("forReview")
	inner := f.handle
	f.handle = func(c call) Response {
		if strings.HasSuffix(c.Path, "/process") {
			return Response{Status: 400, Text: `{"code":"E6001","message":"status must be in drafted/new/incomplete/pending"}`}
		}
		return inner(c)
	}
	res, err := MintPreprodOrder(t.Context(), f, baseSpec(), nil)
	if err != nil || res.ProcessMessage == nil || *res.ProcessMessage != "Order already at forReview status" {
		t.Errorf("res = %+v, err = %v; want E6001 tolerated as forReview", res, err)
	}
}

func TestMintRetriesProcessUntilForReviewThenReportsLastMessageWhenItNeverGetsThere(t *testing.T) {
	noWaits(t)
	f := happy("incomplete")
	res, err := MintPreprodOrder(t.Context(), f, baseSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var procs int
	for _, p := range f.paths() {
		if strings.HasSuffix(p, "/process") {
			procs++
		}
	}
	// 1st + processTries attempts in the final loop
	if procs != 1+processTries || res.ProcessMessage == nil || *res.ProcessMessage != "incomplete" {
		t.Errorf("procs = %d, msg = %v; want %d attempts and the last message surfaced", procs, res.ProcessMessage, 1+processTries)
	}
}

func TestMintFailsOnDraftErrorsAndPUTFailures(t *testing.T) {
	f := queued(config.PreProd, Response{Status: 500, Text: "boom"})
	if _, err := MintPreprodOrder(t.Context(), f, baseSpec(), nil); err == nil || !strings.Contains(err.Error(), "create draft failed 500") {
		t.Errorf("err = %v", err)
	}
	f = queued(config.PreProd, Response{Status: 200, Data: map[string]any{}})
	if _, err := MintPreprodOrder(t.Context(), f, baseSpec(), nil); err == nil || !strings.Contains(err.Error(), "returned no orderUid") {
		t.Errorf("err = %v", err)
	}
	g := happy("forReview")
	inner := g.handle
	g.handle = func(c call) Response {
		if c.Method == "PUT" {
			return Response{Status: 422, Text: "bad"}
		}
		return inner(c)
	}
	if _, err := MintPreprodOrder(t.Context(), g, baseSpec(), nil); err == nil || !strings.Contains(err.Error(), "PUT patient failed 422") {
		t.Errorf("err = %v", err)
	}
}

func TestMintCPTInjectionFailureIsNonFatal(t *testing.T) {
	noWaits(t)
	f := happy("forReview")
	inner := f.handle
	f.handle = func(c call) Response {
		if m, _ := c.Body.JSON.(map[string]any); m["CPTCodes"] != nil {
			return Response{Status: 500, Text: "nope"}
		}
		return inner(c)
	}
	spec := baseSpec()
	spec.CPTCodes = []MintCPT{{Code: "97110", Description: "x"}}
	if _, err := MintPreprodOrder(t.Context(), f, spec, nil); err != nil {
		t.Errorf("err = %v, want best-effort CPT injection to continue", err)
	}
}

func TestNormPhone(t *testing.T) {
	for in, want := range map[string]string{
		"5551234567": "(555) 123-4567", "1-555-123-4567": "(555) 123-4567", "": "(555) 555-5555", "123": "(555) 555-5555",
	} {
		if got := normPhone(in); got != want {
			t.Errorf("normPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDummyPDFHasCorrectXrefOffsets(t *testing.T) {
	pdf := string(buildDummyPDF())
	for i := 1; i <= 5; i++ {
		want := strings.Index(pdf, "\n"+string(rune('0'+i))+" 0 obj\n") + 1
		line := strings.Split(strings.SplitAfter(pdf, "0000000000 65535 f \n")[1], "\n")[i-1]
		if got := strings.TrimSpace(strings.TrimSuffix(line, "00000 n ")); got != fmt.Sprintf("%010d", want) {
			t.Errorf("xref entry %d = %q, want %q", i, got, fmt.Sprintf("%010d", want))
		}
	}
	if !strings.HasPrefix(pdf, "%PDF-1.4\n") || !strings.HasSuffix(pdf, "%%EOF\n") {
		t.Error("not a PDF envelope")
	}
}
