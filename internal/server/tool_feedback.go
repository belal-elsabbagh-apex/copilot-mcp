package server

import (
	"context"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { addToolSet(registerFeedbackTools) }

type buildMcpIssueArgs struct {
	Kind    IssueKind `json:"kind" jsonschema:"bug = something is wrong; feedback = idea / improvement / general comment"`
	Title   string    `json:"title" jsonschema:"Short issue summary"`
	Details string    `json:"details" jsonschema:"The report itself: what happened or what is wanted, expected vs actual, steps if relevant. No credentials or PHI."`
	Tool    string    `json:"tool,omitempty" jsonschema:"Related copilot-mcp tool name, if the report concerns one"`
}

func registerFeedbackTools(s *Server) {
	addTool(s, &mcp.Tool{
		Name:        "build_mcp_issue",
		Title:       "Build a GitHub issue for feedback on this MCP server",
		Annotations: readOnlyHints(false), // pure string building — touches no external service
		Description: "Compose a GitHub issue about THIS MCP server from a user's report — a bug OR general " +
			"feedback (ideas, friction, docs gaps); no tool failure required. BUILD ONLY — posts " +
			"nothing and holds no GitHub credentials: create the issue via the host's GitHub tooling " +
			"(GitHub MCP server / gh) from the returned title/body/labels, or give the user `url` (a " +
			"prefilled new-issue link; filing needs a GitHub account with access to the target repo). " +
			"Targets feedback.repositoryUrl (default: the copilot-mcp repo); works even with no config " +
			"loaded. Never put credentials or PHI in the details. Returns {repo, title, body, labels, url}.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in buildMcpIssueArgs) (any, error) {
		// No config loaded is fine — feedback must still work, against the default repo.
		var repositoryURL string
		if cfg, err := config.Feedback(); err == nil {
			repositoryURL = cfg.RepositoryURL
		}
		return formatMcpIssue(in.Kind, in.Title, in.Details, in.Tool, s.Version, repositoryURL), nil
	},
		Enum("kind", KindBug, KindFeedback),
		MinLen("title", 8),
		MinLen("details", 20),
	)
}
