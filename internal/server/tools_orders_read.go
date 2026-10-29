package server

import (
	"context"
	"net/url"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerOrderReadTools) }

// ---- get_order -------------------------------------------------------------

type getOrderArgs struct {
	OrderUID string     `json:"orderUid" jsonschema:"Order UID to fetch"`
	Env      config.Env `json:"env" jsonschema:"Which env the order lives in (required)"`
	Profile  string     `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
}

type getOrderResult struct {
	OrderUID string     `json:"orderUid"`
	Env      config.Env `json:"env"`
	copilot.OrderVerify
	Documents []copilot.OrderDocument `json:"documents,omitempty"`
}

// prefetchedClient serves one GET from a request already in flight (so get_order's
// medicalAuthorizations listing overlaps the order fetch) and passes everything else through.
type prefetchedClient struct {
	copilot.Client
	path string
	done chan struct{}
	resp copilot.Response
	err  error
}

func (c *prefetchedClient) Req(ctx context.Context, method, path string, body copilot.ReqBody) (copilot.Response, error) {
	if method != "GET" || path != c.path {
		return c.Client.Req(ctx, method, path, body)
	}
	select {
	case <-c.done:
		return c.resp, c.err
	case <-ctx.Done():
		return copilot.Response{}, ctx.Err()
	}
}

func getOrder(ctx context.Context, _ *mcp.CallToolRequest, in getOrderArgs) (any, error) {
	sess, err := copilot.Connect(ctx, in.Env, in.Profile)
	if err != nil {
		return nil, err
	}
	// The medicalAuthorizations listing only needs the uid, so it runs while the order is fetched;
	// OrderDocuments then reads the listing from the prefetch instead of re-requesting it.
	pre := &prefetchedClient{
		Client: sess.Client, done: make(chan struct{}),
		path: "/api/v1/medicalAuthorizations?orderUid=" + url.PathEscape(in.OrderUID),
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(pre.done)
		pre.resp, pre.err = sess.Client.Req(fetchCtx, "GET", pre.path, copilot.ReqBody{})
	})
	order, err := copilot.FetchOrder(ctx, pre, in.OrderUID)
	if err != nil {
		cancel()
		wg.Wait()
		return nil, err
	}
	docs := copilot.OrderDocuments(ctx, pre, order, in.OrderUID, in.Profile)
	return getOrderResult{OrderUID: in.OrderUID, Env: in.Env, OrderVerify: copilot.NormalizeOrder(order), Documents: docs}, nil
}

// ---- search_orders / get_order_category_stats ------------------------------

// orderFilterArgs is the filter-dimension half of the search_orders / get_order_category_stats
// input (TS orderFilterDimensionsSchema).
type orderFilterArgs struct {
	Type                 string   `json:"type,omitempty" jsonschema:"Order category, e.g. 'Outbound Referral' (default) or 'PCP Notes'"`
	Locations            []string `json:"locations,omitempty" jsonschema:"Clinic location names to filter to"`
	Insurances           []string `json:"insurances,omitempty" jsonschema:"Insurance/payer names to filter to"`
	ReferredTo           []string `json:"referredTo,omitempty" jsonschema:"referredFacilityUid/referredProviderUid values to filter to"`
	OrderType            []string `json:"orderType,omitempty" jsonschema:"Order type names, e.g. 'Consultation Referral', 'Follow-up Visit', 'Imaging', 'Injections', 'Medical Supplies', 'Other', 'PCP Notes'"`
	AuthRequired         []string `json:"authRequired,omitempty" jsonschema:"Authorization Required filter (UI strings)"`
	AuthStatus           []string `json:"authStatus,omitempty" jsonschema:"Authorization Status filter"`
	UploadStatusAuth     []string `json:"uploadStatusAuth,omitempty" jsonschema:"Upload Auth Status filter"`
	UploadStatusFax      []string `json:"uploadStatusFax,omitempty" jsonschema:"Upload Fax Status filter"`
	HasAuthScreenshot    *bool    `json:"hasAuthScreenshot,omitempty" jsonschema:"true = Pending Final Approval, false = Pending Review & Submission"`
	SendFax              *bool    `json:"sendFax,omitempty" jsonschema:"Send Fax filter"`
	MissingNotes         *bool    `json:"missingNotes,omitempty" jsonschema:"Missing Provider Notes filter"`
	MRN                  string   `json:"mrn,omitempty" jsonschema:"Free-text MRN search"`
	Search               string   `json:"search,omitempty" jsonschema:"Free-text patient/order search"`
	FromDate             string   `json:"fromDate,omitempty" jsonschema:"Creation Date range start, MM-DD-YYYY"`
	ToDate               string   `json:"toDate,omitempty" jsonschema:"Creation Date range end, MM-DD-YYYY"`
	OrderDateFrom        string   `json:"orderDateFrom,omitempty" jsonschema:"Order Date range start, MM-DD-YYYY"`
	OrderDateTo          string   `json:"orderDateTo,omitempty" jsonschema:"Order Date range end, MM-DD-YYYY"`
	AppointmentDateFrom  string   `json:"appointmentDateFrom,omitempty" jsonschema:"Appointment Date range start, MM-DD-YYYY"`
	AppointmentDateTo    string   `json:"appointmentDateTo,omitempty" jsonschema:"Appointment Date range end, MM-DD-YYYY"`
	SubmissionDateFrom   string   `json:"submissionDateFrom,omitempty" jsonschema:"Submission Date range start, MM-DD-YYYY"`
	SubmissionDateTo     string   `json:"submissionDateTo,omitempty" jsonschema:"Submission Date range end, MM-DD-YYYY"`
	NotificationDateFrom string   `json:"notificationDateFrom,omitempty" jsonschema:"Notification Date range start, MM-DD-YYYY"`
	NotificationDateTo   string   `json:"notificationDateTo,omitempty" jsonschema:"Notification Date range end, MM-DD-YYYY"`
}

// dimensions maps the tool args onto the domain's filter input (sort is search-only).
func (a orderFilterArgs) dimensions(sort string) copilot.OrderFilterDimensions {
	return copilot.OrderFilterDimensions{
		Type: a.Type, Locations: a.Locations, Insurances: a.Insurances, ReferredTo: a.ReferredTo,
		OrderType: a.OrderType, AuthRequired: a.AuthRequired, AuthStatus: a.AuthStatus,
		UploadStatusAuth: a.UploadStatusAuth, UploadStatusFax: a.UploadStatusFax,
		HasAuthScreenshot: a.HasAuthScreenshot, SendFax: a.SendFax, MissingNotes: a.MissingNotes,
		MRN: a.MRN, Search: a.Search, FromDate: a.FromDate, ToDate: a.ToDate,
		OrderDateFrom: a.OrderDateFrom, OrderDateTo: a.OrderDateTo,
		AppointmentDateFrom: a.AppointmentDateFrom, AppointmentDateTo: a.AppointmentDateTo,
		SubmissionDateFrom: a.SubmissionDateFrom, SubmissionDateTo: a.SubmissionDateTo,
		NotificationDateFrom: a.NotificationDateFrom, NotificationDateTo: a.NotificationDateTo,
		Sort: sort,
	}
}

type searchOrdersArgs struct {
	Env        config.Env `json:"env" jsonschema:"Which env to search (required)"`
	Profile    string     `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	PageSize   *int       `json:"pageSize,omitempty" jsonschema:"Rows per page (default 100)"`
	PageNumber *int       `json:"pageNumber,omitempty" jsonschema:"1-based page number (default 1)"`
	Sort       string     `json:"sort,omitempty" jsonschema:"e.g. 'creationDate:desc'"`
	orderFilterArgs
}

type categoryStatsArgs struct {
	Env     config.Env `json:"env" jsonschema:"Which env to query (required)"`
	Profile string     `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	orderFilterArgs
}

func searchOrders(ctx context.Context, _ *mcp.CallToolRequest, in searchOrdersArgs) (any, error) {
	return copilot.SearchOrders(ctx, copilot.SearchOrdersArgs{
		Env: in.Env, Profile: in.Profile, PageSize: in.PageSize, PageNumber: in.PageNumber,
		OrderFilterDimensions: in.dimensions(in.Sort),
	})
}

func getOrderCategoryStats(ctx context.Context, _ *mcp.CallToolRequest, in categoryStatsArgs) (any, error) {
	return copilot.GetOrderCategoryStats(ctx, copilot.CategoryStatsArgs{
		Env: in.Env, Profile: in.Profile, OrderFilterDimensions: in.dimensions(""),
	})
}

// filterPatches are the schema rules of the filter dimensions (TS orderFilterDimensionsSchema).
func filterPatches() []Patch {
	itemEnum := func(vals ...string) func(*jsonschema.Schema) {
		return func(s *jsonschema.Schema) { Enum("", vals...).apply(s) }
	}
	uploadStatus := []string{"not_started", "completed", "failed", "in_progress"}
	return []Patch{
		ItemsOf("authRequired", itemEnum("Yes", "No", "Skip")),
		ItemsOf("authStatus", itemEnum("denied", "approved", "expired", "cancelled", "closed", "denied by Service Provider")),
		ItemsOf("uploadStatusAuth", itemEnum(uploadStatus...)),
		ItemsOf("uploadStatusFax", itemEnum(uploadStatus...)),
	}
}

func registerOrderReadTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:  "get_order",
		Title: "Get a Copilot order's normalized detail",
		Description: "Fetch a single EHR Copilot order by uid in a chosen env and return its normalized " +
			"detail: status/submissionStatus, patient, insurance + memberId, order type, facility, " +
			"place of service, requiredAuthorization, appointment date, ICD/CPT codes, and whether a " +
			"progress note is present. Also returns `documents`: clickable CDN links that actually exist " +
			"for the order — the auth-screenshot PDF (when hasAuthScreenshot) and the medical-authorization " +
			"summary PDF(s) (from /medicalAuthorizations). The CDN is CloudFront signed-cookie protected, so " +
			"these links open only in an authenticated Copilot browser session; they are not fetched here. " +
			"READ-ONLY. Returns {orderUid, env, ...detail, documents?} or an error if the order is not found.",
		Annotations: readOnlyHints(true),
	}, getOrder,
		MinLen("orderUid", 8),
		Enum("env", config.Envs...),
		MinLen("profile", 1),
	)

	addTool(s, &mcp.Tool{
		Name:  "search_orders",
		Title: "Search Copilot orders by dimension",
		Description: "Filter an account's Copilot orders by any combination of location, insurance, referred-to, " +
			"order type, auth/upload status, MRN, free-text search, or any of 5 date ranges, via " +
			"POST /orders/filter. READ-ONLY. Returns slim, non-PHI rows (orderUid, status, dates, insurance, " +
			"speciality, referredTo{name,npi,external}, auth/upload status — no patient data). insurance/" +
			"orderType are omitted from a row when the request already filtered to exactly one value for " +
			"that dimension (it would just repeat the filter back). Defaults to type='Outbound Referral', " +
			"pageSize=100, pageNumber=1 (1-based). Use get_order for a single order's full detail including " +
			"patient info.",
		Annotations: readOnlyHints(true),
	}, searchOrders, append([]Patch{
		Enum("env", config.Envs...),
		MinLen("profile", 1),
		Range("pageSize", 1, 100),
		OnProp("pageNumber", func(s *jsonschema.Schema) { s.Minimum = new(1.0) }),
		OnProp("sort", func(s *jsonschema.Schema) {
			s.Pattern = "^(creationDate|orderDate|submissionDate|appointmentDate|notificationDate):(asc|desc)$"
		}),
	}, filterPatches()...)...)

	addTool(s, &mcp.Tool{
		Name:  "get_order_category_stats",
		Title: "Get Copilot order counts per category folder",
		Description: "Cheap per-folder order counts (For Review, PCP Notes, Processing, Archived, with their " +
			"sub-buckets) for a given filter, via POST /orders/category/stats — same filter dimensions as " +
			"search_orders, no pagination. READ-ONLY. Response shape is passed through as returned by the BE.",
		Annotations: readOnlyHints(true),
	}, getOrderCategoryStats, append([]Patch{
		Enum("env", config.Envs...),
		MinLen("profile", 1),
	}, filterPatches()...)...)
}
