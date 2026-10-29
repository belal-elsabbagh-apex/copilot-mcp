package uipath

import (
	"strconv"
	"strings"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/reference"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// Defensive readers for raw OData rows: a missing or wrongly-typed field reads as "" / 0 / {} so
// downstream code never branches on absence.

func numField(v any, k string) int64 {
	n, _ := shared.Prop(v, k).(float64)
	return int64(n)
}

func recField(v any, k string) map[string]any {
	m, _ := shared.Prop(v, k).(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// idField reads an OData Id, which is a number on the wire but a string in some fixtures. A
// missing, null or zero id reads as "" (absent).
func idField(v any, k string) string {
	switch id := shared.Prop(v, k).(type) {
	case float64:
		if id == 0 {
			return ""
		}
		return strconv.FormatFloat(id, 'f', -1, 64)
	case string:
		return id
	}
	return ""
}

func odataValues(data any) []any {
	rows, _ := shared.Prop(data, "value").([]any)
	return rows
}

// odataQuote escapes a string for an OData single-quoted literal.
func odataQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// FolderDefaults are the known UiPath folders by env (same org/tenant), used when the config
// doesn't override them via folderPathByEnv. Override per call with an explicit folder.
var folderDefaults = map[config.Env]string{
	config.Prod:    "Authorization",
	config.PreProd: "Authorization Dev Clone",
}

// ResolveFolder picks the Orchestrator folder to scope a query to:
// explicit folder > config.folderPathByEnv[env] > known default > legacy folderPath (no env).
func ResolveFolder(env config.Env, folder string) (string, error) {
	if folder != "" {
		return folder, nil
	}
	cfg, err := config.Uipath()
	if err != nil {
		return "", err
	}
	if env == "" {
		return cfg.FolderPath, nil
	}
	if p, ok := cfg.FolderPathByEnv[env]; ok {
		return p, nil
	}
	return folderDefaults[env], nil
}

// FolderScope is where a queue/job request is scoped. Both headers are sent: the QueueItems and
// QueueDefinitions endpoints are addressed by org-unit id; FolderPath ("" omits the header)
// covers endpoints that want the folder name instead.
type FolderScope struct {
	OrgUnitID  string
	FolderPath string
}

// resolveOrgUnitID is the numeric OrganizationUnitId for an env: config.folderIdByEnv > the
// bundled reference default (prod 231517 / pre_prod 434039).
func resolveOrgUnitID(env config.Env) (string, error) {
	cfg, err := config.Uipath()
	if err != nil {
		return "", err
	}
	if id, ok := cfg.FolderIDByEnv[env]; ok {
		return id, nil
	}
	return reference.FolderIDFor(env), nil
}

// scopeFor is the queue/process/trigger/item scope: the env's org-unit id always applies; an
// explicit folder overrides only the folder-path header.
func scopeFor(env config.Env, folder string) (FolderScope, error) {
	id, err := resolveOrgUnitID(env)
	if err != nil {
		return FolderScope{}, err
	}
	path, err := ResolveFolder(env, folder)
	return FolderScope{OrgUnitID: id, FolderPath: path}, err
}

// odataTime renders a `since` bound like JS Date#toISOString (UTC, millisecond precision).
func odataTime(since string) (string, error) {
	if t, ok := shared.ParseTime(since); ok {
		return t.UTC().Format("2006-01-02T15:04:05.000Z"), nil
	}
	if t, err := time.Parse("2006-01-02", since); err == nil {
		return t.UTC().Format("2006-01-02T15:04:05.000Z"), nil
	}
	return "", shared.Expectedf("invalid time value %q (use an ISO-8601 date or timestamp)", since)
}

// sinceClause is the indexed CreationTime bound for `since`.
func sinceClause(since string) (string, error) {
	ts, err := odataTime(since)
	return "CreationTime gt " + ts, err
}
