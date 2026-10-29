package settings

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// SectionKind says whether a section's payload is one object or a list of items.
type SectionKind string

// The section kinds.
const (
	KindObject SectionKind = "object"
	KindList   SectionKind = "list"
)

// SettingsSection is one catalog entry.
type SettingsSection struct {
	Key      string   // stable id used in the `sections` param + output
	Label    string   //
	Tags     []string // categories for coarse filtering / listing (many-to-many)
	Path     string   // GET path (query string included where the UI sends one)
	Kind     SectionKind
	Envelope string   // dot-path to the payload inside the response (e.g. "data"); "" = none
	MatchKey string   // list: field to match items across envs; "" => content-set diff
	Ignore   []string // extra fields to strip before diffing (beyond global noise)
	// Derive is a custom fetch (overrides Path); used for crawled sections.
	Derive func(ctx context.Context, s *Source) ([]any, error)
	// Sync is the additive write capability; nil => reported under skippedSections.
	Sync SectionSyncer
}

// ---- pluggable sync ---------------------------------------------------------

// PayerMaps are the payer lookups every syncer needs to remap payer references prod->pre.
type PayerMaps struct {
	ProdToPre     map[string]string
	ProdNameByUID map[string]string
	PreNameByUID  map[string]string
}

// SyncPlanResult is one domain's plan.
type SyncPlanResult struct {
	Actions           []SyncAction
	Skipped           []SyncSkip
	PayerLinkFindings []PayerLinkFinding
}

// Planner turns a syncer's crawled state into a plan. It is pure: all I/O happened in Crawl.
type Planner func(PayerMaps) SyncPlanResult

// SectionSyncer is a domain's additive write plan, split in two so the crawl (reads) can run
// concurrently with the payer-map fetch it does not depend on, while planning stays pure.
// Domain is a stable identity used to de-dupe when several catalog sections share one syncer
// (e.g. specialties / referred-providers / referred-facilities share the specialities syncer).
type SectionSyncer interface {
	Domain() string
	Crawl(ctx context.Context, prod, pre *Source) (Planner, error)
}

// ---- diff engine result shapes ----------------------------------------------

// FieldDiff is one path-level difference. A side that is absent (not merely null) is omitted
// from the JSON, matching the TS `undefined`.
type FieldDiff struct {
	Path    string
	Prod    any
	Preprod any
}

// ChangedItem is one matched list item whose fields differ.
type ChangedItem struct {
	Key   string      `json:"key"`
	Diffs []FieldDiff `json:"diffs"`
}

// ListDiff is the diff of two normalized lists.
type ListDiff struct {
	OnlyInProd    []any
	OnlyInPreProd []any
	Changed       []ChangedItem
}

// SectionResult is one section's diff. Error set => the section failed to fetch; Diffs applies to
// object sections, the List fields to list sections.
type SectionResult struct {
	Key           string
	Label         string
	Kind          SectionKind
	Equal         bool
	Diffs         []FieldDiff
	OnlyInProd    []any
	OnlyInPreProd []any
	Changed       []ChangedItem
	Error         string
}

// ---- list_setting_sections ---------------------------------------------------

// SettingSectionInfo describes one section for list_setting_sections.
type SettingSectionInfo struct {
	Key     string      `json:"key"`
	Label   string      `json:"label"`
	Tags    []string    `json:"tags"`
	Kind    SectionKind `json:"kind"`
	Derived bool        `json:"derived"` // crawled (heavier) vs a plain single GET
	// MatchKey is the field list items are matched/scoped by (omitted => content-set diff).
	MatchKey string `json:"matchKey,omitempty"`
}

// ListSettingSectionsOpts narrows the listing (AND).
type ListSettingSectionsOpts struct {
	Tag      string
	Sections []string
	EMR      string
}

// ListSettingSectionsResult is the list_setting_sections output.
type ListSettingSectionsResult struct {
	Tags     []string             `json:"tags"`
	Sections []SettingSectionInfo `json:"sections"`
}

// ---- diff_settings -----------------------------------------------------------

// DiffSettingsOpts are the diff_settings arguments. Profile "" = none.
type DiffSettingsOpts struct {
	Profile          string
	Sections         []string
	Tags             []string
	EMR              string
	IncludeUnchanged bool
	OnProgress       shared.StepProgress
}

// DiffSettingsResult is the diff_settings output.
type DiffSettingsResult struct {
	Account          string          `json:"account"`
	ProdBase         string          `json:"prodBase"`
	PreProdBase      string          `json:"preProdBase"`
	SectionsCompared int             `json:"sectionsCompared"`
	SectionsWithDiff int             `json:"sectionsWithDiffs"`
	Sections         []SectionResult `json:"sections"`
}

// ---- get_settings --------------------------------------------------------------

// GetSettingsOpts are the get_settings arguments. The MCP tool defaults Normalized to true; this
// package does not, so the caller must pass it explicitly.
type GetSettingsOpts struct {
	Env        config.Env
	Profile    string
	Sections   []string
	Tags       []string
	EMR        string
	Normalized bool // true = stripped like diff_settings; false = raw
	OnProgress shared.StepProgress
}

// GetSettingsSection is one fetched section. Error set => per-section fetch failure (other
// sections still return). Count applies to list sections; Data is omitted when the payload was
// absent from the response.
type GetSettingsSection struct {
	Key   string
	Label string
	Tags  []string
	Kind  SectionKind
	Count *int
	Data  any
	Error string
}

// GetSettingsResult is the get_settings output.
type GetSettingsResult struct {
	Account         string               `json:"account"`
	Env             config.Env           `json:"env"`
	Base            string               `json:"base"`
	SectionsFetched int                  `json:"sectionsFetched"`
	Sections        []GetSettingsSection `json:"sections"`
}

// ---- sync actions --------------------------------------------------------------

// SyncAction is one planned write. Op is "create" | "merge"; ItemKind "speciality" | "order".
type SyncAction struct {
	Section  string
	Op       string
	ItemKind string
	TypeName string
	ItemName string // speciality name or order name, depending on ItemKind
	Method   string // POST | PUT
	Path     string
	Body     map[string]any
	Warnings []string
}

// SyncSkip is something the plan could not place.
type SyncSkip struct {
	TypeName       string `json:"typeName"`
	SpecialityName string `json:"specialityName,omitempty"`
	Reason         string `json:"reason"`
}

// SkippedSection is a selected section with no write mapping.
type SkippedSection struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// PlannedSyncAction is a SyncAction with its stable id.
type PlannedSyncAction struct {
	SyncAction
	ID string
}

// ApplyFilter is the apply selection: exactly ONE of ActionIDs / All=true is required — there is
// deliberately no default "apply everything".
type ApplyFilter struct {
	ActionIDs []string
	All       bool
}

// PlannedActionSummary is the plan-output shape of one action.
type PlannedActionSummary struct {
	ID       string         `json:"id"`
	Section  string         `json:"section"`
	Op       string         `json:"op"`
	ItemKind string         `json:"itemKind"`
	TypeName string         `json:"typeName"`
	ItemName string         `json:"itemName"`
	Method   string         `json:"method"`
	Path     string         `json:"path"`
	Summary  string         `json:"summary"`
	Warnings []string       `json:"warnings,omitempty"`
	Body     map[string]any `json:"body,omitempty"`
}

// PayerLinkFinding is a payer-link drift/orphan on a facility/provider that exists in both envs.
type PayerLinkFinding struct {
	TypeName       string   `json:"typeName"`
	SpecialityName string   `json:"specialityName"`
	ItemKind       string   `json:"itemKind"` // facility | provider
	ItemName       string   `json:"itemName"`
	ExtraInPreProd []string `json:"extraInPreProd"` // payer NAMEs linked in pre-prod but not in prod
	// MissingInPreProd are payer NAMEs linked in prod but not in pre-prod.
	MissingInPreProd []string `json:"missingInPreProd"`
	// OrphanedProdPayerUIDs are prod's own payerUids that resolve to no payer name in prod.
	OrphanedProdPayerUIDs []string `json:"orphanedProdPayerUids"`
	// OrphanedPreProdPayerUIDs are pre-prod's own payerUids that resolve to no payer name there.
	OrphanedPreProdPayerUIDs []string `json:"orphanedPreProdPayerUids"`
}

// ---- plan_settings_sync ----------------------------------------------------------

// PlanSettingsSyncOpts are the plan_settings_sync arguments. Profile "" = none.
type PlanSettingsSyncOpts struct {
	OnProgress    shared.StepProgress
	Profile       string
	Sections      []string
	Tags          []string
	EMR           string
	IncludeBodies bool
}

// PlanSettingsSyncResult is the plan_settings_sync output.
type PlanSettingsSyncResult struct {
	Account         string                 `json:"account"`
	ProdBase        string                 `json:"prodBase"`
	PreProdBase     string                 `json:"preProdBase"`
	ActionCount     int                    `json:"actionCount"`
	Actions         []PlannedActionSummary `json:"actions"`
	Skipped         []SyncSkip             `json:"skipped"`
	SkippedSections []SkippedSection       `json:"skippedSections"`
	// PayerLinkFindings is payer-link drift/orphans on facilities/providers that already exist
	// (matched by name) in both envs — audit-only, never turned into a write action.
	PayerLinkFindings []PayerLinkFinding `json:"payerLinkFindings"`
}

// ---- apply_settings_sync ----------------------------------------------------------

// ExecutedAction is one write that was attempted.
type ExecutedAction struct {
	ID       string   `json:"id"`
	Op       string   `json:"op"`
	ItemKind string   `json:"itemKind"`
	TypeName string   `json:"typeName"`
	ItemName string   `json:"itemName"`
	Method   string   `json:"method"`
	Path     string   `json:"path"`
	Status   int      `json:"status"`
	OK       bool     `json:"ok"`
	Warnings []string `json:"warnings,omitempty"`
}

// NotSelectedAction is a planned action the caller did not select.
type NotSelectedAction struct {
	ID       string `json:"id"`
	Op       string `json:"op"`
	TypeName string `json:"typeName"`
	ItemName string `json:"itemName"`
}

// ApplySettingsSyncOpts are the apply_settings_sync arguments. Profile "" = none.
type ApplySettingsSyncOpts struct {
	ApplyFilter
	Profile  string
	Sections []string
	Tags     []string
	EMR      string
	// OnWrite is the optional audit hook; the server wires it to mcpLog(warning) so every live
	// write is logged.
	OnWrite    func(message string, data map[string]any)
	OnProgress shared.StepProgress
}

// ApplySettingsSyncResult is the apply_settings_sync output.
type ApplySettingsSyncResult struct {
	Account         string              `json:"account"`
	ProdBase        string              `json:"prodBase"`
	PreProdBase     string              `json:"preProdBase"`
	PlannedCount    int                 `json:"plannedCount"`
	Executed        []ExecutedAction    `json:"executed"`
	NotSelected     []NotSelectedAction `json:"notSelected"`
	UnmatchedIDs    []string            `json:"unmatchedIds"`
	Skipped         []SyncSkip          `json:"skipped"`
	SkippedSections []SkippedSection    `json:"skippedSections"`
}
