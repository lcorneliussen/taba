package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// This derived observation binds local process evidence to the persisted start
// receipt. Missing records and the start/PID-recording gap remain uncertain.
func (r tunnelHostReady) validateProcesses() error {
	if r.Parent.validate() != nil {
		return fmt.Errorf("owned host process evidence missing")
	}
	switch r.ChildPhase {
	case "none", "not-launched", "launching":
		if len(r.Children) != 0 {
			return fmt.Errorf("invalid host child observation")
		}
	case "owned":
		if len(r.Children) != 1 || r.Children[0].validate() != nil || r.Children[0].PID == r.Parent.PID {
			return fmt.Errorf("invalid owned child instance")
		}
	default:
		return fmt.Errorf("unknown host child launch state")
	}
	return nil
}
func (r tunnelHostReady) live() bool {
	if r.validateProcesses() != nil || r.State != "active" || tunnelProcessState(r.Parent, readTunnelProcess) != "alive" {
		return false
	}
	if r.ChildPhase == "none" {
		return true
	}
	return r.ChildPhase == "owned" && tunnelProcessState(r.Children[0], readTunnelProcess) == "alive"
}
func (r tunnelHostReady) disposed() error {
	if err := r.validateProcesses(); err != nil {
		return err
	}
	if r.ChildPhase == "launching" {
		return fmt.Errorf("child launch outcome unknown; retain session")
	}
	if tunnelProcessState(r.Parent, readTunnelProcess) != "absent" {
		return fmt.Errorf("owned host process disposal pending")
	}
	for _, child := range r.Children {
		if tunnelProcessState(child, readTunnelProcess) != "absent" {
			return fmt.Errorf("owned provider child disposal pending")
		}
	}
	return nil
}
func (r tunnelHostReady) stopProcesses(ctx context.Context) error {
	if err := r.validateProcesses(); err != nil {
		return err
	}
	if r.ChildPhase == "launching" {
		return fmt.Errorf("child launch outcome unknown; retain session")
	}
	// A live parent may dispose the exact child normally through cancellation.
	// A crashed parent cannot do so; its separately recorded child still matters.
	if err := stopTunnelOwnedChild(ctx, r.Parent); err != nil {
		return err
	}
	for _, child := range r.Children {
		if err := stopTunnelOwnedChild(ctx, child); err != nil {
			return err
		}
	}
	return r.disposed()
}
func (p *tunnelProviderRuntime) ownedHostObservation(call tunnelEffectCall, receipt tunnelResourceRef) (tunnelHostReady, error) {
	var r tunnelHostReady
	_, task, err := p.hostTask(call)
	if err != nil {
		return r, err
	}
	if receipt.Kind != "host-task" || receipt.Instance == "" || receipt.Ownership != "owned" || receipt.Owner != call.Owner || receipt.Scope != call.Owner || receipt.ID != task.Path || receipt.Fingerprint != task.Spec {
		return r, fmt.Errorf("exact owned host start receipt required")
	}
	path := filepath.Join(p.c.tunnelHostDir(call.Entry.Key), "ready.json")
	if err = runtimeSafeParents(p.c.runtimeRoot(), path); err != nil {
		return r, err
	}
	data, err := readTunnelBoundedFile(path, 8192)
	if err != nil {
		return r, fmt.Errorf("host process observation missing; retain session receipt")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if !tunnelUniqueJSONKeys(json.NewDecoder(bytes.NewReader(data))) || d.Decode(&r) != nil || r.Schema != 1 || r.Owner != call.Owner || r.Definition != call.Entry.Definition || r.OperationID != receipt.Instance {
		return r, fmt.Errorf("host process observation differs from retained start")
	}
	if err = r.validateProcesses(); err != nil {
		return r, err
	}
	if call.Entry.Provider == "ngrok" && r.ChildPhase != "none" || call.Entry.Provider != "ngrok" && r.ChildPhase == "none" {
		return r, fmt.Errorf("provider child boundary is unproven")
	}
	return r, nil
}
func (p *tunnelProviderRuntime) localHostLive(e tunnelEntryState) bool {
	if e.Operation == nil || e.Operation.Lifecycle == nil {
		return false
	}
	call := tunnelEffectCall{Owner: p.c.tunnelOwner(), Entry: e, Definition: e.Operation.Lifecycle.Definition}
	for _, receipt := range e.Resources {
		if receipt.Kind == "host-task" {
			r, err := p.ownedHostObservation(call, receipt)
			return err == nil && r.live()
		}
	}
	return false
}
