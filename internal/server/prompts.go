package server

// MCP prompts: user-invoked workflow templates that chain the existing tools. Each returns a
// single user message that walks the operator (or the model) through the tool sequence for a
// common Copilot ops task. Prompts add no new capability — they encode the playbooks that
// otherwise live only in the skills/docs.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptArg is one prompt argument. complete, when set, backs completion/complete for it (read
// live so suggestions reflect the running config, never a hardcoded list).
type promptArg struct {
	name, desc string
	required   bool
	minLen     int
	complete   func() []string
}

type promptDef struct {
	name, title, desc string
	args              []promptArg
	text              func(args map[string]string) string
}

func envNames() []string { return []string{string(config.Prod), string(config.PreProd)} }

func orchestratorNames() []string {
	names := make([]string, len(orchestrator.Orchestrators))
	for i, o := range orchestrator.Orchestrators {
		names[i] = string(o)
	}
	return names
}

// profileArg is the required credential-profile argument shared by most prompts.
func profileArg() promptArg {
	return promptArg{
		name: "profile", desc: "Credential profile / account name from config (required)",
		required: true, minLen: 1, complete: config.ListProfiles,
	}
}

func promptDefs() []promptDef {
	return slices.Concat(orderPrompts(), settingsPrompts())
}

func (s *Server) registerPrompts() {
	for _, d := range promptDefs() {
		s.AddPrompt(d.prompt(), d.handler)
	}
}

func (d promptDef) prompt() *mcp.Prompt {
	args := make([]*mcp.PromptArgument, len(d.args))
	for i, a := range d.args {
		args[i] = &mcp.PromptArgument{Name: a.name, Description: a.desc, Required: a.required}
	}
	return &mcp.Prompt{Name: d.name, Title: d.title, Description: d.desc, Arguments: args}
}

// handler validates the arguments the way the TS zod schemas did (required, min length) and
// renders the prompt as one user text message.
func (d promptDef) handler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	args := req.Params.Arguments
	for _, a := range d.args {
		v, present := args[a.name]
		switch {
		case a.required && !present:
			return nil, invalidPromptArgs(d.name, a.name+" is required")
		case present && len([]rune(v)) < a.minLen:
			return nil, invalidPromptArgs(d.name, fmt.Sprintf("%s must be at least %d characters", a.name, a.minLen))
		}
	}
	return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{
		{Role: "user", Content: &mcp.TextContent{Text: d.text(args)}},
	}}, nil
}

func invalidPromptArgs(prompt, problem string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Invalid arguments for prompt " + prompt + ": " + problem}
}

// complete answers completion/complete: prompt arguments with a complete func, and the portal
// resource template's {name} variable. Like the TS completable() it returns the whole list, not a
// prefix filter, for prompt arguments; portal names are prefix-filtered as the TS template did.
func (s *Server) complete(_ context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	p := req.Params
	var values []string
	switch p.Ref.Type {
	case "ref/prompt":
		defs := promptDefs()
		if i := slices.IndexFunc(defs, func(d promptDef) bool { return d.name == p.Ref.Name }); i >= 0 {
			if j := slices.IndexFunc(defs[i].args, func(a promptArg) bool { return a.name == p.Argument.Name }); j >= 0 && defs[i].args[j].complete != nil {
				values = defs[i].args[j].complete()
			}
		}
	case "ref/resource":
		if p.Ref.URI == portalTemplate && p.Argument.Name == "name" {
			prefix := strings.ToUpper(strings.TrimSpace(p.Argument.Value))
			for _, portal := range portalKeys() {
				if strings.HasPrefix(portal, prefix) {
					values = append(values, portal)
				}
			}
		}
	}
	if values == nil {
		values = []string{}
	}
	return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: values, Total: len(values)}}, nil
}
