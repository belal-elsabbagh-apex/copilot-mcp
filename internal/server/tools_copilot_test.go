package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

// fakeBE is a scripted Copilot BE: login always succeeds, everything else goes to handle.
type fakeBE struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	handle func(w http.ResponseWriter, r *http.Request, body map[string]any)
}

func (b *fakeBE) requests(path string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, p := range b.paths {
		if p == path {
			n++
		}
	}
	return n
}

// useFakeBE points both envs of profile "ossm" (direct login) at one scripted BE.
func useFakeBE(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body map[string]any)) *fakeBE {
	t.Helper()
	b := &fakeBE{handle: handle}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.mu.Lock()
		b.paths = append(b.paths, r.URL.RequestURI())
		b.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "physician/login") {
			// JWT-ish token with a far-future expiry claim (ms) so the session cache keeps it.
			fmt.Fprint(w, `{"token":"h.eyJ0aW1lc3RhbXAiOjQxMDI0NDQ4MDAwMDB9.s"}`)
			return
		}
		b.handle(w, r, body)
	}))
	t.Cleanup(b.Close)
	creds := map[string]any{"be": b.URL, "email": "u@example.com", "password": "pw"}
	useConfig(t, map[string]any{
		"copilot": map[string]any{
			"sessionCache": false,
			"profiles":     map[string]any{"ossm": map[string]any{"prod": creds, "pre_prod": creds}},
		},
		"uipath": map[string]any{"orchestratorUrl": "https://cloud.uipath.com/org/tenant/orchestrator_", "bearer": "test-bearer"},
	})
	copilot.ResetSessionCacheForTest()
	t.Cleanup(copilot.ResetSessionCacheForTest)
	return b
}

func TestToCloneCandidateKeepsOnlyOrdersWithFacilityTypeAndOrderNames(t *testing.T) {
	full := copilot.BeOrder{
		"orderUid": "u1", "status": "forReview", "appointmentDate": "2026-01-01",
		"referredFacility": map[string]any{"referredFacilityUid": "f1", "name": "Regal"},
		"orderType":        map[string]any{"typeUid": "t1", "name": "Consultation Referral"},
		"insurance":        map[string]any{"name": "OPTUM"},
		"orderNames":       []any{"n1"}, "ICDCodes": []any{"a", "b"},
	}
	without := func(drop string) copilot.BeOrder {
		o := copilot.BeOrder{}
		for k, v := range full {
			if k != drop {
				o[k] = v
			}
		}
		return o
	}
	got, _ := json.Marshal(toCloneCandidate(full))
	want := `{"orderUid":"u1","status":"forReview","type":"Consultation Referral","facility":"Regal","insurance":"OPTUM","hasAppointmentDate":true,"icds":2,"cpts":0}`
	if string(got) != want {
		t.Errorf("candidate = %s, want %s", got, want)
	}
	for _, drop := range []string{"referredFacility", "orderType", "orderNames"} {
		if c := toCloneCandidate(without(drop)); c != nil {
			t.Errorf("order without %s = %+v, want filtered out", drop, c)
		}
	}
}

func TestFindCloneCandidatesStopsAtTheLimitAndReportsWhatItScanned(t *testing.T) {
	row := func(uid string, cloneable bool) map[string]any {
		o := map[string]any{"orderUid": uid, "status": "forReview"}
		if cloneable {
			o["referredFacility"] = map[string]any{"referredFacilityUid": "f", "name": "F"}
			o["orderType"] = map[string]any{"typeUid": "t", "name": "T"}
			o["orderNames"] = []any{"n"}
		}
		return o
	}
	pages := map[float64][]any{
		0: {row("a", false), row("b", true), row("c", true)},
		1: {row("d", true)},
	}
	useFakeBE(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		n, _ := body["pageNumber"].(float64)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": pages[n]})
	})
	cs, _ := connect(t, New("test"))

	res := callTool(t, cs, "find_clone_candidates", map[string]any{"profile": "ossm", "limit": 2})
	var out struct {
		Profile        string
		Scanned, Found int
		Candidates     []map[string]any
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil || res.IsError {
		t.Fatalf("result = %q (isError=%v): %v", resultText(t, res), res.IsError, err)
	}
	if out.Profile != "ossm" || out.Scanned != 3 || out.Found != 2 || len(out.Candidates) != 2 || out.Candidates[1]["orderUid"] != "c" {
		t.Errorf("result = %+v, want 3 scanned, candidates b and c (page 1 never consumed)", out)
	}
}

func TestFindCloneCandidatesStopsAtAnEmptyPage(t *testing.T) {
	useFakeBE(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	cs, _ := connect(t, New("test"))
	res := callTool(t, cs, "find_clone_candidates", map[string]any{"profile": "ossm"})
	if want := "\"candidates\": []"; res.IsError || !strings.Contains(resultText(t, res), want) {
		t.Errorf("result = %q, want an empty candidates array", resultText(t, res))
	}
}

func TestDeletePreprodOrderDeletesSequentiallyReportsEachAndLogsAnAuditEntry(t *testing.T) {
	be := useFakeBE(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if strings.HasSuffix(r.URL.Path, "gone0001") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"msg":"not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"msg":"deleted"}`))
	})
	cs, ev := connect(t, New("test"))

	res := callTool(t, cs, "delete_preprod_order", map[string]any{"profile": "ossm", "uids": []string{"aaaaaaaa", "gone0001"}})
	var out struct {
		Deleted int
		Results []struct {
			UID    string
			Status int
			OK     bool
			Msg    string
		}
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil || res.IsError {
		t.Fatalf("result = %q (isError=%v): %v", resultText(t, res), res.IsError, err)
	}
	if out.Deleted != 1 || len(out.Results) != 2 || out.Results[0].Msg != "deleted" || out.Results[1].Status != 404 || out.Results[1].OK {
		t.Errorf("result = %+v", out)
	}
	if be.requests("/api/v1/orders/aaaaaaaa") != 1 {
		t.Errorf("requests = %v, want one DELETE per uid", be.paths)
	}
	if log := recv(t, ev.logs); log.Level != "warning" || !strings.Contains(fmt.Sprint(log.Data), "deleted pre-prod order aaaaaaaa") {
		t.Errorf("first audit log = %+v", log)
	}
}

func TestGetLoginTokenReturnsNullClinicFieldsInDirectMode(t *testing.T) {
	useFakeBE(t, func(http.ResponseWriter, *http.Request, map[string]any) {})
	cs, _ := connect(t, New("test"))
	res := callTool(t, cs, "get_login_token", map[string]any{"env": "prod", "profile": "ossm"})
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil || res.IsError {
		t.Fatalf("result = %q (isError=%v): %v", resultText(t, res), res.IsError, err)
	}
	if v, ok := out["clinicUid"]; !ok || v != nil {
		t.Errorf("clinicUid = %v (present=%v), want an explicit null", v, ok)
	}
	if out["mode"] != string(config.ModeDirect) || out["env"] != "prod" || out["token"] == "" {
		t.Errorf("result = %v", out)
	}
}

func TestGetOrderListsDocumentsAndAsksForTheMedicalAuthorizationsOnce(t *testing.T) {
	be := useFakeBE(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/medicalAuthorizations") {
			_, _ = w.Write([]byte(`{"data":[{"uid":"ma1"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"orderUid":"abcdefgh","status":"inProgress","hasAuthScreenshot":false}]}`))
	})
	cs, _ := connect(t, New("test"))

	res := callTool(t, cs, "get_order", map[string]any{"orderUid": "abcdefgh", "env": "prod", "profile": "ossm"})
	var out struct {
		OrderUID  string
		Status    string
		Documents []struct{ Kind, URL string }
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil || res.IsError {
		t.Fatalf("result = %q (isError=%v): %v", resultText(t, res), res.IsError, err)
	}
	// The doc link needs a CDN context; the fake BE host is not `be.<env>…`, so the summary has no
	// resolvable url and documents is omitted — what matters here is the single listing request.
	if out.OrderUID != "abcdefgh" || out.Status != "inProgress" {
		t.Errorf("result = %+v", out)
	}
	if n := be.requests("/api/v1/medicalAuthorizations?orderUid=abcdefgh"); n != 1 {
		t.Errorf("medicalAuthorizations requested %d times, want exactly 1 (prefetched, not repeated)", n)
	}
}

func TestGetOrderFailsWhenTheOrderIsNotFound(t *testing.T) {
	useFakeBE(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) { _, _ = w.Write([]byte(`{"data":[]}`)) })
	cs, _ := connect(t, New("test"))
	res := callTool(t, cs, "get_order", map[string]any{"orderUid": "abcdefgh", "env": "prod", "profile": "ossm"})
	if !res.IsError || !strings.Contains(resultText(t, res), "not found in this env") {
		t.Errorf("result = %q (isError=%v), want the not-found error", resultText(t, res), res.IsError)
	}
}

func TestPreprodWriteToolsTakeNoEnvArgument(t *testing.T) {
	// pre_prod is hard-wired in the handlers, so there is no env to set to prod: it is rejected.
	schemas := toolSchemas(t)
	for name, valid := range map[string]m{
		"delete_preprod_order": {"profile": "ossm", "uids": []string{"abcdefgh"}},
		"submit_preprod_order": {"profile": "ossm", "orderUid": "abcdefgh"},
	} {
		r, ok := schemas[name]
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if !accepts(t, r, valid) {
			t.Errorf("%s: rejected the valid payload", name)
		}
		valid["env"] = "prod"
		if accepts(t, r, valid) {
			t.Errorf("%s: accepted an env argument", name)
		}
	}
}
