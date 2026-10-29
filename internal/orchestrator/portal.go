package orchestrator

import (
	"regexp"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
)

// Portal helpers shared by every backend's pull_queue_item.

var (
	edgeDashRE = regexp.MustCompile(`^-|-$`)
)

// MemberFilename builds a PHI-light filename slug from a queue item's member name.
func MemberFilename(sc map[string]any) string {
	name := func(key string) string {
		s, _ := sc[key].(string)
		return jsLower(jsTrim(s))
	}
	slug := edgeDashRE.ReplaceAllString(name("MemberFirstName")+"-"+name("MemberLastName"), "")
	if slug == "" {
		slug = name("MemberFullName")
	}
	if slug == "" {
		slug = "unknown"
	}
	slug = jsSpaceRE.ReplaceAllString(strings.ReplaceAll(slug, ",", ""), "-")
	return slug + ".yaml"
}

// ToPortalRef projects the five portal fields exposed on a pull result.
func ToPortalRef(p reference.ResolvedPortal) PortalRef {
	return PortalRef{
		Matched:       p.Matched,
		Key:           p.Key,
		PortalDir:     p.PortalDir,
		BuildArtifact: p.BuildArtifact,
		Family:        string(p.Family),
	}
}
