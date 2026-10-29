package copilot

// EHR Copilot pre-prod order mint engine. Ported natively from the legacy
// `.planning/copy_order_prod_to_preprod.mjs` (logic unchanged; config + types only).
//
// Create an empty pre-prod draft, replay all PUTs, /note/upload (dummy PDF), ICDCodes, /process
// retry loop until forReview, post-forReview corrections (requiredAuthorization then
// placeOfService LAST). Never submits — see SubmitOrder in order.go for that.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

var (
	e6001Re      = regexp.MustCompile(`(?i)(drafted|incomplete|pending)`)
	forReviewRe  = regexp.MustCompile(`(?i)forreview`)
	incompleteRe = regexp.MustCompile(`(?i)incomplete`)
)

// processTries / processRetryDelay: the final /process loop waits for the async note OCR. The
// delay is a var so tests don't really wait.
var (
	processTries      = 6
	processRetryDelay = 5 * time.Second
)

func normPhone(s string) string {
	var d []byte
	for i := range len(s) {
		if s[i] >= '0' && s[i] <= '9' {
			d = append(d, s[i])
		}
	}
	switch {
	case len(d) == 10:
		return fmt.Sprintf("(%s) %s-%s", d[:3], d[3:6], d[6:])
	case len(d) == 11 && d[0] == '1':
		return fmt.Sprintf("(%s) %s-%s", d[1:4], d[4:7], d[7:])
	}
	return "(555) 555-5555"
}

// buildDummyPDF is a minimal valid single-page PDF with correct xref offsets (dummy auth note).
func buildDummyPDF() []byte {
	stream := "BT /F1 18 Tf 20 60 Td (Dummy auth note - preprod clone) Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 300 144] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	var pdf strings.Builder
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefStart := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefStart)
	return []byte(pdf.String())
}

// MintProgress is client-visible progress, one call per mint milestone — in addition to (not
// instead of) the local slog diagnostics. May be nil.
type MintProgress func(message string)

// report logs a milestone locally and to the client.
func (p MintProgress) report(msg string) {
	slog.Debug("mint: " + msg)
	if p != nil {
		p(msg)
	}
}

type putFn func(body map[string]any, label string) error

func mkPut(ctx context.Context, c Client, uid string, onProgress MintProgress) putFn {
	return func(body map[string]any, label string) error {
		r, err := ReqWithRefresh(ctx, c, "PUT", "/api/v1/orders/"+uid, ReqBody{JSON: body})
		if err != nil {
			return err
		}
		if r.Status >= 400 {
			return fmt.Errorf("PUT %s failed %d: %s", label, r.Status, shared.SliceUTF16(r.Text, 300))
		}
		onProgress.report("PUT " + label + ": ok")
		return nil
	}
}

// procResult is one /process response: its msg (when it carries a string one) and the raw body.
type procResult struct {
	msg    string
	hasMsg bool
	raw    any
}

func postProcess(ctx context.Context, c Client, uid string) (procResult, error) {
	r, err := c.Req(ctx, "POST", "/api/v1/orders/"+uid+"/process", ReqBody{})
	if err != nil {
		return procResult{}, err
	}
	// already past forReview -> 400 E6001 "status must be in drafted/new/incomplete/pending" = success
	if r.Status == 400 && strings.Contains(r.Text, "E6001") && e6001Re.MatchString(r.Text) {
		return procResult{msg: "Order already at forReview status", hasMsg: true}, nil
	}
	if r.Status >= 400 {
		return procResult{}, fmt.Errorf("/process failed %d: %s", r.Status, shared.SliceUTF16(r.Text, 300))
	}
	raw := r.Data
	if raw == nil {
		raw = map[string]any{}
	}
	msg, hasMsg := shared.StringProp(raw, "msg")
	return procResult{msg: msg, hasMsg: hasMsg, raw: raw}, nil
}

func processUntilForReview(ctx context.Context, c Client, uid string, onProgress MintProgress) (procResult, error) {
	var body procResult
	for i := range processTries {
		var err error
		if body, err = postProcess(ctx, c, uid); err != nil {
			return body, err
		}
		shown := body.msg
		if !body.hasMsg {
			b, _ := shared.MarshalJSON(body.raw)
			shown = shared.SliceUTF16(string(b), 120)
		}
		onProgress.report(fmt.Sprintf("/process [%d]: %s", i, shown))
		if forReviewRe.MatchString(body.msg) {
			return body, nil
		}
		if i < processTries-1 {
			if err := shared.Sleep(ctx, processRetryDelay); err != nil {
				return body, err
			}
		}
	}
	return body, nil
}

// MintCPT is one CPT code replayed onto the mint.
type MintCPT struct {
	Code        string `json:"code"`
	Units       any    `json:"units,omitempty"`
	Description string `json:"description"`
	Treatments  any    `json:"treatments,omitempty"`
}

// MintICD is one ICD code replayed onto the mint.
type MintICD struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// MintSpec is everything the pre-prod mint engine needs, as plain data. TOTAL shape ("" / nil
// sentinels) so create_preprod_order's explicit payload normalizes at the boundary.
type MintSpec struct {
	PatientName         string
	PatientBirthDate    string // MM/DD/YYYY, "" = unset
	PatientPhoneNumber  string // raw; normalized at PUT time ("" -> dummy)
	InsuranceName       string
	InsuranceMemberID   string
	Location            string  // "" = skip
	TypeUID             string  // order type uid ("" = omitted — /process will stall)
	SpecialityUID       *string // nil = null
	ReferredFacilityUID string  // "" = skip
	// ReferredProviderUID "" = skip (referral orders; PUT after facility — must come after
	// orderNames, which reset it).
	ReferredProviderUID string
	ClinicProviderUID   string    // "" = leave to FE default
	OrderNamesUIDs      []string  // auto-seed CPTs on the BE
	CPTCodes            []MintCPT // empty = rely on orderNames auto-seed
	UploadAuth          bool
	UploadFax           bool
	Retro               bool
	Authorization       map[string]any // nil = none
	AppointmentDate     string         // MM/DD/YYYY, "" = none
	ICDCodes            []MintICD
	PlaceOfService      string // "" = none; applied LAST, post-forReview
}

// MintResult is the outcome of MintPreprodOrder.
type MintResult struct {
	NewUID string       `json:"newUid"`
	Verify *OrderVerify `json:"verify"`
	// ProcessMessage is the last /process response's message — set even when the order didn't
	// reach forReview, so a caller can see WHY (e.g. a missing reference) instead of just an
	// unexplained non-forReview status.
	ProcessMessage *string `json:"processMessage,omitempty"`
}

// opt sets key only for a non-empty value ("" sentinels -> omitted keys, so the wire bodies match
// the legacy mirror exactly).
func opt(m map[string]any, key, v string) map[string]any {
	if v != "" {
		m[key] = v
	}
	return m
}

// nullable is v, or null for "".
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// MintPreprodOrder creates a pre-prod draft and drives it to forReview from a MintSpec,
// preserving the sequence invariants: order names auto-seed CPTs and RESET speciality/
// referredProvider (names after type, speciality/provider after names); placeOfService is applied
// LAST, post-forReview; /process runs an E6001-tolerant retry loop (async note OCR blocks it).
// pre must already be logged in to the pre-prod tenant. Always stops at forReview — never submits;
// see SubmitOrder for that as a separate, explicit step.
func MintPreprodOrder(ctx context.Context, pre Client, spec MintSpec, onProgress MintProgress) (MintResult, error) {
	if err := AssertPreProdClient(pre, "mintPreprodOrder"); err != nil {
		return MintResult{}, err
	}
	// Step 1: empty draft
	draftRes, err := pre.Req(ctx, "POST", "/api/v1/orders", ReqBody{})
	if err != nil {
		return MintResult{}, err
	}
	if draftRes.Status >= 400 {
		return MintResult{}, fmt.Errorf("create draft failed %d: %s", draftRes.Status, shared.SliceUTF16(draftRes.Text, 300))
	}
	newUID, _ := shared.StringProp(shared.Prop(draftRes.Data, "order"), "orderUid")
	if newUID == "" {
		return MintResult{}, fmt.Errorf("create draft returned no orderUid: %s", shared.SliceUTF16(draftRes.Text, 200))
	}
	onProgress.report("new draft: " + newUID)
	proc, err := mintSteps(ctx, pre, spec, newUID, onProgress)
	if err != nil {
		return MintResult{}, err
	}
	v := Verify(ctx, pre, newUID)
	if b, err := json.MarshalIndent(v, "  ", "  "); err == nil {
		slog.Debug("mint verify", "verify", string(b))
	}
	onProgress.report("verify: done")
	res := MintResult{NewUID: newUID, Verify: v}
	if proc.hasMsg {
		res.ProcessMessage = &proc.msg
	}
	return res, nil
}
