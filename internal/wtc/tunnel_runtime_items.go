package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
)

func tunnelTaskPath(dir string, key tunnelKey) string {
	return dir + "/" + key.Service + "/tunnel-" + key.Name
}

func (c *Context) planManagedTunnels(plan *runtimeManifest, native map[string]any) error {
	entries, err := c.effectiveTunnels(plan)
	if err != nil {
		return err
	}
	state, err := c.readTunnelJournal()
	if err != nil {
		return err
	}
	wanted := map[tunnelKey]tunnelEffectiveEntry{}
	for _, entry := range entries {
		wanted[entry.Key] = entry
	}
	for _, old := range state.Entries {
		if !old.needsCleanup() {
			continue
		}
		entry, exists := wanted[old.Key]
		if !exists || entry.Disabled || !entry.Matched {
			return fmt.Errorf("retained tunnel obligations require cleanup before rendering changed/deleted policy")
		}
		d, err := c.tunnelDefinitionFor(entry, plan)
		if err != nil {
			return err
		}
		digest, _ := d.digest()
		if digest != old.Definition {
			return fmt.Errorf("retained tunnel definition requires cleanup before rendering replacement")
		}
	}
	cli, err := os.Executable()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Disabled || !entry.Matched || entry.Policy.Provider == "" || entry.Policy.Provider == "tailscale" {
			continue
		}
		d, err := c.tunnelDefinitionFor(entry, plan)
		if err != nil {
			return err
		}
		digest, _ := d.digest()
		path := tunnelTaskPath(d.Directory, entry.Key)
		if _, exists := native[path]; exists {
			return fmt.Errorf("managed tunnel task conflicts with native declaration")
		}
		scope, err := c.ValidateControlRoot()
		if err != nil {
			return err
		}
		command := []string{"/usr/bin/env", "-i"}
		command = append(command, minimalTunnelEnvironment(c.tunnelHostDir(entry.Key))...)
		command = append(command, "TABA_DEV_BUILD=off", "WTC_DEV_BUILD=off")
		for _, alias := range rootAliases {
			command = append(command, alias+"="+scope.Root)
		}
		command = append(command, cli, "svc", "tunnel-host", "--repo", entry.Key.Repo, "--service", entry.Key.Service, "--name", entry.Key.Name)
		task := map[string]any{"cmd": command, "cwd": c.Collection, "autostart": false, "ready": map[string]any{"log": "TUNNEL_READY", "timeout": "60s"}}
		spec, _ := json.Marshal(task)
		item := runtimeTask{RuntimeItem: RuntimeItem{ManagedTunnel: true, TunnelName: entry.Key.Name, Collection: filepath.Base(c.Collection), Repo: entry.Key.Repo, Dir: d.Directory, Path: path, Group: d.Directory + "/" + entry.Key.Service, Kind: "endpoint", Label: entry.Key.Name, Provider: entry.Policy.Provider, Source: entry.Source, State: "unconfigured", DefinitionID: digest}, Log: filepath.Join(plan.Root, "logs", filepath.FromSlash(path)+".log"), Definition: digest, Spec: tunnelDigest(spec)}
		if old, err := c.runtimeManifest(); err != nil {
			return err
		} else if old != nil {
			for _, prior := range old.Tasks {
				if prior.Path != path || !prior.ManagedTunnel || prior.Spec == item.Spec {
					continue
				}
				for _, saved := range state.Entries {
					if saved.Key != entry.Key {
						continue
					}
					live := saved.Active != nil
					for _, r := range saved.Resources {
						live = live || r.Kind == "host-task"
					}
					if live {
						return fmt.Errorf("stop owned named host before replacing its supervisor build")
					}
				}
			}
		}
		native[path] = task
		plan.Tasks = append(plan.Tasks, item)
	}
	return nil
}

func (c *Context) recordedTunnelItems(observed []RuntimeItem) []RuntimeItem {
	// Managed host tasks are implementation details. Their displayed identity,
	// selection and lifecycle always come from this same policy/journal view.
	items := []RuntimeItem{}
	plan := &runtimeManifest{}
	for _, item := range observed {
		if !item.ManagedTunnel {
			items = append(items, item)
			plan.Tasks = append(plan.Tasks, runtimeTask{RuntimeItem: item})
		}
	}
	state, stateErr := c.readTunnelJournal()
	entries, policyErr := c.effectiveTunnels(plan)
	// Native discovery already reports unsafe shared runtime parents per repo.
	// Keep those diagnostics without adding a duplicate global row. Distinct
	// journal corruption still receives its own visible diagnostic.
	unsafeAlreadyReported := c.runtimePathsSafe() != nil && slices.ContainsFunc(items, func(item RuntimeItem) bool { return item.Kind == "configuration" })
	if stateErr != nil && !unsafeAlreadyReported {
		items = append(items, c.runtimeDeclarationError("", stateErr))
	}
	if policyErr != nil {
		items = append(items, c.runtimeDeclarationError("", policyErr))
	}
	desired := map[tunnelKey]tunnelEffectiveEntry{}
	saved := map[tunnelKey]tunnelEntryState{}
	for _, e := range entries {
		desired[e.Key] = e
	}
	for _, e := range state.Entries {
		saved[e.Key] = e
	}
	keys := map[tunnelKey]bool{}
	for k := range desired {
		keys[k] = true
	}
	for k := range saved {
		keys[k] = true
	}
	baseItems := len(items)
	for key := range keys {
		e, wanted := desired[key]
		old, exists := saved[key]
		dir := key.Repo
		for _, task := range plan.Tasks {
			if task.Repo == key.Repo && task.Group == task.Dir+"/"+key.Service {
				dir = task.Dir
				break
			}
		}
		if !wanted && old.Operation != nil && old.Operation.Lifecycle != nil {
			dir = old.Operation.Lifecycle.Definition.Directory
		}
		item := RuntimeItem{ManagedTunnel: true, TunnelName: key.Name, Repo: key.Repo, Dir: dir, Collection: filepath.Base(c.Collection), Path: tunnelTaskPath(dir, key), Group: dir + "/" + key.Service, Kind: "endpoint", Label: key.Name, Source: e.Source, Provider: e.Policy.Provider, State: "idle", Exposure: ExposureUnknown, AnswerDetail: "recorded tunnel state; no provider/account probe"}
		item.TunnelMode = e.Policy.Mode
		if wanted && !e.Disabled && e.Matched {
			if definition, err := c.tunnelDefinitionFor(e, plan); err == nil {
				item.DesiredDefinitionID, _ = definition.digest()
			} else {
				item.Lifecycle = "resolve native service and desired definition before exposure approval"
			}
		}
		if e.Selected != nil {
			item.TunnelIntent = "off"
			if *e.Selected {
				item.TunnelIntent = "on"
			}
		}
		if exists {
			item.Registered = old.Registered
			item.DefinitionID = old.Definition
			if item.Provider == "" {
				item.Provider = old.Provider
			}
			if old.Known != nil {
				item.RegistrationURL = old.Known.URL
			}
			if old.Registered {
				item.State = "registered"
			}
			if old.Active != nil {
				item.State = "ready"
				item.URL = old.Active.URL
			}
			if old.Operation != nil {
				item.OperationID = old.Operation.ID
				if !lifecycleComplete(old.Operation) {
					item.Lifecycle = "needs reconciliation"
					item.State = "pending"
				}
				if wanted && old.Operation.Lifecycle != nil {
					original := old.Operation.Lifecycle.Definition.Entry
					original.Selected = nil
					original.Policy.Enabled = nil
					current := e
					current.Selected = nil
					current.Policy.Enabled = nil
					if !reflect.DeepEqual(original, current) {
						item.Lifecycle = "definition changed; clean retained registration before replacement"
					}
				}
			}
		}
		if !wanted || !e.Matched {
			item.Lifecycle = "unmatched/deleted definition; retained cleanup remains available"
		}
		if e.Disabled {
			item.Lifecycle = "disabled"
			if old.Active == nil {
				item.State = "disabled"
			}
		}
		items = append(items, item)
	}
	// Preserve all observed native rows and their ordering. Only the newly
	// appended named entries need deterministic ordering.
	named := items[baseItems:]
	sort.SliceStable(named, func(i, j int) bool { return named[i].Path < named[j].Path })
	return items
}
