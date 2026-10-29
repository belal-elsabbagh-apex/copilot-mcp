package server

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	jsonMIME       = "application/json"
	portalURIBase  = "copilot://reference/portal/"
	portalTemplate = portalURIBase + "{name}"
)

// Resources are static, bundled reference data (see internal/reference). Live data
// (queues/jobs/orders) is exposed via tools, not resources.
type staticResource struct{ name, uri, title, description string }

var staticResources = []staticResource{
	{"portals", "copilot://reference/portals", "Portal registry",
		"Auth-submit portals: queue name -> queue def ids, account, platform family, build artifact, volume."},
	{"account-automation-ids", "copilot://reference/account-automation-ids", "Account automation ids",
		"Real UiPath automation ids per account (build_queue_item's automationId arg), confirmed from a successful queue item's SpecificContent. Accounts with no successful job yet are absent, not guessed."},
	{"orchestrators", "copilot://reference/orchestrators", "Orchestrators (UiPath + Apex) by env",
		"Per orchestrator and env: folder name + ids (UiPath: OrganizationUnitId/FolderKey/real-BE-calls; Apex: resolved at runtime), the write rule, job-state / queue-item-status label maps, terminal sets, and notes on many-jobs-per-transaction, Apex log availability, and never-started jobs."},
	{"queue-item-schema", "copilot://schema/queue-item", "Queue item (SpecificContent) schema",
		"Base SpecificContent fields shared across portal auth-submit queue items."},
	{"result-contract", "copilot://schema/result-contract", "result.json output contract",
		"The result.json contract every portal automation writes and UiPath reads."},
	{"order-lifecycle", "copilot://reference/order-lifecycle", "Copilot order lifecycle",
		"Order statuses (drafted -> forReview -> inProgress) and tolerated error codes."},
	{"safety-rules", "copilot://reference/safety-rules", "Safety rules",
		"Invariants that keep test/dev activity from touching prod (IsApproved=false, pre-prod-only writes, guarded queue items, ...)."},
	{"config-guide", "copilot://reference/config-guide", "Config setup guide",
		"How to configure this server from scratch: load order, JSON template with <ASK-USER> placeholders, field docs, and verification steps (doctor)."},
}

// resourceData is the value each static resource serves, keyed by URI.
func resourceData() map[string]any {
	return map[string]any{
		"copilot://reference/portals":                reference.Portals,
		"copilot://reference/account-automation-ids": reference.AccountAutomationIDs,
		"copilot://reference/orchestrators":          orchestratorReference(),
		"copilot://schema/queue-item":                reference.QueueItemSchema,
		"copilot://schema/result-contract":           reference.ResultContract,
		"copilot://reference/order-lifecycle":        reference.OrderLifecycle,
		"copilot://reference/safety-rules":           reference.SafetyRules,
		"copilot://reference/config-guide":           reference.ConfigGuide,
	}
}

func jsonContents(uri string, v any) (*mcp.ReadResourceResult, error) {
	text, err := renderJSON(v)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: jsonMIME, Text: text}}}, nil
}

func (s *Server) registerResources() {
	data := resourceData()
	for _, r := range staticResources {
		body := data[r.uri]
		s.AddResource(
			&mcp.Resource{Name: r.name, URI: r.uri, Title: r.title, Description: r.description, MIMEType: jsonMIME},
			func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return jsonContents(req.Params.URI, body)
			},
		)
	}
	s.registerPortalResources()
}

// registerPortalResources serves the per-portal lookup: a resource template (read one portal by
// queue name or alias, case-insensitive) plus one concrete resource per portal so resources/list
// still enumerates them — the TS template had a list callback, the Go SDK's templates do not.
func (s *Server) registerPortalResources() {
	const title = "Portal by queue name"
	read := func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		name, err := url.PathUnescape(strings.TrimPrefix(req.Params.URI, portalURIBase))
		if err != nil {
			name = strings.TrimPrefix(req.Params.URI, portalURIBase)
		}
		if portal, ok := findPortal(name); ok {
			return jsonContents(req.Params.URI, portal)
		}
		return jsonContents(req.Params.URI, map[string]string{"error": "unknown portal: " + name})
	}
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "portal",
		URITemplate: portalTemplate,
		Title:       title,
		Description: "Look up a single portal (queue ids, account, family, build artifact) by its queue name or alias.",
		MIMEType:    jsonMIME,
	}, read)
	for _, p := range reference.Portals {
		account := p.Account
		if account == "" {
			account = "(unknown)"
		}
		s.AddResource(&mcp.Resource{
			Name:        p.Key,
			URI:         portalURIBase + url.PathEscape(p.Key),
			Title:       title,
			Description: string(p.Family) + " portal for account " + account,
			MIMEType:    jsonMIME,
		}, read)
	}
}

// findPortal resolves a queue name or alias (trimmed, uppercased).
func findPortal(name string) (reference.PortalEntry, bool) {
	n := strings.ToUpper(strings.TrimSpace(name))
	i := slices.IndexFunc(reference.Portals, func(p reference.PortalEntry) bool {
		return p.Key == n || slices.Contains(p.Aliases, n)
	})
	if i < 0 {
		return reference.PortalEntry{}, false
	}
	return reference.Portals[i], true
}

func portalKeys() []string {
	keys := make([]string, len(reference.Portals))
	for i, p := range reference.Portals {
		keys[i] = p.Key
	}
	return keys
}
