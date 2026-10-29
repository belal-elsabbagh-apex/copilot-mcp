// Package settings compares and additively syncs an account's EHR Copilot settings across its
// prod and pre-prod tenants.
//
// The BE exposes per-tenant settings under plain GET /api/v1/settings/* endpoints (see
// SettingsCatalog). We log into both envs for one account, fetch every section, and report a
// NORMALIZED diff. diff, get and plan are read-only; apply writes to pre-prod only.
//
// The hard part is cross-env noise: UIDs, dummy emails, CDN hosts, and timestamps differ between
// prod and pre-prod, so a naive deep diff flags every one of them. We therefore strip env-specific
// fields (stripNoise) and match list items on a SEMANTIC key (name), never on UID. See
// diffengine.go.
//
// Settings sync (plan/apply) is the write-side counterpart — see sync.go and the per-domain
// SectionSyncer implementations (specialities.go, orders.go).
//
// Concurrency: every operation builds a Source per client; all the operation's reads against that
// client share one 8-slot semaphore and memoize the shared fetches (order types, the speciality
// and order-name crawls). A Source never outlives its operation, so nothing is cached across
// plan and apply.
package settings
