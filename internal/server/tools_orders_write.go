package server

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerOrderWriteTools) }

// ---- delete_preprod_order --------------------------------------------------

type deletePreprodOrderArgs struct {
	UIDs    []string `json:"uids" jsonschema:"Pre-prod orderUid(s) to delete"`
	Profile string   `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
}

type deleteResult struct {
	UID    string `json:"uid"`
	Status int    `json:"status"`
	OK     bool   `json:"ok"`
	Msg    string `json:"msg"`
}

func (s *Server) deletePreprodOrder(ctx context.Context, _ *mcp.CallToolRequest, in deletePreprodOrderArgs) (any, error) {
	sess, err := copilot.Connect(ctx, config.PreProd, in.Profile)
	if err != nil {
		return nil, err
	}
	if err := copilot.AssertPreProdClient(sess.Client, "delete_preprod_order"); err != nil {
		return nil, err
	}
	results := []deleteResult{}
	deleted := 0
	// Sequential on purpose: deletes are destructive and the audit trail should be ordered.
	for _, uid := range in.UIDs {
		r, err := sess.Client.Req(ctx, "DELETE", "/api/v1/orders/"+uid, copilot.ReqBody{})
		if err != nil {
			return nil, err
		}
		msg, ok := shared.StringProp(r.Data, "msg")
		if !ok {
			msg = shared.SliceUTF16(r.Text, 80)
		}
		okDelete := r.Status < 400
		// Audit trail: surface every destructive delete to the client log.
		s.mcpLog("warning", "deleted pre-prod order "+uid, map[string]any{"status": r.Status, "ok": okDelete})
		results = append(results, deleteResult{UID: uid, Status: r.Status, OK: okDelete, Msg: msg})
		if okDelete {
			deleted++
		}
	}
	return struct {
		Profile string         `json:"profile"`
		Deleted int            `json:"deleted"`
		Results []deleteResult `json:"results"`
	}{in.Profile, deleted, results}, nil
}

// ---- create_preprod_order --------------------------------------------------

type icdCodeArg struct {
	Code        string `json:"code"`
	Description string `json:"description,omitempty"`
}

type createPreprodOrderArgs struct {
	Profile             string       `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	PatientName         string       `json:"patientName" jsonschema:"Patient name, 'Last, First' preferred"`
	PatientBirthDate    string       `json:"patientBirthDate" jsonschema:"Patient DOB (MM/DD/YYYY or ISO)"`
	PatientPhoneNumber  string       `json:"patientPhoneNumber,omitempty" jsonschema:"Patient phone (normalized; dummy used when omitted)"`
	InsuranceName       string       `json:"insuranceName" jsonschema:"Insurance name — must be activated on the pre-prod tenant"`
	InsuranceMemberID   string       `json:"insuranceMemberId,omitempty" jsonschema:"Member id"`
	TypeUID             string       `json:"typeUid" jsonschema:"PRE-PROD order type uid (see get_settings)"`
	OrderNamesUIDs      []string     `json:"orderNamesUids" jsonschema:"PRE-PROD order name uid(s) — auto-seed CPTs; set BEFORE speciality/provider"`
	SpecialityUID       string       `json:"specialityUid,omitempty" jsonschema:"PRE-PROD speciality uid"`
	ReferredFacilityUID string       `json:"referredFacilityUid,omitempty" jsonschema:"PRE-PROD referred facility uid"`
	ReferredProviderUID string       `json:"referredProviderUid,omitempty" jsonschema:"PRE-PROD referred provider uid (referral orders)"`
	Location            string       `json:"location,omitempty" jsonschema:"Clinic location string"`
	AppointmentDate     string       `json:"appointmentDate,omitempty" jsonschema:"Appointment date (MM/DD/YYYY or ISO)"`
	ICDCodes            []icdCodeArg `json:"icdCodes,omitempty" jsonschema:"ICD codes to set after the note upload"`
	Retro               bool         `json:"retro,omitempty" jsonschema:"Retro flag"`
	PlaceOfService      string       `json:"placeOfService,omitempty" jsonschema:"Place of service — applied LAST, post-forReview"`
}

func (s *Server) createPreprodOrder(ctx context.Context, req *mcp.CallToolRequest, a createPreprodOrderArgs) (any, error) {
	sess, err := copilot.Connect(ctx, config.PreProd, a.Profile)
	if err != nil {
		return nil, err
	}
	// Surfaces MintPreprodOrder's ~15-step sequence (up to 6 /process retries at 5s apart) to the
	// client instead of leaving a multi-retry mint with no visible feedback at all.
	step := 0
	onProgress := func(message string) {
		step++
		reportProgress(ctx, req, float64(step), 0, message)
	}
	birth, err := copilot.ToMDY(a.PatientBirthDate)
	if err != nil {
		return nil, err
	}
	appointment, err := copilot.ToMDY(a.AppointmentDate)
	if err != nil {
		return nil, err
	}
	spec := copilot.MintSpec{
		PatientName:         a.PatientName,
		PatientBirthDate:    birth,
		PatientPhoneNumber:  a.PatientPhoneNumber,
		InsuranceName:       a.InsuranceName,
		InsuranceMemberID:   a.InsuranceMemberID,
		Location:            a.Location,
		TypeUID:             a.TypeUID,
		ReferredFacilityUID: a.ReferredFacilityUID,
		ReferredProviderUID: a.ReferredProviderUID,
		ClinicProviderUID:   "", // left to the FE default (no cross-account mapping here)
		OrderNamesUIDs:      a.OrderNamesUIDs,
		CPTCodes:            nil, // order names auto-seed CPTs
		Retro:               a.Retro,
		AppointmentDate:     appointment,
		PlaceOfService:      a.PlaceOfService,
	}
	if a.SpecialityUID != "" {
		spec.SpecialityUID = &a.SpecialityUID
	}
	for _, i := range a.ICDCodes {
		spec.ICDCodes = append(spec.ICDCodes, copilot.MintICD{Code: i.Code, Description: i.Description})
	}
	result, err := copilot.MintPreprodOrder(ctx, sess.Client, spec, onProgress)
	if err != nil {
		return nil, err
	}
	// Audit trail: surface every pre-prod order mint to the client log.
	data := map[string]any{"profile": a.Profile}
	if result.Verify != nil && result.Verify.Status.Set {
		data["status"] = result.Verify.Status.V
	}
	s.mcpLog("warning", "minted pre-prod order "+result.NewUID, data)
	return struct {
		Profile string `json:"profile"`
		copilot.MintResult
	}{a.Profile, result}, nil
}

// ---- submit_preprod_order --------------------------------------------------

type submitPreprodOrderArgs struct {
	Profile  string `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
	OrderUID string `json:"orderUid" jsonschema:"PRE-PROD orderUid to submit (must be at forReview)"`
}

func (s *Server) submitPreprodOrder(ctx context.Context, _ *mcp.CallToolRequest, in submitPreprodOrderArgs) (any, error) {
	sess, err := copilot.Connect(ctx, config.PreProd, in.Profile)
	if err != nil {
		return nil, err
	}
	result, err := copilot.SubmitOrder(ctx, sess.Client, in.OrderUID)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"profile": in.Profile}
	if result != nil && result.Status.Set {
		data["status"] = result.Status.V
	}
	s.mcpLog("warning", "submitted pre-prod order "+in.OrderUID, data)
	return submitResult{in.Profile, in.OrderUID, true, result}, nil
}

// submitResult is submit_preprod_order's output; verify marshals null when nothing was verified.
type submitResult struct {
	Profile   string               `json:"profile"`
	OrderUID  string               `json:"orderUid"`
	Submitted bool                 `json:"submitted"`
	Verify    *copilot.OrderVerify `json:"verify"`
}

func registerOrderWriteTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:  "find_clone_candidates",
		Title: "Find cloneable prod orders",
		Description: "Scan the most recent PROD orders and return only the ones that will actually clone to forReview — " +
			"i.e. they have a referredFacility, orderType, and orderNames. Orders missing those (common for " +
			"recent PCP-notes / order-only entries) get stuck 'incomplete' and are filtered out. Use this to " +
			"pick good uids before cloning via the clone-and-verify-order prompt.",
		Annotations: readOnlyHints(true),
	}, findCloneCandidates,
		MinLen("profile", 1),
		Range("limit", 1, 50), Default("limit", 10),
		Range("scanPages", 1, 20), Default("scanPages", 8),
	)

	addTool(s, &mcp.Tool{
		Name:  "delete_preprod_order",
		Title: "Delete pre-prod order(s)",
		Description: "Delete one or more orders from the PRE-PROD tenant via DELETE /api/v1/orders/{uid}. " +
			"Use to clean up junk or incomplete clones. PRE-PROD ONLY — never targets prod.",
		// destructive: deletes orders; idempotent: re-deleting an already-gone uid is a no-op.
		Annotations: writeHints(true, true),
	}, s.deletePreprodOrder,
		MinItems("uids", 1), ItemsOf("uids", func(s *jsonschema.Schema) { s.MinLength = new(8) }),
		MinLen("profile", 1),
	)

	addTool(s, &mcp.Tool{
		Name:  "create_preprod_order",
		Title: "Mint a pre-prod order from an explicit spec",
		Description: "Create a fresh order in the PRE-PROD tenant from explicit data (to clone an existing prod " +
			"order instead, use the clone-and-verify-order prompt, which reads the prod order via " +
			"get_order and calls this tool with the derived fields) and drive it to forReview. NEVER " +
			"submits — hand-minted test orders stop at forReview by design; use submit_preprod_order " +
			"separately, with explicit user authorization, to advance one. The sequence quirks (order " +
			"names reset speciality/provider; placeOfService applied last, post-forReview; /process " +
			"retry loop) are handled internally. Reference uids (typeUid, orderNamesUids, specialityUid, " +
			"facility/provider uids) are the PRE-PROD tenant's own — discover them via get_settings " +
			"(orders/specialties tags); never reuse prod uids. The result composes with " +
			"build_queue_item -> add_queue_item for dev-clone robot tests. If the order doesn't reach " +
			"forReview, the result's processMessage explains why (e.g. a missing reference) — check " +
			"get_settings/diff_settings for the implicated section. Returns " +
			"{profile, newUid, verify, processMessage}.",
		// non-destructive: creates a new order; non-idempotent: each call mints a new order.
		Annotations: writeHints(false, false),
	}, s.createPreprodOrder,
		MinLen("profile", 1),
		MinLen("patientName", 1),
		MinLen("patientBirthDate", 6),
		MinLen("insuranceName", 1),
		MinLen("typeUid", 8),
		MinItems("orderNamesUids", 1), ItemsOf("orderNamesUids", func(s *jsonschema.Schema) { s.MinLength = new(8) }),
		MinLen("specialityUid", 8),
		MinLen("referredFacilityUid", 8),
		MinLen("referredProviderUid", 8),
		OnProp("icdCodes", func(s *jsonschema.Schema) { s.Items.Properties["code"].MinLength = new(1) }),
		Default("retro", false),
	)

	addTool(s, &mcp.Tool{
		Name:  "submit_preprod_order",
		Title: "Submit a pre-prod order sitting at forReview",
		Description: "Submit a PRE-PROD order that is sitting at forReview, advancing it to inProgress. " +
			"PRE-PROD ONLY. Requires EXPLICIT user authorization before calling — this is a real, " +
			"deliberate write, separate from create_preprod_order (which never submits). Returns " +
			"{profile, orderUid, submitted:true, verify}.",
		// non-destructive: advances the order's own state; non-idempotent: a second call fails
		// against an already-submitted order.
		Annotations: writeHints(false, false),
	}, s.submitPreprodOrder,
		MinLen("profile", 1),
		MinLen("orderUid", 8),
	)
}
