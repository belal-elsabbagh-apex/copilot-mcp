package orchestrator

import (
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
)

func TestMemberFilename(t *testing.T) {
	tests := []struct {
		name string
		sc   map[string]any
		want string
	}{
		{"first-last lowercased and trimmed", map[string]any{"MemberFirstName": " Jane ", "MemberLastName": "DOE"}, "jane-doe.yaml"},
		{"missing first leaves no dangling dash", map[string]any{"MemberLastName": "Doe"}, "doe.yaml"},
		{"missing last leaves no dangling dash", map[string]any{"MemberFirstName": "Jane"}, "jane.yaml"},
		{"falls back to full name, commas dropped, spaces dashed", map[string]any{"MemberFullName": "Doe, Jane Q"}, "doe-jane-q.yaml"},
		{"falls back to unknown", map[string]any{}, "unknown.yaml"},
		{"non-string parts count as missing", map[string]any{"MemberFirstName": 5, "MemberLastName": nil}, "unknown.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MemberFilename(tt.sc); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToPortalRefProjectsThePortalFieldsForAHit(t *testing.T) {
	want := PortalRef{Matched: true, Key: "PMG", PortalDir: "portals/pmg", BuildArtifact: "pmg-auth.cjs", Family: "Aerial"}
	if got := ToPortalRef(reference.ResolvePortal("PMG")); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestToPortalRefCarriesEmptySentinelsForAMiss(t *testing.T) {
	want := PortalRef{Family: "Standalone"}
	if got := ToPortalRef(reference.ResolvePortal("NO SUCH PORTAL")); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
