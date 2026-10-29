// Package backends maps each orchestrator value to the backend that serves it, and runs the
// cross-backend order search. Retiring a backend = delete its package, its entry here and its
// orchestrator.Orchestrators value.
package backends

import (
	"errors"
	"slices"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/apex"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/orchestrator"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/uipath"
)

// all is in orchestrator.Orchestrators order (the order tool schemas advertise them).
var all = []orchestrator.Backend{uipath.Backend, apex.Backend}

// BackendFor is the backend serving o; nil for a value outside orchestrator.Orchestrators.
func BackendFor(o orchestrator.Orchestrator) orchestrator.Backend {
	i := slices.IndexFunc(all, func(b orchestrator.Backend) bool { return b.Name() == o })
	if i < 0 {
		return nil
	}
	return all[i]
}

// apexAbsent: a wholly unconfigured Apex is skipped by "search everything" (UiPath-only setups
// stay quiet); a half-configured one is searched so its ApexNotConfiguredError surfaces.
func apexAbsent() bool {
	_, err := config.ResolveApex()
	nc, ok := errors.AsType[*config.ApexNotConfiguredError](err)
	return ok && !nc.Partial
}

// SearchableBackends is every backend "search everything" should ask.
func SearchableBackends() []orchestrator.Backend {
	return slices.DeleteFunc(slices.Clone(all), func(b orchestrator.Backend) bool {
		return b.Name() == orchestrator.Apex && apexAbsent()
	})
}
