package wtc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
)

func tunnelOperationID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "op-" + hex.EncodeToString(b), nil
}

func (c *Context) managedTunnelLifecycle(p *tunnelProviderRuntime) tunnelLifecycle {
	return tunnelLifecycle{Owner: c.tunnelOwner(), Store: c.tunnelRuntimeStore(), Effects: p.effects()}
}

// Tunnel reads and changes the same named desired policy and operation journal
// used by status and retirement. Provider shorthand selects existing policy;
// it never creates an ambient-auth provider command in a task fragment.
func (c *Context) Tunnel(opt TunnelOptions) (result RuntimeResult, err error) {
	defer c.signalServicesChanged()
	result = RuntimeResult{Root: c.runtimeRoot(), Action: "tunnel", Items: []RuntimeItem{}}
	defer func() { result.Items = c.recordedTunnelItems(c.runtimeDeclarations()) }()
	if _, err = c.ValidateControlRoot(); err != nil {
		return result, err
	}
	action := opt.Action
	if action == "" {
		action = "start"
	}
	if opt.Stop {
		action = "stop"
	}
	switch action {
	case "register", "start", "stop", "unregister", "reconcile", "configure", "disable":
	default:
		return result, fmt.Errorf("unknown tunnel action %q", action)
	}
	if opt.Project && action != "configure" && action != "disable" {
		return result, fmt.Errorf("project scope is available only for explicit desired-policy writes")
	}
	if opt.Declared {
		if opt.Name != "" || opt.ExpectedDefinition == "" || !validTunnelDigest(opt.ExpectedDefinition) || action != "start" && action != "stop" {
			return result, fmt.Errorf("declared tunnel action requires its exact observed definition")
		}
		opt.Stop = action == "stop"
		return c.legacyTunnel(opt)
	}
	if opt.Name == "" && opt.ExpectedDefinition == "" && (action == "start" || action == "stop") {
		if repo, service, e := c.runtimeServiceRepo(opt.Service, true); e == nil {
			declarations := c.runtimeDeclarations()
			native := &runtimeManifest{}
			for _, item := range declarations {
				native.Tasks = append(native.Tasks, runtimeTask{RuntimeItem: item})
			}
			entries, e := c.effectiveTunnels(native)
			if e != nil {
				return result, e
			}
			named := false
			for _, entry := range entries {
				if entry.Key.Repo == repo.repo && entry.Key.Service == service {
					named = true
				}
			}
			if !named {
				// Pure discovery intentionally does not interpret provider command
				// metadata. An explicit legacy action resolves its native plan.
				legacy, _, err := c.runtimePlanWithTunnels("dekit", false)
				if err != nil {
					return result, err
				}
				for _, item := range legacy.Tasks {
					if item.Kind == "endpoint" && !item.ManagedTunnel && item.Group == repo.dir+"/"+service && (opt.Provider == "" || item.Provider == opt.Provider) {
						opt.Stop = action == "stop"
						return c.legacyTunnel(opt)
					}
				}
			}
		}
	}
	if action == "configure" || action == "disable" {
		repo, service, e := c.runtimeServiceRepo(opt.Service, true)
		if e != nil {
			return result, e
		}
		key := tunnelKey{repo.repo, service, opt.Name}
		if key.Name == "" {
			return result, fmt.Errorf("name the desired tunnel with --name")
		}
		entry := tunnelPolicyEntry{}
		if action == "disable" {
			off := false
			entry.Enabled = &off
		} else {
			if opt.DefinitionFile == "" {
				return result, fmt.Errorf("configure requires an explicit --definition policy file")
			}
			policy, e := readTunnelPolicyFile(opt.DefinitionFile)
			if e != nil {
				return result, e
			}
			var ok bool
			entry, ok = policy.Entries[key]
			if !ok || len(policy.Entries) != 1 || len(policy.Starts) != 0 {
				return result, fmt.Errorf("definition file must contain only the selected repo/service/name entry")
			}
		}
		// Preserve cleanup inputs and finish old obligations before replacement.
		s, e := c.readTunnelJournal()
		if e != nil {
			return result, e
		}
		if old := lifecycleEntry(&s, key); old != nil && old.needsCleanup() {
			changesEffective := true
			if old.Operation != nil && old.Operation.Lifecycle != nil {
				previous := old.Operation.Lifecycle.Definition.Entry
				// A project default does not replace an existing collection
				// override. Re-saving the same effective policy is also inert.
				if opt.Project && previous.Scope == "collection" {
					changesEffective = false
				} else if (previous.Scope == "project") == opt.Project {
					before, after := previous.Policy, entry
					before.Enabled, after.Enabled = nil, nil
					changesEffective = entry.disabled() || !reflect.DeepEqual(before, after)
				}
			}
			if changesEffective {
				if e = c.executeRetainedTunnel(context.Background(), *old, "unregister", c.tunnelProviders()); e != nil {
					return result, e
				}
			}
		}
		return result, c.SetTunnelPolicy(key.Repo, key.Service, key.Name, entry, opt.Project)
	}
	p := c.tunnelProviders()
	return result, c.runManagedTunnel(context.Background(), opt, action, p)
}

func (c *Context) runManagedTunnel(ctx context.Context, opt TunnelOptions, action string, p *tunnelProviderRuntime) error {
	state, err := c.readTunnelJournal()
	if err != nil {
		return err
	}
	if state.Retiring && (action == "start" || action == "register") {
		return fmt.Errorf("collection tunnel retirement is pending")
	}
	cleanup := action == "stop" || action == "unregister" || action == "reconcile"
	if cleanup {
		matches := []tunnelEntryState{}
		for _, old := range state.Entries {
			if opt.Name != "" && old.Key.Name != opt.Name || opt.Provider != "" && old.Provider != opt.Provider {
				continue
			}
			service := old.Key.Service
			repo := old.Key.Repo
			if opt.Service != service && opt.Service != repo+"/"+service {
				if old.Operation == nil || old.Operation.Lifecycle == nil || opt.Service != old.Operation.Lifecycle.Definition.Directory+"/"+service {
					continue
				}
			}
			matches = append(matches, old)
		}
		if len(matches) != 1 {
			return fmt.Errorf("select one retained named tunnel with --name (matched %d)", len(matches))
		}
		old := matches[0]
		if opt.ExpectedDefinition != "" && opt.ExpectedDefinition != old.Definition {
			return fmt.Errorf("tunnel definition changed; refresh recorded state")
		}
		if action == "stop" {
			if err = c.setTunnelStart(old.Key, false); err != nil {
				return err
			}
		}
		if action == "reconcile" {
			if old.Operation == nil {
				return errTunnelReconcile
			}
			if lifecycleComplete(old.Operation) && old.Active != nil {
				live, observeErr := p.activeSessionLive(ctx, old)
				if observeErr != nil {
					return observeErr
				}
				if !live {
					return c.executeRetainedTunnel(ctx, old, "stop", p)
				}
			}
			return c.managedTunnelLifecycle(p).Reconcile(ctx, old.Key, old.Operation.ID)
		}
		return c.executeRetainedTunnel(ctx, old, action, p)
	}
	plan, _, err := c.runtimePlanWithTunnels("dekit", false)
	if err != nil {
		return err
	}
	entries, err := c.effectiveTunnels(plan)
	if err != nil {
		return err
	}
	repo, service, err := c.runtimeServiceRepo(opt.Service, true)
	if err != nil {
		return err
	}
	matches := []tunnelEffectiveEntry{}
	for _, entry := range entries {
		if entry.Key.Repo == repo.repo && entry.Key.Service == service && (opt.Name == "" || entry.Key.Name == opt.Name) && (opt.Provider == "" || entry.Policy.Provider == opt.Provider) && !entry.Disabled && entry.Matched {
			matches = append(matches, entry)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("select one configured named tunnel with --name (matched %d); configure receiving-local tunnels.toml first", len(matches))
	}
	entry := matches[0]
	d, err := c.tunnelDefinitionFor(entry, plan)
	if err != nil {
		return err
	}
	digest, err := d.digest()
	if err != nil {
		return err
	}
	if opt.ExpectedDefinition != "" && opt.ExpectedDefinition != digest {
		return fmt.Errorf("tunnel definition changed; refresh and approve the current definition")
	}
	if old := lifecycleEntry(&state, entry.Key); old != nil && old.Definition != digest && old.needsCleanup() {
		if err = c.executeRetainedTunnel(ctx, *old, "unregister", p); err != nil {
			return err
		}
	}
	evidence := tunnelAccessEvidence{}
	if !opt.Public {
		evidence, err = p.access(ctx, d)
		if err != nil {
			return err
		}
	}
	consent, err := newTunnelConsent(d, opt.Public, evidence)
	if old := lifecycleEntry(&state, entry.Key); err != nil && old != nil && old.Consent != nil && old.Consent.permits(d, evidence) {
		consent = *old.Consent
		err = nil
	}
	if err != nil {
		return err
	}
	if action == "start" {
		if old := lifecycleEntry(&state, entry.Key); old != nil && old.Active != nil {
			live, observeErr := p.activeSessionLive(ctx, *old)
			if observeErr != nil {
				return observeErr
			}
			if !live {
				if err = c.executeRetainedTunnel(ctx, *old, "stop", p); err != nil {
					return err
				}
			}
		}
	}
	if action == "start" {
		// Start the native application before allocating/hosting an endpoint.
		// The generated host has no runner dependency that could later restart
		// it behind the persisted lifecycle and consent checks.
		if _, err = c.runtimeAction("up", d.Directory+"/"+entry.Key.Service, opt.Wait, nil); err != nil {
			return err
		}
		if err = c.setTunnelStart(entry.Key, true); err != nil {
			return err
		}
		entry.Selected = nil
		d.Entry = entry
	}
	id, err := tunnelOperationID()
	if err != nil {
		return err
	}
	return c.managedTunnelLifecycle(p).Execute(ctx, tunnelLifecycleRequest{ID: id, Action: action, Definition: d, Consent: consent, Evidence: evidence})
}

func (c *Context) executeRetainedTunnel(ctx context.Context, old tunnelEntryState, action string, p *tunnelProviderRuntime) error {
	if !old.needsCleanup() {
		return nil
	}
	if old.Operation == nil || old.Operation.Lifecycle == nil {
		return fmt.Errorf("retained tunnel lacks original cleanup definition; reconcile before proceeding")
	}
	id, err := tunnelOperationID()
	if err != nil {
		return err
	}
	return c.managedTunnelLifecycle(p).Execute(ctx, tunnelLifecycleRequest{ID: id, Action: action, Definition: old.Operation.Lifecycle.Definition})
}

// Retirement closes allocation admission in the same journal before arbitrary
// receiver hooks run. Every stopped registration is included, even without a
// runtime manifest. Uncertain effects retain the fence, code and credentials.
func (c *Context) cleanupManagedTunnels(ctx context.Context) error {
	s, err := c.readTunnelJournal()
	if err != nil {
		return err
	}
	if len(s.Entries) == 0 {
		return nil
	}
	if !s.Retiring || s.Retirement == nil {
		return fmt.Errorf("owned retirement admission required before cleanup")
	}
	p := c.tunnelProviders()
	for _, old := range s.Entries {
		if err = c.executeRetainedTunnel(ctx, old, "unregister", p); err != nil {
			return fmt.Errorf("tunnel %s cleanup refused: %w", old.Key.String(), err)
		}
	}
	return c.refuseTunnelObligations()
}

func tunnelItemService(item RuntimeItem) string { return strings.TrimPrefix(item.Group, item.Dir+"/") }
