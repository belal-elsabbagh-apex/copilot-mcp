package copilot

import (
	"strings"
	"testing"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
)

func TestOrderAuditSendsPagingAndDecodesThePage(t *testing.T) {
	body := `{"orderUid":"abcdefgh","patientId":"9","nextCursor":"eyJ0cyI6IngifQ","items":[{"id":"7",` +
		`"orderUid":"abcdefgh","timestamp":"2026-11-15T12:39:20.000Z","source":"http","actionGroup":"edit",` +
		`"action":"ORDER_FIELDS_EDITED","actor":{"staffUserId":"57","displayName":"Dr X","isSystem":false},` +
		`"text":"edited the order.","details":{"changes":{"Referred Facility":{"from":"A","to":"B"}}}}]}`
	c := queued(config.Prod, Response{Status: 200, Text: body})

	page, err := OrderAudit(t.Context(), c, "abcdefgh", AuditQuery{Limit: 100, Order: "asc", Cursor: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.paths(); got[0] != "GET /api/v1/orders/abcdefgh/audit?cursor=c1&limit=100&order=asc" {
		t.Errorf("request = %v", got)
	}
	it := page.Items[0]
	if page.NextCursor == nil || it.Actor.StaffUserID != "57" || it.Action != "ORDER_FIELDS_EDITED" ||
		!strings.Contains(string(it.Details), `"Referred Facility"`) {
		t.Errorf("page = %+v", page)
	}
}

func TestOrderAuditOmitsUnsetParamsAndReportsNotFound(t *testing.T) {
	c := queued(config.Prod, Response{Status: 404, Text: `{"error":{"code":"E7155","message":"Order not found"}}`})

	_, err := OrderAudit(t.Context(), c, "abcdefgh", AuditQuery{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want not found", err)
	}
	if got := c.paths(); got[0] != "GET /api/v1/orders/abcdefgh/audit" {
		t.Errorf("request = %v", got)
	}
}
