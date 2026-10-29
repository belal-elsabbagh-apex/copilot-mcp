package server

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/doctor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerDoctorTool) }

type doctorArgs struct {
	Profile string `json:"profile" jsonschema:"Credential profile / account name from config to check (required)"`
}

func runDoctor(ctx context.Context, req *mcp.CallToolRequest, in doctorArgs) (any, error) {
	return doctor.Run(ctx, in.Profile, stepProgress(ctx, req))
}

func registerDoctorTool(s *Server) {
	addTool(s, &mcp.Tool{
		Name:  "doctor",
		Title: "Check the server's external API connections",
		Description: "Probe the MCP server's connections to its external APIs and report what is reachable: " +
			"for PROD and PRE-PROD, an auth check (Copilot BE login, reusing a cached session), a " +
			"clinic-scope fingerprint (GET /orders/locations, the cheap proof a support-account " +
			"session landed on the intended clinic and not just that login succeeded), and one cheap " +
			"authenticated UiPath Orchestrator call per env/folder, and — when Apex is configured — " +
			"which config source supplied the Apex account, that Apex login works, and that each env's " +
			"folder resolves to an id (an unconfigured Apex is reported as optional, not a failure). " +
			"READ-ONLY. Use it to debug setup (creds, clinic scope, UiPath token, Apex login, folder " +
			"access). Returns {account, ok, " +
			"checks:[{name, target, ok, detail}]}.",
		Annotations: readOnlyHints(true),
	}, runDoctor, MinLen("profile", 1))
}
