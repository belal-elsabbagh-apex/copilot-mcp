package uipath

import (
	"context"
	"fmt"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// QueueItem is a queue item normalized into a TOTAL shape: raw OData fields are read defensively
// into "" / 0 / {} so downstream code never branches on absence.
type QueueItem struct {
	ID                int64
	Status            string
	Reference         string
	CreationTime      string
	RetryNumber       int64
	QueueDefinitionID int64
	Name              string // queue display name — from $expand=QueueDefinition (live), else the item's own possibly-stale Name
	RobotName         string // from $expand=Robot; "" if unassigned
	SpecificContent   map[string]any
}

// queueItemExpand is folded into every QueueItems query below — confirmed via live $metadata that
// QueueDefinition/Robot are valid QueueItems nav properties. MUST stay a bare expand (no nested
// $select): confirmed live that a nested $select inside $expand on THIS endpoint silently nulls
// that entity — e.g. `Robot($select=Name)` returns Robot:null even when the item has a robot —
// while the identical nested-$select pattern works fine on Jobs (see jobExpand). toQueueItem only
// reads .Name off the full object, so the larger payload costs nothing downstream.
const queueItemExpand = "QueueDefinition,Robot"

func toQueueItem(raw any) QueueItem {
	name := shared.Str(recField(raw, "QueueDefinition"), "Name")
	if name == "" {
		name = shared.Str(raw, "Name")
	}
	return QueueItem{
		ID:                numField(raw, "Id"),
		Status:            shared.Str(raw, "Status"),
		Reference:         shared.Str(raw, "Reference"),
		CreationTime:      shared.Str(raw, "CreationTime"),
		RetryNumber:       numField(raw, "RetryNumber"),
		QueueDefinitionID: numField(raw, "QueueDefinitionId"),
		Name:              name,
		RobotName:         shared.Str(recField(raw, "Robot"), "Name"),
		SpecificContent:   recField(raw, "SpecificContent"),
	}
}

func mapRows[T any](data any, conv func(any) T) []T {
	rows := odataValues(data)
	out := make([]T, len(rows))
	for i, r := range rows {
		out[i] = conv(r)
	}
	return out
}

// GetQueueItem reads a single queue item by transaction/item id.
func GetQueueItem(ctx context.Context, itemID int64, scope FolderScope) (QueueItem, error) {
	data, err := get(ctx, fmt.Sprintf("/odata/QueueItems(%d)", itemID), map[string]string{"$expand": queueItemExpand}, scope.FolderPath, scope.OrgUnitID)
	if err != nil {
		return QueueItem{}, err
	}
	return toQueueItem(data), nil
}

// getQueueDefinitionName is the canonical queue display name for a QueueDefinition id, "" if
// unresolved — the item's own Name field is sometimes empty/stale, so this is the source of truth.
func getQueueDefinitionName(ctx context.Context, queueDefID int64, scope FolderScope) (string, error) {
	data, err := get(ctx, fmt.Sprintf("/odata/QueueDefinitions(%d)", queueDefID), map[string]string{"$select": "Name"}, scope.FolderPath, scope.OrgUnitID)
	if err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return shared.Str(data, "Name"), nil
}

// ---- Queues / releases / triggers (discovery reads) ------------------------

// QueueDefinition is a queue (not its items), normalized total.
type QueueDefinition struct {
	ID           int64
	Name         string
	Description  string
	CreationTime string
}

// nameFilterParams is the shared paging/sort/filter of the by-name discovery reads.
func nameFilterParams(sel, nameContains string, top int) map[string]string {
	params := map[string]string{"$orderby": "Name asc", "$top": fmt.Sprint(top), "$select": sel}
	if needle := strings.TrimSpace(nameContains); needle != "" {
		params["$filter"] = fmt.Sprintf("contains(Name, '%s')", odataQuote(needle))
	}
	return params
}

// listQueueDefinitions lists the folder's queue definitions, sorted by name. nameContains ""
// = no filter. Dev-clone queue ids differ from the prod ids in the static PORTALS registry, so
// this is how pre_prod queueDefIds are discovered.
func listQueueDefinitions(ctx context.Context, scope FolderScope, nameContains string, top int) ([]QueueDefinition, error) {
	data, err := get(ctx, "/odata/QueueDefinitions", nameFilterParams("Id,Name,Description,CreationTime", nameContains, top), scope.FolderPath, scope.OrgUnitID)
	if err != nil {
		return nil, err
	}
	return mapRows(data, func(raw any) QueueDefinition {
		return QueueDefinition{
			ID:           numField(raw, "Id"),
			Name:         shared.Str(raw, "Name"),
			Description:  shared.Str(raw, "Description"),
			CreationTime: shared.Str(raw, "CreationTime"),
		}
	}), nil
}

// Release is a release ("process" in the Orchestrator UI) — the startable unit a job runs.
// ProcessVersion is the package version the release is pinned to (a job resolves it at START
// time, so verify the pin here before start_job).
type Release struct {
	ID              int64
	Key             string // GUID — what StartJobs takes as ReleaseKey
	Name            string
	ProcessKey      string // package id
	ProcessVersion  string
	IsLatestVersion bool
}

// listReleases lists the folder's releases (processes), sorted by name. nameContains "" = no filter.
func listReleases(ctx context.Context, scope FolderScope, nameContains string, top int) ([]Release, error) {
	data, err := get(ctx, "/odata/Releases", nameFilterParams("Id,Key,Name,ProcessKey,ProcessVersion,IsLatestVersion", nameContains, top), scope.FolderPath, scope.OrgUnitID)
	if err != nil {
		return nil, err
	}
	return mapRows(data, func(raw any) Release {
		latest, _ := shared.Prop(raw, "IsLatestVersion").(bool)
		return Release{
			ID:              numField(raw, "Id"),
			Key:             shared.Str(raw, "Key"),
			Name:            shared.Str(raw, "Name"),
			ProcessKey:      shared.Str(raw, "ProcessKey"),
			ProcessVersion:  shared.Str(raw, "ProcessVersion"),
			IsLatestVersion: latest,
		}
	}), nil
}

// Trigger is a ProcessSchedules row. Queue triggers carry a non-zero QueueDefinitionID; time
// triggers have 0/"" sentinels there.
type Trigger struct {
	ID                       int64
	Name                     string
	Enabled                  bool
	ReleaseID                int64
	ReleaseKey               string
	ReleaseName              string
	QueueDefinitionID        int64
	QueueDefinitionName      string
	ItemsActivationThreshold int64
	MaxJobsForActivation     int64
}

// listTriggers lists the folder's triggers. There is no separate queue-trigger collection in the
// OData API — queue triggers are ProcessSchedules rows with a QueueDefinitionId. This is the
// "does the queue trigger's ReleaseId point at MY dev release?" check.
func listTriggers(ctx context.Context, scope FolderScope, top int) ([]Trigger, error) {
	data, err := get(ctx, "/odata/ProcessSchedules", map[string]string{
		"$top": fmt.Sprint(top),
		// ProcessScheduleDto already denormalizes ReleaseName/QueueDefinitionName as plain columns
		// (confirmed via $metadata — no $expand needed/available for Release/QueueDefinition here);
		// $select just trims the rest of the row (MachineRobots, Tags, cron fields, …) that this
		// shape doesn't use.
		"$select": "Id,Name,Enabled,ReleaseId,ReleaseKey,ReleaseName,QueueDefinitionId," +
			"QueueDefinitionName,ItemsActivationThreshold,MaxJobsForActivation",
	}, scope.FolderPath, scope.OrgUnitID)
	if err != nil {
		return nil, err
	}
	return mapRows(data, func(raw any) Trigger {
		enabled, _ := shared.Prop(raw, "Enabled").(bool)
		return Trigger{
			ID:                       numField(raw, "Id"),
			Name:                     shared.Str(raw, "Name"),
			Enabled:                  enabled,
			ReleaseID:                numField(raw, "ReleaseId"),
			ReleaseKey:               shared.Str(raw, "ReleaseKey"),
			ReleaseName:              shared.Str(raw, "ReleaseName"),
			QueueDefinitionID:        numField(raw, "QueueDefinitionId"),
			QueueDefinitionName:      shared.Str(raw, "QueueDefinitionName"),
			ItemsActivationThreshold: numField(raw, "ItemsActivationThreshold"),
			MaxJobsForActivation:     numField(raw, "MaxJobsForActivation"),
		}
	}), nil
}

// listQueueItems lists a queue's items, newest first, filtered by QueueDefinitionId and, when
// status is non-empty, Status. Capped at top.
//
// No `$select` here: combining an outer $select with the bare $expand this endpoint requires (see
// queueItemExpand) makes UiPath's OData controller 400 with "Could not find a property named
// 'Name' on type ... QueueItemDto" — confirmed live, 100% repro. GetQueueItem already fetches the
// full object for the same reason; toQueueItem only reads the fields it needs, so the larger
// payload costs nothing downstream.
func listQueueItems(ctx context.Context, queueDefID int64, scope FolderScope, status string, top int) ([]QueueItem, error) {
	filters := []string{fmt.Sprintf("QueueDefinitionId eq %d", queueDefID)}
	if status != "" {
		filters = append(filters, fmt.Sprintf("Status eq '%s'", odataQuote(status)))
	}
	data, err := get(ctx, "/odata/QueueItems", map[string]string{
		"$filter":  strings.Join(filters, " and "),
		"$orderby": "CreationTime desc",
		"$top":     fmt.Sprint(top),
		"$expand":  queueItemExpand,
	}, scope.FolderPath, scope.OrgUnitID)
	if err != nil {
		return nil, err
	}
	return mapRows(data, toQueueItem), nil
}
