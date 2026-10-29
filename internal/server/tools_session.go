package server

import (
	"context"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerSessionTools) }

// ---- get_login_token -------------------------------------------------------

type getLoginTokenArgs struct {
	Env     config.Env `json:"env" jsonschema:"Which env to log into (required)"`
	Profile string     `json:"profile" jsonschema:"Credential profile / account name from config (required)"`
}

func getLoginToken(ctx context.Context, req *mcp.CallToolRequest, in getLoginTokenArgs) (any, error) {
	s, err := withHeartbeat(ctx, req, string(in.Env)+" connect", heartbeatEvery, func() (copilot.Session, error) {
		return copilot.Connect(ctx, in.Env, in.Profile)
	})
	if err != nil {
		return nil, err
	}
	return struct {
		Env            config.Env `json:"env"`
		Profile        string     `json:"profile"`
		Mode           string     `json:"mode"`
		ClinicUID      *string    `json:"clinicUid"`
		ActiveClinicID *int64     `json:"activeClinicId"`
		Token          string     `json:"token"`
	}{in.Env, in.Profile, string(s.Mode), shared.NonEmpty(s.ClinicUID), s.ActiveClinicID, s.Token}, nil
}

// ---- list_clinics ----------------------------------------------------------

type listClinicsArgs struct {
	Env config.Env `json:"env" jsonschema:"Which env to list clinics for (required)"`
}

type clinicRow struct {
	copilot.Clinic
	Profile *string `json:"profile"`
}

// The documented carve-out from the "every Copilot operation requires env + profile" invariant:
// this is the discovery tool that PRODUCES the profile -> clinicUid mapping, so requiring a
// profile would be circular.
func listClinics(ctx context.Context, req *mcp.CallToolRequest, in listClinicsArgs) (any, error) {
	clinics, err := withHeartbeat(ctx, req, string(in.Env)+" list clinics", heartbeatEvery, func() ([]copilot.Clinic, error) {
		return copilot.ListClinics(ctx, in.Env)
	})
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	profileByUID := map[string]string{}
	for name, envs := range cfg.Copilot.Profiles {
		if uid := envs[in.Env].ClinicUID; uid != "" {
			profileByUID[uid] = name
		}
	}
	rows := make([]clinicRow, len(clinics))
	for i, c := range clinics {
		rows[i] = clinicRow{Clinic: c}
		if name, ok := profileByUID[c.ClinicUID]; ok {
			rows[i].Profile = &name
		}
	}
	// ponytail: lower-cased byte order approximates JS localeCompare; ICU collation of accents is not reproduced.
	slices.SortStableFunc(rows, func(a, b clinicRow) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return struct {
		Env     config.Env  `json:"env"`
		Clinics []clinicRow `json:"clinics"`
	}{in.Env, rows}, nil
}

func registerSessionTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:  "get_login_token",
		Title: "Get a Copilot BE login token",
		Description: "Return the Copilot BE session JWT for a profile + env. In support-account envs " +
			"that is the post-switch-clinic, clinic-scoped token with the activeClinicId it is " +
			"pinned to; otherwise it is the direct login token. A cached session is reused rather " +
			"than re-logging in. READ-ONLY — authenticates only, never reads or writes order data. " +
			"Returns {env, profile, mode, clinicUid, activeClinicId, token}.",
		Annotations: readOnlyHints(true),
	}, getLoginToken,
		Enum("env", config.Envs...),
		MinLen("profile", 1),
	)

	addTool(s, &mcp.Tool{
		Name:  "list_clinics",
		Title: "List the clinics the support account can switch into",
		Description: "List the clinics the support account can switch into, with the config profile each one " +
			"is already mapped to (profile:null = unmapped). Use it to fill " +
			"copilot.profiles.<name>.<env>.clinicUid. Requires copilot.support.<env> (support-account " +
			"auth — today prod only); takes no profile, because it is what produces the profile -> " +
			"clinicUid mapping. READ-ONLY. Returns {env, clinics}.",
		Annotations: readOnlyHints(true),
	}, listClinics,
		Enum("env", config.Envs...),
	)
}
