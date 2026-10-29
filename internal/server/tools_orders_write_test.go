package server

import (
	"encoding/json"
	"testing"
)

func TestSubmitResultMarshalsVerifyNullWhenNothingWasVerified(t *testing.T) {
	b, err := json.Marshal(submitResult{Profile: "ossm", OrderUID: "abcdefgh", Submitted: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"profile":"ossm","orderUid":"abcdefgh","submitted":true,"verify":null}`; string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}
