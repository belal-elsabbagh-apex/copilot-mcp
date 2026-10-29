// Package reference holds the curated, hand-maintained static facts shared by the MCP resources
// and the queue/sweep tools: the STATIC half of the "hybrid" model. Stable facts (portal registry,
// UiPath folder ids, schemas, contracts, safety rules) are bundled here rather than fetched live,
// while anything that changes per-run (queue items, jobs, orders) is a tool.
//
// Sources (keep in sync when the sibling repo changes; <repo> = the apex-auth-submit checkout,
// formerly RPAPlaywright):
//   - <repo>/docs/uipath-production-queues.md   (queue ids, accounts, folders)
//   - <repo>/docs/portal-registry.md            (portal families, directories, build artifacts)
//   - <repo>/.planning/pull_queue_item_by_id.py (QUEUE_TO_PORTAL aliases)
//   - <repo>/packages/core/src/schema.ts        (SpecificContent schema)
//   - <repo>/.claude/skills/submissions-contract (result.json contract, queue-item input contract)
//
// Queue definition ids are re-checked against the live UiPath prod folder (list_queues).
package reference

import (
	"regexp"
	"slices"
	"strings"
)

// PortalFamily is the automation family a portal's bot is built on.
type PortalFamily string

// The portal families.
const (
	FamilyAvaility278    PortalFamily = "Availity 278"
	FamilyAvailityAuthAI PortalFamily = "Availity AuthAI"
	FamilyAerial         PortalFamily = "Aerial"
	FamilyQuickCap       PortalFamily = "QuickCap"
	FamilyMedpoint       PortalFamily = "Medpoint"
	FamilyMedhok         PortalFamily = "Medhok"
	FamilyEZNet          PortalFamily = "EZ-NET"
	FamilyCedarGate      PortalFamily = "Cedar Gate"
	FamilyStandalone     PortalFamily = "Standalone"
	FamilyOptumCuroAI    PortalFamily = "Optum / CuroAI"
	FamilyCareAffiliate  PortalFamily = "CareAffiliate"
)

// PortalEntry is a total shape — no nullable/optional fields. Sentinels carry "unknown": "" for
// strings, 0 for queue ids. Callers test those sentinels instead of nil. Aliases is never nil
// (it marshals as [], matching the TS registry).
type PortalEntry struct {
	Key              string       `json:"key"`     // canonical queue name (uppercase), e.g. "PMG"
	Aliases          []string     `json:"aliases"` // other names that map to this portal (uppercase)
	PortalDir        string       `json:"portalDir"`
	BuildArtifact    string       `json:"buildArtifact"` // <name>-auth.cjs ("" = no Playwright build yet)
	Family           PortalFamily `json:"family"`
	Account          string       `json:"account"`          // KAFRI | OSSM | SCLC | "" (unknown/empty)
	SubmitQueueDefID int          `json:"submitQueueDefId"` // 0 = unknown
	SyncQueueDefID   int          `json:"syncQueueDefId"`   // 0 = unknown
	Volume           string       `json:"volume"`           // prod volume note from the queue doc ("" = unknown)
}

// ResolvedPortal is a portal-lookup result. Matched discriminates a hit from a miss, so callers
// never deal with nil — an unmatched lookup carries empty/zero sentinels.
type ResolvedPortal struct {
	PortalEntry
	Matched bool `json:"matched"`
}

// Portals is one row per portal. submit/sync queue ids + accounts from uipath-production-queues.md;
// families + build artifacts from CLAUDE.md; aliases from the QUEUE_TO_PORTAL map.
var Portals = []PortalEntry{
	{Key: "PMG", Aliases: []string{}, PortalDir: "portals/pmg", BuildArtifact: "pmg-auth.cjs", Family: "Aerial", Account: "KAFRI", SubmitQueueDefID: 38592, SyncQueueDefID: 39491, Volume: "1,019 (highest)"},
	{Key: "OPTUM CARE NETWORK", Aliases: []string{"OPTUM"}, PortalDir: "portals/optum", BuildArtifact: "optum-auth.cjs", Family: FamilyOptumCuroAI, Account: "OSSM", SubmitQueueDefID: 118705, SyncQueueDefID: 115325, Volume: "821"},
	{Key: "SPMG", Aliases: []string{"SDPMG"}, PortalDir: "SDPMG Through Cedar Gate Auth Submit with Playwright", BuildArtifact: "sdpmg-auth.cjs", Family: "Cedar Gate", Account: "KAFRI", SubmitQueueDefID: 43446, SyncQueueDefID: 40372, Volume: "212"},
	{Key: "REGAL", Aliases: []string{}, PortalDir: "Regal Auth Submit with Playwright", BuildArtifact: "regal-auth.cjs", Family: "Standalone", Account: "OSSM", SubmitQueueDefID: 118708, SyncQueueDefID: 113961, Volume: "206"},
	{Key: "IHP", Aliases: []string{"CCI"}, PortalDir: "portals/medpoint", BuildArtifact: "medpoint-auth.cjs", Family: "Medpoint", Account: "KAFRI", SubmitQueueDefID: 43449, SyncQueueDefID: 95893, Volume: "139"},
	{Key: "PCA", Aliases: []string{}, PortalDir: "portals/pca", BuildArtifact: "pca-auth.cjs", Family: "Aerial", Account: "KAFRI", SubmitQueueDefID: 43448, SyncQueueDefID: 50071, Volume: "104"},
	{Key: "SDP", Aliases: []string{"SAN DIEGO"}, PortalDir: "San Diego Through QuickCap Auth Submit with Playwright", BuildArtifact: "sdp-auth.cjs", Family: "QuickCap", Account: "KAFRI", SubmitQueueDefID: 43445, SyncQueueDefID: 62812, Volume: "98"},
	{Key: "MOLINA", Aliases: []string{}, PortalDir: "Molina Through Availity Auth Submit with Playwright", BuildArtifact: "molina-auth.cjs", Family: "Availity AuthAI", Account: "KAFRI", SubmitQueueDefID: 78843, SyncQueueDefID: 78795, Volume: "active"},
	{Key: "BSC", Aliases: []string{}, PortalDir: "BSC Through Medhok Auth Submit with Playwright", BuildArtifact: "bsc-auth.cjs", Family: "Medhok", Account: "KAFRI", SubmitQueueDefID: 78203, SyncQueueDefID: 81420, Volume: "75"},
	{Key: "CHG", Aliases: []string{}, PortalDir: "portals/chg", BuildArtifact: "chg-auth.cjs", Family: "Standalone", Account: "KAFRI", SubmitQueueDefID: 35658, SyncQueueDefID: 40360, Volume: "active"},
	{Key: "RIOS", Aliases: []string{}, PortalDir: "Rios Through QuickCap Auth Submit with Playwright", BuildArtifact: "rios-auth.cjs", Family: "QuickCap", Account: "KAFRI", SubmitQueueDefID: 0, SyncQueueDefID: 0, Volume: ""},
	{Key: "AETNA", Aliases: []string{}, PortalDir: "portals/aetna", BuildArtifact: "aetna-auth.cjs", Family: "Availity 278", Account: "", SubmitQueueDefID: 107164, SyncQueueDefID: 120205, Volume: "0"},
	{Key: "ANTHEM", Aliases: []string{}, PortalDir: "portals/anthem", BuildArtifact: "anthem-auth.cjs", Family: "Availity 278", Account: "", SubmitQueueDefID: 102365, SyncQueueDefID: 120202, Volume: "0"},
	{Key: "HEALTHNET", Aliases: []string{"HEALTH NET"}, PortalDir: "portals/healthnet", BuildArtifact: "healthnet-auth.cjs", Family: "Availity 278", Account: "", SubmitQueueDefID: 151961, SyncQueueDefID: 151962, Volume: ""},
	{Key: "HUMANA", Aliases: []string{}, PortalDir: "portals/humana", BuildArtifact: "humana-auth.cjs", Family: "Availity AuthAI", Account: "", SubmitQueueDefID: 151963, SyncQueueDefID: 122378, Volume: ""},
	{Key: "CHMP", Aliases: []string{}, PortalDir: "portals/chmp", BuildArtifact: "chmp-auth.cjs", Family: "Availity AuthAI", Account: "", SubmitQueueDefID: 151970, SyncQueueDefID: 151971, Volume: ""},
	{Key: "SCAN", Aliases: []string{}, PortalDir: "portals/scan", BuildArtifact: "scan-auth.cjs", Family: "Availity AuthAI", Account: "", SubmitQueueDefID: 151966, SyncQueueDefID: 151967, Volume: ""},
	{Key: "CENTRAL", Aliases: []string{}, PortalDir: "", BuildArtifact: "", Family: "Availity AuthAI", Account: "", SubmitQueueDefID: 151964, SyncQueueDefID: 151965, Volume: ""},
	{Key: "CIGNA", Aliases: []string{}, PortalDir: "Cigna Auth Submit with Playwright", BuildArtifact: "cigna-auth.cjs", Family: "Standalone", Account: "", SubmitQueueDefID: 106485, SyncQueueDefID: 0, Volume: "0"},
	{Key: "IEHP", Aliases: []string{}, PortalDir: "IEHP Auth Submit with Playwright", BuildArtifact: "iehp-auth.cjs", Family: "Standalone", Account: "", SubmitQueueDefID: 121163, SyncQueueDefID: 0, Volume: "0"},
	{Key: "ALIGNMENT", Aliases: []string{"ALHC"}, PortalDir: "Alignment Auth Submit with Playwright", BuildArtifact: "alignment-auth.cjs", Family: "Standalone", Account: "", SubmitQueueDefID: 52747, SyncQueueDefID: 81423, Volume: "0"},
	{Key: "IHH", Aliases: []string{}, PortalDir: "IHH Through EZ-NET Auth Submit with Playwright", BuildArtifact: "ihh-auth.cjs", Family: "EZ-NET", Account: "", SubmitQueueDefID: 43447, SyncQueueDefID: 42055, Volume: "0"},
	{Key: "CALOPTIMA", Aliases: []string{}, PortalDir: "CalOptima Auth Submit with Playwright", BuildArtifact: "caloptima-auth.cjs", Family: "Standalone", Account: "", SubmitQueueDefID: 134596, SyncQueueDefID: 152810, Volume: ""},
	{Key: "CAREFIRST", Aliases: []string{}, PortalDir: "CareFirst Auth Submit with Playwright", BuildArtifact: "carefirst-auth.cjs", Family: "Standalone", Account: "", SubmitQueueDefID: 0, SyncQueueDefID: 0, Volume: ""},
	{Key: "UHC", Aliases: []string{}, PortalDir: "portals/uhc", BuildArtifact: "", Family: "Standalone", Account: "", SubmitQueueDefID: 168862, SyncQueueDefID: 0, Volume: ""},
	{Key: "CCIPA", Aliases: []string{}, PortalDir: "portals/medpoint", BuildArtifact: "medpoint-auth.cjs", Family: "Medpoint", Account: "KAFRI", SubmitQueueDefID: 88575, SyncQueueDefID: 152540, Volume: "14"},
	{Key: "TRICARE", Aliases: []string{}, PortalDir: "portals/tricare", BuildArtifact: "tricare-auth.cjs", Family: FamilyCareAffiliate, Account: "", SubmitQueueDefID: 155637, SyncQueueDefID: 156575, Volume: ""},
	{Key: "ALTURA", Aliases: []string{}, PortalDir: "Altura Auth Submit with Playwright", BuildArtifact: "altura-auth.cjs", Family: "Standalone", Account: "", SubmitQueueDefID: 162543, SyncQueueDefID: 156472, Volume: ""},
	{Key: "CENCAL", Aliases: []string{}, PortalDir: "portals/cencal", BuildArtifact: "cencal-auth.cjs", Family: "Standalone", Account: "", SubmitQueueDefID: 168205, SyncQueueDefID: 168394, Volume: ""},
}

// accountAutomationIDs is ordered like the TS record so the resource JSON matches key-for-key.
type accountAutomationIDs struct {
	KAFRI string `json:"KAFRI"`
	SCLC  string `json:"SCLC"`
	OSSM  string `json:"OSSM"`
}

// AccountAutomationIDs holds the real UiPath automation ids per account, confirmed from
// SpecificContent.automationId on a successful queue item for each. Portals and accounts are
// many-to-many (a portal is a payer's queue any provider group can submit through), so this is
// keyed by account, not by portal — do not conflate with PortalEntry.Account, which is a separate,
// approximate per-portal field. Accounts with no confirmed automationId yet (no successful job to
// sample) are omitted rather than guessed. Marshals as the TS record; look one up with AutomationID.
var AccountAutomationIDs = accountAutomationIDs{
	KAFRI: "c741d791-3e3b-4102-b95c-f94549949c9a",
	SCLC:  "85619cc6-5e25-4219-ad12-fc9877aaf841",
	OSSM:  "21a5ab32-817c-4a4a-95a0-bff16605ca6b",
}

// AutomationID is the confirmed automation id for an uppercase account name.
func AutomationID(account string) (string, bool) {
	switch account {
	case "KAFRI":
		return AccountAutomationIDs.KAFRI, true
	case "SCLC":
		return AccountAutomationIDs.SCLC, true
	case "OSSM":
		return AccountAutomationIDs.OSSM, true
	}
	return "", false
}

// queueNameSuffixRE strips the trailing " auth (sync|submit) queue" suffix from a
// QueueDefinitions.Name (e.g. "SCAN auth submit queue" -> "SCAN", "MOLINA auth sync queue" -> "MOLINA").
var queueNameSuffixRE = regexp.MustCompile(`(?i)\s+auth\s+(?:sync|submit)\s+queue\s*$`)

// NormalizeQueueName drops the " auth (sync|submit) queue" suffix and trims.
func NormalizeQueueName(raw string) string {
	return strings.TrimSpace(queueNameSuffixRE.ReplaceAllString(raw, ""))
}

var noPortal = ResolvedPortal{PortalEntry: PortalEntry{
	Aliases: []string{},
	Family:  FamilyStandalone,
}}

// ResolvePortal resolves a portal from a queue name (canonical or alias, case-insensitive, with
// or without the "auth ... queue" suffix). Always returns a total ResolvedPortal — check Matched
// to tell a hit from a miss.
func ResolvePortal(queueName string) ResolvedPortal {
	key := strings.ToUpper(NormalizeQueueName(queueName))
	if key == "" {
		return noPortal
	}
	return findPortal(func(p PortalEntry) bool { return p.Key == key || slices.Contains(p.Aliases, key) })
}

// ResolvePortalByDefID resolves a portal from a QueueDefinitionId (submit or sync). Same total
// contract.
func ResolvePortalByDefID(id int) ResolvedPortal {
	if id == 0 {
		return noPortal
	}
	return findPortal(func(p PortalEntry) bool { return p.SubmitQueueDefID == id || p.SyncQueueDefID == id })
}

func findPortal(match func(PortalEntry) bool) ResolvedPortal {
	i := slices.IndexFunc(Portals, match)
	if i < 0 {
		return noPortal
	}
	return ResolvedPortal{PortalEntry: Portals[i], Matched: true}
}
