// Package doctor probes the MCP server's connections to its external APIs and reports what is
// reachable. Read-only — it authenticates against the Copilot BE (prod + pre-prod),
// fingerprints the clinic scope that session actually has, and makes one cheap authenticated
// UiPath Orchestrator call per env, and — when configured — Apex: which config source supplied
// the account, that login works, and that each env's folder resolves to an id. Intended for
// setup/onboarding debugging ("are my creds + clinic + token + folders right?").
package doctor

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/apex"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/uipath"
)

// Check is one probe's outcome.
type Check struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"` // success note, or the failure reason
}

// Report is the doctor tool's result.
type Report struct {
	Account string  `json:"account"`
	OK      bool    `json:"ok"` // true only if every check passed
	Checks  []Check `json:"checks"`
}

// Probe is one check, deferred so Run can count them for progress before running any.
type Probe func(ctx context.Context) Check

// probe runs fn, capturing success detail or the error message — never fails.
func probe(name, target string, fn func(ctx context.Context) (string, error)) Probe {
	return func(ctx context.Context) Check {
		detail, err := fn(ctx)
		if err != nil {
			return Check{Name: name, Target: target, Detail: err.Error()}
		}
		return Check{Name: name, Target: target, OK: true, Detail: detail}
	}
}

// ApexProbes: Apex is optional, so the report treats "not configured" as fine: nothing is set at
// all = one passing check saying so (UiPath/Copilot results are unaffected). A half-finished
// setup (some but not all of apiUrl/username/password) is a failing check, since that is a
// mistake, not a choice. Otherwise: config source, login, and one folder-resolution check per env.
func ApexProbes() ([]Probe, error) {
	cfg, err := config.ResolveApex()
	if nc, ok := errors.AsType[*config.ApexNotConfiguredError](err); ok {
		check := Check{Name: "apex config", Target: "apex", Detail: nc.Error()}
		if !nc.Partial {
			check.OK = true
			check.Detail = "not configured (optional) — set the apex section of the config file, or APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD, to enable Apex"
		}
		return []Probe{func(context.Context) Check { return check }}, nil
	}
	if err != nil {
		return nil, err
	}
	src := cfg.Sources
	probes := []Probe{
		probe("apex config", cfg.APIURL, func(context.Context) (string, error) {
			return fmt.Sprintf("apiUrl from %s, username from %s, password from %s", src.APIURL, src.Username, src.Password), nil
		}),
		probe("apex login", cfg.APIURL, func(ctx context.Context) (string, error) {
			return "login OK", apex.Login(ctx)
		}),
	}
	for _, env := range config.Envs {
		probes = append(probes, probe(fmt.Sprintf("apex %s folder", env), cfg.APIURL, func(ctx context.Context) (string, error) {
			folder, err := apex.ResolveFolderID(ctx, env, "")
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("folder '%s' -> %s", folder.Name, folder.ID), nil
		}))
	}
	return probes, nil
}

// Run: per env, an auth probe, a clinic-scope fingerprint, and an Orchestrator round-trip —
// plus the Apex probes. All run concurrently and each is independent; one failure never aborts
// the others, and config resolution happens inside the probes so one misconfigured env fails one
// check instead of the whole report. Checks come back in a fixed order.
func Run(ctx context.Context, profile string, onProgress shared.StepProgress) (Report, error) {
	account := profile
	if account == "" {
		account = "(default)"
	}
	uipathCfg, err := config.Uipath()
	if err != nil {
		return Report{}, err
	}
	// Probe target = the env's BE base when the config resolves; the env name otherwise, so a
	// misconfigured env still surfaces as one failed check rather than an error here.
	beOf := func(env config.Env) string {
		auth, err := config.ResolveAuth(profile, env)
		if err != nil {
			return string(env)
		}
		return auth.BE
	}

	apexProbes, err := ApexProbes()
	if err != nil {
		return Report{}, err
	}
	var probes []Probe
	for _, env := range config.Envs {
		probes = append(probes, probe(fmt.Sprintf("copilot %s auth", env), beOf(env), func(ctx context.Context) (string, error) {
			s, err := copilot.Connect(ctx, env, profile)
			if err != nil {
				return "", err
			}
			if s.Mode == config.ModeSupport {
				return fmt.Sprintf("support-account login OK (switched to activeClinicId=%s)", clinicID(s)), nil
			}
			return "direct login OK", nil
		}))
	}
	// The documented PHI-free clinic fingerprint (/orders/locations differs per clinic) — the
	// only cheap proof the session acts as the intended clinic. Connect is cached, so this costs
	// no extra login.
	for _, env := range config.Envs {
		probes = append(probes, probe(fmt.Sprintf("copilot %s clinic scope", env), beOf(env), func(ctx context.Context) (string, error) {
			return clinicScope(ctx, env, profile)
		}))
	}
	authMode := "bearer"
	if uipathCfg.OAuth != nil {
		authMode = "oauth"
	}
	for _, env := range config.Envs {
		probes = append(probes, probe(fmt.Sprintf("uipath %s folder", env), uipathCfg.OrchestratorURL, func(ctx context.Context) (string, error) {
			folder, err := uipath.ResolveFolder(env, "")
			if err != nil {
				return "", err
			}
			jobs, err := uipath.ListRecentJobs(ctx, "", 1, folder, "")
			if err != nil {
				return "", err
			}
			shown := folder
			if shown == "" {
				shown = "(default)"
			}
			plural := "s"
			if len(jobs) == 1 {
				plural = ""
			}
			return fmt.Sprintf("reachable via %s (folder '%s', %d recent job%s visible)", authMode, shown, len(jobs), plural), nil
		}))
	}
	probes = append(probes, apexProbes...)

	checks := make([]Check, len(probes))
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex // serializes the progress callback
		settled int
	)
	for i, run := range probes {
		wg.Go(func() {
			c := run(ctx)
			checks[i] = c
			mu.Lock()
			defer mu.Unlock()
			settled++
			status := "failed"
			if c.OK {
				status = "ok"
			}
			onProgress.Step(settled, len(probes), c.Name+": "+status)
		})
	}
	wg.Wait()

	ok := true
	for _, c := range checks {
		ok = ok && c.OK
	}
	return Report{Account: account, OK: ok, Checks: checks}, nil
}

func clinicID(s copilot.Session) string {
	if s.ActiveClinicID == nil {
		return "undefined"
	}
	return fmt.Sprint(*s.ActiveClinicID)
}

func clinicScope(ctx context.Context, env config.Env, profile string) (string, error) {
	s, err := copilot.Connect(ctx, env, profile)
	if err != nil {
		return "", err
	}
	r, err := s.Client.Req(ctx, "GET", "/api/v1/orders/locations", copilot.ReqBody{})
	if err != nil {
		return "", err
	}
	if r.Status >= 400 {
		return "", fmt.Errorf("GET /orders/locations failed %d: %s", r.Status, shared.SliceUTF16(r.Text, 200))
	}
	locations, _ := shared.Prop(r.Data, "locations").([]any)
	n := len(locations)
	support := s.Mode == config.ModeSupport
	scope, empty := "", ""
	if support {
		scope = fmt.Sprintf(" (activeClinicId=%s)", clinicID(s))
	}
	if n == 0 && support {
		empty = " — 0 locations; a clinic-less token also returns 0, but activeClinicId is set, so this clinic really has none"
	}
	return fmt.Sprintf("%d locations visible%s%s", n, scope, empty), nil
}
