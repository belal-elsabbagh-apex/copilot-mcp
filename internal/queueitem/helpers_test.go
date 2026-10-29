package queueitem

import (
	"encoding/base64"
	"testing"
)

func TestToMDYNormalizesOrBlanks(t *testing.T) {
	for in, want := range map[string]string{
		"1/2/2024": "01/02/2024", "12/31/2024": "12/31/2024", "2024-03-04": "03/04/2024",
		"": "", "not a date": "",
	} {
		if got := ToMDY(in); got != want {
			t.Errorf("ToMDY(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitName(t *testing.T) {
	for _, c := range []struct{ in, last, first string }{
		{"Doe, John A", "DOE", "JOHN"},
		{"Madonna", "MADONNA", ""}, // comma-less = last name only
		{"", "", ""},
	} {
		if last, first := SplitName(c.in); last != c.last || first != c.first {
			t.Errorf("SplitName(%q) = %q, %q; want %q, %q", c.in, last, first, c.last, c.first)
		}
	}
}

func TestResolveAutomationID(t *testing.T) {
	for _, c := range []struct{ explicit, profile, want string }{
		{"explicit-id", "kafri", "explicit-id"}, // explicit wins over the account table
		{"", "kafri", "c741d791-3e3b-4102-b95c-f94549949c9a"},
		{"", "SCLC", "85619cc6-5e25-4219-ad12-fc9877aaf841"},
		{"", "unknown-account", ""},
		{"  ", "", ""},
	} {
		if got := ResolveAutomationID(c.explicit, c.profile); got != c.want {
			t.Errorf("ResolveAutomationID(%q, %q) = %q, want %q", c.explicit, c.profile, got, c.want)
		}
	}
}

func TestDecodeJwtID(t *testing.T) {
	jwt := func(payload string) string {
		return "header." + base64.StdEncoding.EncodeToString([]byte(payload)) + ".sig"
	}
	if got := DecodeJwtID(jwt(`{"id":42}`)); got != 42.0 {
		t.Errorf("numeric id = %v, want 42", got)
	}
	if got := DecodeJwtID(jwt(`{"id":"abc"}`)); got != "abc" {
		t.Errorf("string id = %v, want abc", got)
	}
	for _, bad := range []string{"", "garbage", jwt(`{"sub":"x"}`), jwt(`{"id":true}`)} {
		if got := DecodeJwtID(bad); got != nil {
			t.Errorf("DecodeJwtID(%q) = %v, want nil", bad, got)
		}
	}
}
