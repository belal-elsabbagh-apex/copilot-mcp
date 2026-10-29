package copilot

import (
	"context"
	"fmt"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// OrderMode is the orderMode sent on every /orders/filter call (orders + pcp notes).
const OrderMode = `["orders_only_mode","pcp_notes_mode"]`

// BeOrder is one raw BE order row. It stays untyped JSON: the BE row is large and loosely typed,
// tools pass it through verbatim, and each reader takes only the subset it needs via Get/Str.
//
// Known fields worth knowing about: referredFacility and referredProvider are mutually exclusive
// (provider-targeted orders are PCP-notes sends, insurance null; payer referrals always target a
// facility). authScreenshotFileUrl is stamped with the deterministic CDN path even when the file
// is absent — hasAuthScreenshot is the existence signal (see order_docs.go).
type BeOrder map[string]any

// Get walks nested object keys; nil when any step is missing or not an object.
func (o BeOrder) Get(keys ...string) any {
	var v any = map[string]any(o)
	for _, k := range keys {
		v = shared.Prop(v, k)
	}
	return v
}

// Str is Get restricted to strings ("" when absent or not a string).
func (o BeOrder) Str(keys ...string) string {
	s, _ := o.Get(keys...).(string)
	return s
}

// Pass is shared.Pass over the row: the value at keys with TS's absent / null / value states.
func (o BeOrder) Pass(keys ...string) shared.Field { return shared.Pass(map[string]any(o), keys...) }

// OrderVerify is the read-only status subset the tools expose. Optional fields are shared.Field so
// an absent BE value is omitted (TS `undefined`) while a JSON null stays `null`.
type OrderVerify struct {
	Status                shared.Field `json:"status,omitzero"`
	SubmissionStatus      shared.Field `json:"submissionStatus,omitzero"`
	SubmissionReference   shared.Field `json:"submissionReference,omitzero"`
	Patient               shared.Field `json:"patient,omitzero"`
	Insurance             shared.Field `json:"insurance,omitzero"`
	MemberID              shared.Field `json:"memberId,omitzero"`
	Type                  shared.Field `json:"type,omitzero"`
	Facility              shared.Field `json:"facility,omitzero"`
	PlaceOfService        shared.Field `json:"placeOfService,omitzero"`
	RequiredAuthorization shared.Field `json:"requiredAuthorization,omitzero"`
	AppointmentDate       shared.Field `json:"appointmentDate,omitzero"`
	Icds                  []string     `json:"icds"`
	Cpts                  []string     `json:"cpts"`
	NotePresent           bool         `json:"notePresent"`
}

// truthy is JS truthiness for a decoded JSON value (`!!x`): objects and arrays are truthy even
// when empty.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

// codes lists the `code` of every entry of an array-of-objects field ("" for a missing code).
func codes(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, e := range list {
		out[i], _ = shared.StringProp(e, "code")
	}
	return out
}

// NormalizeOrder reduces a raw BE order to the read-only status subset the tools expose. Pure.
func NormalizeOrder(o BeOrder) OrderVerify {
	return OrderVerify{
		Status:                o.Pass("status"),
		SubmissionStatus:      o.Pass("submissionStatus"),
		SubmissionReference:   o.Pass("submissionReference"),
		Patient:               o.Pass("patient", "patientName"),
		Insurance:             o.Pass("insurance", "name"),
		MemberID:              o.Pass("insurance", "memberId"),
		Type:                  o.Pass("orderType", "name"),
		Facility:              o.Pass("referredFacility", "name"),
		PlaceOfService:        o.Pass("placeOfService"),
		RequiredAuthorization: o.Pass("requiredAuthorization"),
		AppointmentDate:       o.Pass("appointmentDate"),
		Icds:                  codes(o["ICDCodes"]),
		Cpts:                  codes(o["CPTCodes"]),
		NotePresent:           truthy(o["latestProgressNote"]),
	}
}

// FilterDimensions is the filter half of the documented /orders/filter request-body contract
// (see the copilot-orders-filter reference). Every array-valued filter is a JSON-ENCODED STRING,
// not a bare array (e.g. `locations: '["OSSM COR"]'`) — build these from real arrays with
// BuildFilterDimensions. Empty strings / nil bools are omitted from the wire body.
type FilterDimensions struct {
	Type              string `json:"type,omitempty"`
	OrderMode         string `json:"orderMode,omitempty"`
	Locations         string `json:"locations,omitempty"`
	Insurances        string `json:"insurances,omitempty"`
	ReferredTo        string `json:"referredTo,omitempty"`
	OrderType         string `json:"orderType,omitempty"`
	AuthRequired      string `json:"authRequired,omitempty"`
	AuthStatus        string `json:"authStatus,omitempty"`
	UploadStatusAuth  string `json:"uploadStatusAuth,omitempty"`
	UploadStatusFax   string `json:"uploadStatusFax,omitempty"`
	MRN               string `json:"mrn,omitempty"`
	Search            string `json:"search,omitempty"`
	FromDate          string `json:"fromDate,omitempty"`
	ToDate            string `json:"toDate,omitempty"`
	OrderDateFrom     string `json:"orderDateFrom,omitempty"`
	OrderDateTo       string `json:"orderDateTo,omitempty"`
	AppointmentFrom   string `json:"appointmentDateFrom,omitempty"`
	AppointmentTo     string `json:"appointmentDateTo,omitempty"`
	SubmissionFrom    string `json:"submissionDateFrom,omitempty"`
	SubmissionTo      string `json:"submissionDateTo,omitempty"`
	NotificationFrom  string `json:"notificationDateFrom,omitempty"`
	NotificationTo    string `json:"notificationDateTo,omitempty"`
	Sort              string `json:"sort,omitempty"`
	HasAuthScreenshot *bool  `json:"hasAuthScreenshot,omitempty"`
	SendFax           *bool  `json:"sendFax,omitempty"`
	MissingNotes      *bool  `json:"missingNotes,omitempty"`
}

// OrderFilterBody is a full /orders/filter request: paging + SearchUID + the filter dimensions.
type OrderFilterBody struct {
	PageSize   int    `json:"pageSize"`
	PageNumber int    `json:"pageNumber"`
	SearchUID  string `json:"searchUid,omitempty"`
	FilterDimensions
}

// OrdersPage is one /orders/filter result. Total is nil when the response carries no numeric
// totalNumberOfElements.
type OrdersPage struct {
	Rows  []BeOrder
	Total *int
}

// FilterOrders is the single low-level POST /orders/filter wrapper — every call site (FetchOrder,
// Verify, findStuckOrders, find_clone_candidates, SearchOrders) shares this instead of building
// its own request/response handling.
func FilterOrders(ctx context.Context, c Client, body OrderFilterBody) (OrdersPage, error) {
	r, err := c.Req(ctx, "POST", "/api/v1/orders/filter", ReqBody{JSON: body})
	if err != nil {
		return OrdersPage{}, err
	}
	if r.Status >= 400 {
		return OrdersPage{}, fmt.Errorf("/orders/filter failed %d: %s", r.Status, shared.SliceUTF16(r.Text, 300))
	}
	raw := shared.EnvelopeRows(r.Data)
	page := OrdersPage{Rows: make([]BeOrder, len(raw))}
	for i, row := range raw {
		m, _ := row.(map[string]any)
		page.Rows[i] = m
	}
	if total, ok := shared.Prop(r.Data, "totalNumberOfElements").(float64); ok {
		n := int(total)
		page.Total = &n
	}
	return page, nil
}

// CategoryStats is POST /orders/category/stats — the same filter dimensions (minus paging),
// returning per-category folder counts. The response is passed through as-is: no live sample
// exists in this codebase to type it against yet (see reference/copilot-orders-filter.md for the
// documented folder/sub-bucket names) — verify against a real account before relying on
// specific fields.
func CategoryStats(ctx context.Context, c Client, dims FilterDimensions) (map[string]any, error) {
	r, err := c.Req(ctx, "POST", "/api/v1/orders/category/stats", ReqBody{JSON: dims})
	if err != nil {
		return nil, err
	}
	if r.Status >= 400 {
		return nil, fmt.Errorf("/orders/category/stats failed %d: %s", r.Status, shared.SliceUTF16(r.Text, 300))
	}
	if m, ok := r.Data.(map[string]any); ok {
		return m, nil
	}
	return map[string]any{}, nil
}

// orderByUID is the /orders/filter lookup for one uid.
func orderByUID(ctx context.Context, c Client, uid string) (BeOrder, error) {
	page, err := FilterOrders(ctx, c, OrderFilterBody{
		SearchUID:        uid,
		PageSize:         30,
		PageNumber:       0,
		FilterDimensions: FilterDimensions{Type: "Outbound Referral", OrderMode: OrderMode},
	})
	if err != nil || len(page.Rows) == 0 {
		return nil, err
	}
	return page.Rows[0], nil
}

// FetchOrder is /orders/filter for one uid; errors if the order isn't found in this env.
func FetchOrder(ctx context.Context, c Client, uid string) (BeOrder, error) {
	o, err := orderByUID(ctx, c, uid)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, fmt.Errorf("order %s not found in this env", uid)
	}
	return o, nil
}

// Verify is the read-only status summary for an order (used by clone verify + analyze enrich).
// Swallows both "not found" and every HTTP/transport error into nil (matches the prior behavior
// of reading straight off the response body regardless of status).
func Verify(ctx context.Context, c Client, uid string) *OrderVerify {
	o, err := orderByUID(ctx, c, uid)
	if err != nil || o == nil {
		return nil
	}
	v := NormalizeOrder(o)
	return &v
}

// submitSettle is the pause between a submit and its verifying re-read.
var submitSettle = 1500 * time.Millisecond

// SubmitOrder submits a pre-prod order that is sitting at forReview (advances it to inProgress).
// pre must already be logged in and tagged pre_prod (fails closed via AssertPreProdClient).
// Retries once on a 403 missing_token (session-cookie race), mirroring every other write in this
// codebase.
func SubmitOrder(ctx context.Context, pre Client, uid string) (*OrderVerify, error) {
	if err := AssertPreProdClient(pre, "submitOrder"); err != nil {
		return nil, err
	}
	r, err := ReqWithRefresh(ctx, pre, "POST", "/api/v1/orders/"+uid+"/submit", ReqBody{
		JSON: map[string]any{"actionsHistory": []string{"submit_action"}},
	})
	if err != nil {
		return nil, err
	}
	if r.Status >= 400 {
		return nil, fmt.Errorf("/submit failed %d: %s", r.Status, shared.SliceUTF16(r.Text, 300))
	}
	if err := shared.Sleep(ctx, submitSettle); err != nil {
		return nil, err
	}
	return Verify(ctx, pre, uid), nil
}
