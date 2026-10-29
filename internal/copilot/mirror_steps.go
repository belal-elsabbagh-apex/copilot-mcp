package copilot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
)

func todayMDY() string { return time.Now().Format("01/02/2006") }

// mintSteps replays every PUT / process / upload step onto the fresh draft and returns the last
// /process result. The step numbering is the legacy mirror's; the ORDER is the invariant.
func mintSteps(ctx context.Context, pre Client, spec MintSpec, uid string, onProgress MintProgress) (procResult, error) {
	put := mkPut(ctx, pre, uid, onProgress)

	// Step 2: patient
	if err := put(opt(map[string]any{"emrPatientId": "", "patientBirthDate": nullable(spec.PatientBirthDate)}, "patientName", spec.PatientName), "patient"); err != nil {
		return procResult{}, err
	}
	// Step 3: phone (always non-empty)
	if err := put(map[string]any{"patientPhoneNumber": normPhone(spec.PatientPhoneNumber)}, "phone"); err != nil {
		return procResult{}, err
	}
	// Step 4: insurance
	ins := opt(opt(map[string]any{}, "name", spec.InsuranceName), "memberId", spec.InsuranceMemberID)
	if err := put(map[string]any{"insurance": ins}, "insurance"); err != nil {
		return procResult{}, err
	}
	// Step 5: location
	if spec.Location != "" {
		if err := put(map[string]any{"location": spec.Location}, "location"); err != nil {
			return procResult{}, err
		}
	}
	// Step 6: order type + speciality (orderDate = today)
	if err := put(opt(map[string]any{
		"specialityUid": spec.SpecialityUID, "referredProviderUid": nil, "referredFacilityUid": nil, "orderDate": todayMDY(),
	}, "typeUid", spec.TypeUID), "type"); err != nil {
		return procResult{}, err
	}
	// Step 7: order names (auto-seeds CPTs; RESETS speciality/provider — hence re-sent here)
	names := spec.OrderNamesUIDs
	if names == nil {
		names = []string{}
	}
	if err := put(map[string]any{
		"orderNamesUids": names, "specialityUid": spec.SpecialityUID, "referredProviderUid": nil, "referredFacilityUid": nil,
	}, "orderNames"); err != nil {
		return procResult{}, err
	}
	// Step 8: sent by — only when a valid pre-prod uid is known (prod's may not exist)
	if spec.ClinicProviderUID != "" {
		if err := put(map[string]any{"clinicProviderUid": spec.ClinicProviderUID}, "sentBy"); err != nil {
			return procResult{}, err
		}
	} else {
		onProgress.report("(sentBy: left to FE default — clinicProvider not mapped)")
	}
	// Step 9: facility (auto-sets POS + fax)
	if spec.ReferredFacilityUID != "" {
		if err := put(map[string]any{"referredFacilityUid": spec.ReferredFacilityUID}, "facility"); err != nil {
			return procResult{}, err
		}
	}
	// Step 9a: referred provider (referral orders) — after names/speciality, which reset it
	if spec.ReferredProviderUID != "" {
		if err := put(map[string]any{"referredProviderUid": spec.ReferredProviderUID}, "referredProvider"); err != nil {
			return procResult{}, err
		}
	}
	// Step 9b: best-effort CPT injection — "Other" orderName won't auto-seed PT CPTs
	if len(spec.CPTCodes) > 0 {
		cpts := make([]map[string]any, len(spec.CPTCodes))
		for i, c := range spec.CPTCodes {
			cpts[i] = map[string]any{"code": c.Code, "description": c.Description, "evidences": []any{}}
			if c.Units != nil {
				cpts[i]["units"] = c.Units
			}
			if c.Treatments != nil {
				cpts[i]["treatments"] = c.Treatments
			}
		}
		if err := put(map[string]any{"CPTCodes": cpts}, "CPTCodes (injected)"); err != nil {
			if ctx.Err() != nil {
				return procResult{}, err
			}
			slog.Warn("CPT injection failed (continuing)", "err", err)
		}
	}
	// Step 10: yes/no flags
	for _, f := range []struct {
		key string
		val bool
	}{{"uploadAuth", spec.UploadAuth}, {"uploadFax", spec.UploadFax}, {"retro", spec.Retro}} {
		if err := put(map[string]any{f.key: f.val}, f.key); err != nil {
			return procResult{}, err
		}
	}
	if spec.Authorization != nil {
		if err := put(map[string]any{"authorization": spec.Authorization}, "authorization"); err != nil {
			return procResult{}, err
		}
	}
	// Step 11: direct processing
	if err := put(map[string]any{"directProcessingEnabled": true}, "directProcessingEnabled"); err != nil {
		return procResult{}, err
	}
	// Step 12: first /process (usually incomplete: appointmentDate missing)
	proc, err := postProcess(ctx, pre, uid)
	if err != nil {
		return procResult{}, err
	}
	onProgress.report("/process (1st): " + proc.msg)
	// Step 14: appointment date
	if spec.AppointmentDate != "" {
		if err := put(map[string]any{"appointmentDate": spec.AppointmentDate}, "appointmentDate"); err != nil {
			return procResult{}, err
		}
		if incompleteRe.MatchString(proc.msg) {
			if proc, err = postProcess(ctx, pre, uid); err != nil {
				return procResult{}, err
			}
			onProgress.report("/process (after appt): " + proc.msg)
		}
	}
	// Step 15: note upload (dummy PDF, async OCR on BE)
	up, err := ReqWithRefresh(ctx, pre, "POST", "/api/v1/orders/"+uid+"/note/upload", ReqBody{
		Form: &FilePart{Field: "pdfFile", Filename: "note.pdf", ContentType: "application/pdf", Data: buildDummyPDF()},
	})
	if err != nil {
		return procResult{}, err
	}
	if up.Status >= 400 {
		return procResult{}, fmt.Errorf("/note/upload failed %d: %s", up.Status, shared.SliceUTF16(up.Text, 300))
	}
	onProgress.report("/note/upload: ok")
	// Step 16: ICDs (manual; OCR can't read dummy PDF)
	if len(spec.ICDCodes) > 0 {
		icds := make([]map[string]any, len(spec.ICDCodes))
		for i, c := range spec.ICDCodes {
			icds[i] = map[string]any{"code": c.Code, "description": c.Description, "evidences": []any{}}
		}
		if err := put(map[string]any{"ICDCodes": icds}, "ICDCodes"); err != nil {
			return procResult{}, err
		}
	}
	// Final /process retry loop until forReview (note upload async-blocks it)
	if proc, err = processUntilForReview(ctx, pre, uid, onProgress); err != nil {
		return procResult{}, err
	}
	if !forReviewRe.MatchString(proc.msg) {
		slog.Warn("order did not reach forReview", "msg", proc.msg)
	}
	// Step 13: post-forReview corrections — requiredAuthorization FIRST, placeOfService LAST.
	if err := put(map[string]any{"requiredAuthorization": true}, "requiredAuthorization (final)"); err != nil {
		return procResult{}, err
	}
	if spec.PlaceOfService != "" {
		if err := put(map[string]any{"placeOfService": spec.PlaceOfService}, "placeOfService (final, must be last)"); err != nil {
			return procResult{}, err
		}
	}
	return proc, nil
}
