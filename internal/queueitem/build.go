// Package queueitem is the canonical Copilot-order -> queue-item content builder, shared by
// every orchestrator backend. Fetches the order and logs in to capture the BE callback JWT +
// physicianId, then maps the order onto SpecificContent in NATIVE types and WITHOUT
// serverURL/queueUrl — each backend's serializer fills those from its own config. BUILD ONLY —
// never POSTs.
package queueitem

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/config"
	"github.com/belal-elsabbagh-apex/copilot-mcp/internal/copilot"
)

// Opts are Build's inputs. Env is required — never defaulted (repo invariant).
type Opts struct {
	Profile      string // "" = none
	Env          config.Env
	AutomationID string // "" = resolve from Profile
}

// Build reads the order and assembles its queue-item content.
func Build(ctx context.Context, orderUID string, opts Opts) (BuiltQueueItem, error) {
	session, err := copilot.Connect(ctx, opts.Env, opts.Profile)
	if err != nil {
		return BuiltQueueItem{}, err
	}
	support := session.Mode == config.ModeSupport
	providerMail := session.Email

	// The owner-email lookup and the order read are independent once connected.
	var order copilot.BeOrder
	g, gctx := errgroup.WithContext(ctx)
	if support {
		g.Go(func() (err error) {
			providerMail, err = copilot.ClinicOwnerEmail(gctx, opts.Env, session.ClinicUID)
			return err
		})
	}
	g.Go(func() (err error) {
		order, err = copilot.FetchOrder(gctx, session.Client, orderUID)
		return err
	})
	if err := g.Wait(); err != nil {
		return BuiltQueueItem{}, err
	}

	built := Assemble(AssembleInput{
		Order: order, OrderUID: orderUID, Profile: opts.Profile, Env: opts.Env,
		AutomationID: ResolveAutomationID(opts.AutomationID, opts.Profile),
		Token:        session.Token, PhysicianID: DecodeJwtID(session.Token),
		SupportMode: support, ProviderMail: providerMail,
	})
	return BuiltQueueItem{QueueItemContent: built, BeBase: session.Client.Base()}, nil
}
