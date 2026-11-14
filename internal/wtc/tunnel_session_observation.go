package wtc

import (
	"context"
	"fmt"
)

// Mutation-time observation is separate from pure recorded status. Unknown or
// foreign daemon state returns an error, never a false "absent" cleanup signal.
func (p *tunnelProviderRuntime) activeSessionLive(ctx context.Context, e tunnelEntryState) (bool, error) {
	if e.Provider != "tailscale" {
		return p.localHostLive(e), nil
	}
	if e.Active == nil || e.Operation == nil || e.Operation.Lifecycle == nil || !lifecycleComplete(e.Operation) {
		return false, errTunnelReconcile
	}
	live, _, err := p.tailscaleSessionLive(ctx, e)
	return live, err
}

// The same exact scoped read can confirm a lost stop reply, without replaying
// the mutation. It never adopts a matching handler for an uncertain start.
func (p *tunnelProviderRuntime) tailscaleSessionLive(ctx context.Context, e tunnelEntryState) (bool, tunnelResourceRef, error) {
	if e.Operation == nil || e.Operation.Lifecycle == nil {
		return false, tunnelResourceRef{}, errTunnelReconcile
	}
	var live bool
	var owned tunnelResourceRef
	err := p.c.withTunnelRuntimeLock(ctx, func() error {
		d := e.Operation.Lifecycle.Definition
		if err := p.authorizeScope(d); err != nil {
			return err
		}
		target, err := p.tailscaleTarget(d)
		if err != nil {
			return err
		}
		for _, receipt := range e.Resources {
			if receipt.Provider != "tailscale" || receipt.Kind != "serve" {
				continue
			}
			view, err := readOwnedTailscaleServe(ctx, p.Local, p.c.tunnelOwner(), receipt, target)
			if err != nil {
				return err
			}
			owned = receipt
			live = len(view.current) != 0
			return nil
		}
		return fmt.Errorf("Tailscale session lacks exact owned serve receipt")
	})
	return live, owned, err
}
