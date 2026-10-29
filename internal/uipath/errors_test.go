package uipath

import (
	"errors"
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func TestAPIErrorCarriesStructuredFields(t *testing.T) {
	e := apiErrorFromText("GET", "https://host/odata/Jobs(1)", 500, `{"message":"boom"}`)
	if e.Method != "GET" || e.URL != "https://host/odata/Jobs(1)" || e.Status != 500 {
		t.Fatalf("fields = %+v", e)
	}
}

func TestAPIErrorMessageShape(t *testing.T) {
	if got := apiErrorFromText("POST", "/odata/Jobs", 404, "not found").Error(); got != "UiPath POST /odata/Jobs -> 404: not found" {
		t.Fatalf("message = %q", got)
	}
	if got := apiErrorFromText("GET", "/x", 500, `{"a":1}`).Error(); !strings.Contains(got, `{"a":1}`) {
		t.Fatalf("JSON body not stringified: %q", got)
	}
}

func TestAPIErrorTruncatesLongBodies(t *testing.T) {
	long := strings.Repeat("x", 400)
	got := apiErrorFromText("GET", "/x", 500, long).Error()
	if !strings.Contains(got, strings.Repeat("x", 300)+"…") || len(got) > len(long)+50 {
		t.Fatalf("not truncated: %d chars", len(got))
	}
}

func TestAPIErrorFromResponseTextKeepsKeyOrder(t *testing.T) {
	got := apiErrorFromText("GET", "/x", 400, `{ "z": 1,  "a": 2 }`).Error()
	if !strings.HasSuffix(got, `{"z":1,"a":2}`) {
		t.Fatalf("message = %q", got)
	}
}

func TestAPIErrorIsExpected(t *testing.T) {
	if !shared.IsExpected(apiErrorFromText("GET", "/x", 500, "boom")) {
		t.Fatal("APIError must be an Expected error")
	}
}

func TestIsNotFoundOnlyFor404(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"404":     {apiErrorFromText("GET", "/x", 404, ""), true},
		"401":     {apiErrorFromText("GET", "/x", 401, ""), false},
		"500":     {apiErrorFromText("GET", "/x", 500, ""), false},
		"plain":   {errors.New("404 not found"), false},
		"nil":     {nil, false},
		"wrapped": {errors.Join(errors.New("ctx"), apiErrorFromText("GET", "/x", 404, "")), true},
	}
	for name, c := range cases {
		if got := IsNotFound(c.err); got != c.want {
			t.Errorf("%s: IsNotFound = %v, want %v", name, got, c.want)
		}
	}
}
