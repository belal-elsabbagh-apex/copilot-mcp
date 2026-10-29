package copilot

// search_orders / get_order_category_stats domain logic: build the full /orders/filter
// request-body contract (reference: the copilot-orders-filter skill, ported from the OSSM
// referral-report kit) from a friendlier tool-input shape, and map results into slim, non-PHI
// rows. Read-only. Matches the find_stuck_orders / find_clone_candidates bulk-tool convention (no
// `patient` field), not get_order's full-detail-with-patient shape — this tool can return many
// rows per call.

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

// OrderFilterDimensions is the friendly filter input. Every array-valued filter is sent to the BE
// as a JSON-encoded STRING, never a bare array (the single biggest /orders/filter gotcha).
// Omitting a dimension means "no filter" — matches the FE's own omission semantics (no filter, or
// "everything selected", both send no param), so an empty array is treated the same as omitted
// rather than "match nothing". Unset scalars are "" (and omitted); unset bools are nil — an
// explicit false is sent.
type OrderFilterDimensions struct {
	Type                 string   `json:"type,omitempty"`
	Locations            []string `json:"locations,omitempty"`
	Insurances           []string `json:"insurances,omitempty"`
	ReferredTo           []string `json:"referredTo,omitempty"`
	OrderType            []string `json:"orderType,omitempty"`
	AuthRequired         []string `json:"authRequired,omitempty"`
	AuthStatus           []string `json:"authStatus,omitempty"`
	UploadStatusAuth     []string `json:"uploadStatusAuth,omitempty"`
	UploadStatusFax      []string `json:"uploadStatusFax,omitempty"`
	HasAuthScreenshot    *bool    `json:"hasAuthScreenshot,omitempty"`
	SendFax              *bool    `json:"sendFax,omitempty"`
	MissingNotes         *bool    `json:"missingNotes,omitempty"`
	MRN                  string   `json:"mrn,omitempty"`
	Search               string   `json:"search,omitempty"`
	FromDate             string   `json:"fromDate,omitempty"`
	ToDate               string   `json:"toDate,omitempty"`
	OrderDateFrom        string   `json:"orderDateFrom,omitempty"`
	OrderDateTo          string   `json:"orderDateTo,omitempty"`
	AppointmentDateFrom  string   `json:"appointmentDateFrom,omitempty"`
	AppointmentDateTo    string   `json:"appointmentDateTo,omitempty"`
	SubmissionDateFrom   string   `json:"submissionDateFrom,omitempty"`
	SubmissionDateTo     string   `json:"submissionDateTo,omitempty"`
	NotificationDateFrom string   `json:"notificationDateFrom,omitempty"`
	NotificationDateTo   string   `json:"notificationDateTo,omitempty"`
	Sort                 string   `json:"sort,omitempty"`
}

// SearchOrdersArgs are SearchOrders' inputs. Profile "" = no profile; nil paging = defaults
// (pageSize 100, pageNumber 1).
type SearchOrdersArgs struct {
	Env        config.Env
	Profile    string
	PageSize   *int
	PageNumber *int
	OrderFilterDimensions
}

// CategoryStatsArgs are GetOrderCategoryStats' inputs.
type CategoryStatsArgs struct {
	Env     config.Env
	Profile string
	OrderFilterDimensions
}

// encodeList is a non-empty array as the JSON-encoded string the BE wants; "" (omitted) for empty.
func encodeList(v []string) string {
	if len(v) == 0 {
		return ""
	}
	b, _ := shared.MarshalJSON(v)
	return string(b)
}

// BuildFilterDimensions builds the /orders/category/stats dimensions (no paging) from friendlier
// array-of-strings inputs. Pure. Omits any dimension the caller didn't set. `type` and `orderMode`
// default to the same values every other tool in this codebase hardcodes (Outbound Referral /
// both order+pcp-notes modes) unless the caller overrides `type`.
func BuildFilterDimensions(a OrderFilterDimensions) FilterDimensions {
	typ := a.Type
	if typ == "" {
		typ = "Outbound Referral"
	}
	return FilterDimensions{
		Type:              typ,
		OrderMode:         OrderMode,
		Locations:         encodeList(a.Locations),
		Insurances:        encodeList(a.Insurances),
		ReferredTo:        encodeList(a.ReferredTo),
		OrderType:         encodeList(a.OrderType),
		AuthRequired:      encodeList(a.AuthRequired),
		AuthStatus:        encodeList(a.AuthStatus),
		UploadStatusAuth:  encodeList(a.UploadStatusAuth),
		UploadStatusFax:   encodeList(a.UploadStatusFax),
		MRN:               a.MRN,
		Search:            a.Search,
		FromDate:          a.FromDate,
		ToDate:            a.ToDate,
		OrderDateFrom:     a.OrderDateFrom,
		OrderDateTo:       a.OrderDateTo,
		AppointmentFrom:   a.AppointmentDateFrom,
		AppointmentTo:     a.AppointmentDateTo,
		SubmissionFrom:    a.SubmissionDateFrom,
		SubmissionTo:      a.SubmissionDateTo,
		NotificationFrom:  a.NotificationDateFrom,
		NotificationTo:    a.NotificationDateTo,
		Sort:              a.Sort,
		HasAuthScreenshot: a.HasAuthScreenshot,
		SendFax:           a.SendFax,
		MissingNotes:      a.MissingNotes,
	}
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// BuildFilterBody builds a full /orders/filter request body (dimensions + paging). Pure.
func BuildFilterBody(a OrderFilterDimensions, pageSize, pageNumber *int) OrderFilterBody {
	return OrderFilterBody{
		PageSize:         intOr(pageSize, 100),
		PageNumber:       intOr(pageNumber, 1),
		FilterDimensions: BuildFilterDimensions(a),
	}
}

// SlimReferredTo is the referred-to summary on a slim row.
type SlimReferredTo struct {
	Name     shared.Field `json:"name,omitzero"`
	NPI      shared.Field `json:"npi,omitzero"`
	External shared.Field `json:"external,omitzero"`
}

// SlimOrderRow is the non-PHI row shape search_orders returns — no `patient` field.
type SlimOrderRow struct {
	OrderUID         shared.Field    `json:"orderUid,omitzero"`
	CreationDate     shared.Field    `json:"creationDate,omitzero"`
	Status           shared.Field    `json:"status,omitzero"`
	Category         shared.Field    `json:"category,omitzero"`
	OrderType        shared.Field    `json:"orderType,omitzero"`
	Insurance        shared.Field    `json:"insurance,omitzero"`
	Speciality       shared.Field    `json:"speciality,omitzero"`
	ReferredTo       *SlimReferredTo `json:"referredTo,omitempty"`
	AuthRequired     shared.Field    `json:"authRequired,omitzero"`
	AuthStatus       shared.Field    `json:"authStatus,omitzero"`
	UploadStatusAuth shared.Field    `json:"uploadStatusAuth,omitzero"`
	UploadStatusFax  shared.Field    `json:"uploadStatusFax,omitzero"`
}

// SlimOrder is the pure mapping from a raw BE order row to the slim shape. referredFacility and
// referredProvider are mutually exclusive on the BE row; prefer whichever is present.
func SlimOrder(o BeOrder) SlimOrderRow {
	row := SlimOrderRow{
		OrderUID:         o.Pass("orderUid"),
		CreationDate:     o.Pass("creationDate"),
		Status:           o.Pass("status"),
		Category:         o.Pass("category"),
		OrderType:        o.Pass("orderType", "name"),
		Insurance:        o.Pass("insurance", "name"),
		Speciality:       o.Pass("speciality", "name"),
		AuthRequired:     o.Pass("requiredAuthorization"),
		AuthStatus:       o.Pass("authStatus"),
		UploadStatusAuth: o.Pass("uploadStatusAuth"),
		UploadStatusFax:  o.Pass("uploadStatusFax"),
	}
	rf, rp := o["referredFacility"], o["referredProvider"]
	if truthy(rf) || truthy(rp) {
		row.ReferredTo = &SlimReferredTo{
			Name:     shared.Coalesce(shared.Pass(rf, "name"), shared.Pass(rp, "name")),
			NPI:      shared.NPIField(shared.Coalesce(shared.Pass(rf, "NPI"), shared.Pass(rf, "npi"), shared.Pass(rp, "NPI"))),
			External: shared.Pass(rf, "external"),
		}
	}
	return row
}

// StripFilterEchoedFields drops a row field when the caller's own filter already pins it to one
// value — repeating it back on every row of a 100-row page is pure restatement of the request, not
// new information. Left alone (not stripped) whenever the dimension is unfiltered or matches more
// than one value, since then per-row values can differ and are informative.
func StripFilterEchoedFields(row SlimOrderRow, a OrderFilterDimensions) SlimOrderRow {
	if len(a.Insurances) == 1 {
		row.Insurance = shared.Field{}
	}
	if len(a.OrderType) == 1 {
		row.OrderType = shared.Field{}
	}
	return row
}

// SearchOrdersResult is the search_orders result.
type SearchOrdersResult struct {
	Env                   config.Env     `json:"env"`
	Profile               string         `json:"profile"`
	PageSize              int            `json:"pageSize"`
	PageNumber            int            `json:"pageNumber"`
	TotalNumberOfElements *int           `json:"totalNumberOfElements,omitempty"`
	Count                 int            `json:"count"`
	Rows                  []SlimOrderRow `json:"rows"`
}

// SearchOrders logs in, POSTs /orders/filter with the built body, and maps the rows.
func SearchOrders(ctx context.Context, a SearchOrdersArgs) (SearchOrdersResult, error) {
	s, err := Connect(ctx, a.Env, a.Profile)
	if err != nil {
		return SearchOrdersResult{}, err
	}
	body := BuildFilterBody(a.OrderFilterDimensions, a.PageSize, a.PageNumber)
	page, err := FilterOrders(ctx, s.Client, body)
	if err != nil {
		return SearchOrdersResult{}, err
	}
	profile := a.Profile
	if profile == "" {
		profile = "(default)"
	}
	rows := make([]SlimOrderRow, len(page.Rows))
	for i, o := range page.Rows {
		rows[i] = StripFilterEchoedFields(SlimOrder(o), a.OrderFilterDimensions)
	}
	return SearchOrdersResult{
		Env: a.Env, Profile: profile, PageSize: body.PageSize, PageNumber: body.PageNumber,
		TotalNumberOfElements: page.Total, Count: len(rows), Rows: rows,
	}, nil
}

// GetOrderCategoryStats logs in and POSTs /orders/category/stats (no paging fields).
func GetOrderCategoryStats(ctx context.Context, a CategoryStatsArgs) (map[string]any, error) {
	s, err := Connect(ctx, a.Env, a.Profile)
	if err != nil {
		return nil, err
	}
	return CategoryStats(ctx, s.Client, BuildFilterDimensions(a.OrderFilterDimensions))
}
