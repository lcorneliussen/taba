package wtc

import (
	"context"
	"fmt"
	"time"
)

// Service actions coordinate sessions through the same named-entry controller;
// they never clear selection merely because an application is stopped.
func (c *Context) managedRuntimeAction(action, target string, wait time.Duration) (RuntimeResult, error) {
	result := RuntimeResult{Root: c.runtimeRoot(), Action: action, Items: []RuntimeItem{}}
	if action != "up" && action != "down" && action != "restart" {
		return c.runtimeAction(action, target, wait, nil)
	}
	items := c.recordedTunnelItems(c.runtimeDeclarations())
	managed := false
	for _, item := range items {
		managed = managed || item.ManagedTunnel
	}
	if !managed {
		return c.runtimeAction(action, target, wait, nil)
	}
	view := &runtimeManifest{}
	for _, item := range items {
		view.Tasks = append(view.Tasks, runtimeTask{RuntimeItem: item})
	}
	_, selected, err := runtimeSelect(view, target)
	if err != nil {
		return result, err
	}
	if len(selected) == 1 && selected[0].ManagedTunnel {
		item := selected[0].RuntimeItem
		tunnelAction := "start"
		if action == "down" {
			tunnelAction = "stop"
		}
		if action == "restart" {
			if _, err = c.Tunnel(TunnelOptions{Service: item.Group, Name: item.TunnelName, Action: "stop"}); err != nil {
				return result, err
			}
		}
		return c.Tunnel(TunnelOptions{Service: item.Group, Name: item.TunnelName, Action: tunnelAction, ExpectedDefinition: item.DefinitionID})
	}
	groups := map[string]bool{}
	for _, task := range selected {
		if !task.ManagedTunnel {
			groups[task.Group] = true
		}
	}
	state, err := c.readTunnelJournal()
	if err != nil {
		return result, err
	}
	p := c.tunnelProviders()
	if action == "down" || action == "restart" {
		for _, old := range state.Entries {
			if old.Operation == nil || old.Operation.Lifecycle == nil {
				if old.needsCleanup() {
					return result, fmt.Errorf("retained tunnel has no cleanup definition")
				}
				continue
			}
			d := old.Operation.Lifecycle.Definition
			if !groups[d.Directory+"/"+old.Key.Service] {
				continue
			}
			if old.Active != nil || lifecycleSessionResources(old) {
				if err = c.executeRetainedTunnel(context.Background(), old, "stop", p); err != nil {
					return result, err
				}
			}
		}
	}
	result, err = c.runtimeAction(action, target, wait, nil)
	if err != nil || action == "down" {
		return result, err
	}
	// Native status is only a local application observation. No provider auth
	// or endpoint probe is used to select a tunnel.
	running := map[string]bool{}
	for _, item := range result.Items {
		if item.Kind != "endpoint" && (item.State == "ready" || item.State == "running") {
			running[item.Group] = true
		}
	}
	plan, _, err := c.runtimePlanWithTunnels("dekit", false)
	if err != nil {
		return result, err
	}
	entries, err := c.effectiveTunnels(plan)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.Disabled || !entry.Matched {
			continue
		}
		wanted := entry.Policy.StartWithService
		if entry.Selected != nil {
			wanted = *entry.Selected
		}
		if !wanted {
			continue
		}
		d, e := c.tunnelDefinitionFor(entry, plan)
		if e != nil {
			return result, e
		}
		group := d.Directory + "/" + entry.Key.Service
		if !groups[group] || !running[group] {
			continue
		}
		// Starts cannot manufacture approval. Existing public approval must still
		// match the full current definition; private evidence is observed at use.
		if err = c.runManagedTunnel(context.Background(), TunnelOptions{Service: group, Name: entry.Key.Name}, "start", p); err != nil {
			return result, err
		}
	}
	result.Items = c.RuntimeStatus()
	return result, nil
}
