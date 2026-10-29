package orchestrator

import (
	"strings"
	"testing"
)

func TestNormalizeErrorCollapsesIDsAndTimestampsToOneSignature(t *testing.T) {
	a := NormalizeError("Selector not found for order 998877 at 2026-06-30T10:00:30Z (job 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d)")
	b := NormalizeError("Selector not found for order 112233 at 2026-06-29T22:14:01Z (job 9f8e7d6c-5b4a-3210-fedc-ba9876543210)")
	if a != b {
		t.Fatalf("signatures differ: %q vs %q", a, b)
	}
	for _, want := range []string{"selector not found", "<id>", "<n>", "<ts>"} {
		if !strings.Contains(a, want) {
			t.Errorf("signature %q missing %q", a, want)
		}
	}
}

func TestNormalizeErrorLowercasesStripsQuotesCollapsesWhitespaceAndTruncates(t *testing.T) {
	got := NormalizeError("  Login   FAILED:  \"bad creds\"  \n\n with `token` ")
	if want := "login failed: bad creds with token"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if n := len(NormalizeError(strings.Repeat("x", 300))); n != 160 {
		t.Errorf("truncated length = %d, want 160", n)
	}
}

// The signature is a GitHub-search dedupe key, so it must keep JS semantics: Unicode whitespace
// collapses, and the 160 cap counts UTF-16 units (an emoji is two).
func TestNormalizeErrorKeepsJavaScriptStringSemantics(t *testing.T) {
	if got, want := NormalizeError("a\u00a0\u00a0b\ufeff"), "a b"; got != want {
		t.Errorf("unicode whitespace: got %q, want %q", got, want)
	}
	got := NormalizeError(strings.Repeat("😀", 100))
	if want := strings.Repeat("😀", 80); got != want {
		t.Errorf("surrogate-pair cap: got %d runes, want 80", len([]rune(got)))
	}
}

func TestFirstLineKeepsOnlyTheTrimmedFirstLine(t *testing.T) {
	if got := FirstLine("  boom  \nstack frame 1\nstack frame 2"); got != "boom" {
		t.Errorf("got %q", got)
	}
}

func TestFirstLineCapsAtOneHundredTwentyCharacters(t *testing.T) {
	if n := len(FirstLine(strings.Repeat("y", 300))); n != 120 {
		t.Errorf("length = %d, want 120", n)
	}
}

func TestSignatureMarkerIsTheExactHiddenComment(t *testing.T) {
	got := SignatureMarker("selector not found for order <n>")
	if want := "<!-- fault-signature: selector not found for order <n> -->"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFaultSearchQueryTargetsRepoOpenIssuesAndMarkerPhrase(t *testing.T) {
	sig := "selector not found for order <n>"
	got := FaultSearchQuery("acme/repo", sig)
	want := `repo:acme/repo is:issue is:open in:body "fault-signature: selector not found for order <n>"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// the quoted phrase occurs verbatim inside the marker, so a search finds its own issue
	_, rest, _ := strings.Cut(got, `in:body "`)
	if phrase := strings.TrimSuffix(rest, `"`); !strings.Contains(SignatureMarker(sig), phrase) {
		t.Errorf("marker does not contain search phrase %q", phrase)
	}
}
