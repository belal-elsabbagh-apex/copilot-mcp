package copilot

// get_order_audit: GET /api/v1/orders/{uid}/audit, the per-order activity trail the Copilot UI
// shows. It is the only BE record of WHO edited or submitted an order (statusTimestamps and
// actionsHistory carry only times and action kinds). The route has no BE source to cite; its
// contract was reverse-engineered from prod on 2026-11-15 (docs/copilot-order-audit-endpoint.md).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// AuditQuery is the endpoint's paging input. Zero values are not sent, so the BE defaults apply
// (limit 20, order desc).
type AuditQuery struct {
	Limit  int
	Order  string // "asc" | "desc"
	Cursor string // a previous page's nextCursor
}

// AuditActor is who wrote an entry. StaffUserID is numeric for people and "system:<name>" for
// system actors. A bot's /selected-facility callback is logged under the user its queue-item
// token belongs to, not a system actor.
type AuditActor struct {
	StaffUserID string `json:"staffUserId"`
	DisplayName string `json:"displayName"`
	IsSystem    bool   `json:"isSystem"`
}

// AuditItem is one trail entry. Details varies by action (transition, changes, attachments) and
// is passed through untouched.
type AuditItem struct {
	ID          string          `json:"id"`
	Timestamp   string          `json:"timestamp"`
	Source      string          `json:"source"`
	ActionGroup string          `json:"actionGroup"`
	Action      string          `json:"action"`
	Actor       AuditActor      `json:"actor"`
	Text        string          `json:"text"`
	Details     json.RawMessage `json:"details,omitempty"`
}

// AuditPage is one page of the trail. NextCursor is nil on the last page.
type AuditPage struct {
	OrderUID   string      `json:"orderUid"`
	Items      []AuditItem `json:"items"`
	NextCursor *string     `json:"nextCursor"`
}

// OrderAudit fetches one page of an order's audit trail.
func OrderAudit(ctx context.Context, c Client, uid string, q AuditQuery) (AuditPage, error) {
	params := url.Values{}
	if q.Limit > 0 {
		params.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Order != "" {
		params.Set("order", q.Order)
	}
	if q.Cursor != "" {
		params.Set("cursor", q.Cursor)
	}
	path := "/api/v1/orders/" + url.PathEscape(uid) + "/audit"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	r, err := c.Req(ctx, "GET", path, ReqBody{})
	if err != nil {
		return AuditPage{}, err
	}
	if r.Status == http.StatusNotFound {
		return AuditPage{}, fmt.Errorf("order %s not found in this env (or outside this profile's clinic)", uid)
	}
	if r.Status >= 400 {
		return AuditPage{}, fmt.Errorf("/orders/%s/audit failed %d: %s", uid, r.Status, shared.SliceUTF16(r.Text, 300))
	}
	var page AuditPage
	if err := json.Unmarshal([]byte(r.Text), &page); err != nil {
		return AuditPage{}, fmt.Errorf("decode /orders/%s/audit: %w", uid, err)
	}
	return page, nil
}
