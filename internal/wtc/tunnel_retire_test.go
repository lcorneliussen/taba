package wtc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTunnelRetireStandalonePolicyOnlyAndUnknownCredentialRefusal(t *testing.T) {
	for _, extra := range []string{"", "host.token", "unknown.txt", "changed-policy"} {
		t.Run(extra, func(t *testing.T) {
			root := adHocFixture(t)
			c := initAdHoc(t, filepath.Join(root, "task"))
			entry := tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", Options: tunnelProviderOptions{ListenerPort: 8443}}
			if err := c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
				t.Fatal(err)
			}
			policy := filepath.Join(c.Collection, ".config", "tunnels.toml")
			if extra == "changed-policy" {
				f, err := os.OpenFile(policy, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				f.WriteString("# user edit\n")
				f.Close()
			} else if extra != "" {
				fixtureFile(t, filepath.Join(c.Collection, ".config", extra), "synthetic preserved data", 0600)
			}
			opt := RetireOptions{Name: "task", Self: true, InPlace: true, Force: true}
			_, err := c.RetirePreflight(opt)
			if extra != "" {
				if err == nil || !strings.Contains(err.Error(), "private control") {
					t.Fatal("unknown/credential policy data not preserved", err)
				}
				if _, err = c.RetireCollection(opt); err == nil {
					t.Fatal("force bypassed control preservation")
				}
				if _, err = os.Stat(policy); err != nil {
					t.Fatal("refusal removed policy", err)
				}
				return
			}
			if err != nil {
				t.Fatal("owned non-secret policy-only preflight refused", err)
			}
			result, err := c.RetireCollection(opt)
			if err != nil || !result.FolderRemoved {
				t.Fatal("policy-only standalone retirement failed", result, err)
			}
		})
	}
}

func TestTunnelRetirePendingOwnershipPrecedesDestructiveLegacyHook(t *testing.T) {
	c := newWorkspaceFixture(t)
	made, err := c.NewCollection(NewOptions{Slug: "pending-tunnel", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(made.Collection, "widget/init-ran")); err != nil {
		t.Fatal(err)
	}
	target, err := OpenCollection(made.Collection)
	if err != nil {
		t.Fatal(err)
	}
	s := testTunnelState()
	s.Owner = target.tunnelOwner()
	for i := range s.Entries[0].Resources {
		s.Entries[0].Resources[i].Owner = s.Owner
	}
	data, err := encodeTunnelState(s, s.Owner)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, target.tunnelJournalPath(), string(data), 0600)
	fixtureFile(t, filepath.Join(c.Harness, "hooks/wtc/retire.pre.sh"), "#!/bin/sh\ntouch \"$PWD/destructive-hook-ran\"\n", 0755)
	for _, force := range []bool{false, true} {
		if _, err = c.RetireCollection(RetireOptions{Name: "pending-tunnel", Force: force}); err == nil {
			t.Fatal("pending registration allowed retirement")
		}
		if _, err = os.Stat(filepath.Join(c.Collection, "destructive-hook-ran")); !os.IsNotExist(err) {
			t.Fatal("legacy pre-hook preceded tunnel validation")
		}
		if _, err = os.Stat(filepath.Join(made.Collection, "widget")); err != nil {
			t.Fatal("worktree deleted before cleanup", err)
		}
	}
}

func TestTunnelRetireCleansStoppedRegistrationBeforeLegacyPreHook(t *testing.T) {
	c := newWorkspaceFixture(t)
	made, err := c.NewCollection(NewOptions{Slug: "stopped-tunnel", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := OpenCollection(made.Collection)
	if err != nil {
		t.Fatal(err)
	}
	target.Config.Runtime.Backend = "dekit"
	target.Config.Runtime.Binary = filepath.Join(t.TempDir(), "dekit")
	fixtureFile(t, target.Config.Runtime.Binary, "#!/bin/sh\nif [ \"$1\" = --version ];then echo 'dekit 0.10.0';exit;fi\ncase \"$*\" in\n *'runner status'*) printf '{\"status\":\"absent\"}\\n';;\n *'runner stop'*) printf 'Runner is not running.\\n' >&2;exit 1;;\n *) exit 92;;\nesac\n", 0755)
	fixtureFile(t, filepath.Join(target.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
	marker := filepath.Join(t.TempDir(), "cleanup-before-legacy")
	fixtureFile(t, filepath.Join(target.Collection, "widget/cleanup-event.sh"), "#!/bin/sh\ntouch \"$1\"\n", 0755)
	entry := tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", Options: tunnelProviderOptions{ListenerPort: 8443, Path: "/preview"}, Inputs: tunnelInputRefs{Socket: "/synthetic/authorized.sock", NodeID: "synthetic-node", DNSName: "node.synthetic.ts.net"}, Events: map[string]tunnelEventCommand{"unregistered": {Command: []string{"cleanup-event.sh", marker}}}}
	if err = target.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
		t.Fatal(err)
	}
	daemon := tailscaleSyntheticDaemon(`{}`)
	scopeTarget := tailscaleSyntheticTarget()
	scopeTarget.Socket = entry.Inputs.Socket
	p := &tunnelProviderRuntime{c: target, Local: daemon.transport(t, scopeTarget)}
	if err = target.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview"}, "register", p); err != nil {
		t.Fatal(err)
	}
	state, err := target.readTunnelJournal()
	if err != nil || !state.Entries[0].Registered || state.Entries[0].Active != nil {
		t.Fatal("stopped reservation missing", err)
	}
	// Legacy pre-hook can destroy credentials/code only after the owned cleanup
	// callback has run. Actual RetireCollection reopens the receiving context.
	fixtureFile(t, filepath.Join(c.Harness, "hooks/wtc/retire.pre.sh"), "#!/bin/sh\n[ -f '"+marker+"' ] || exit 73\n", 0755)
	result, err := c.RetireCollection(RetireOptions{Name: "stopped-tunnel", Force: true})
	if err != nil || !result.FolderRemoved {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("cleanup hook did not precede destructive boundary", err)
	}
	if err = target.RetireRuntime(); err != nil {
		t.Errorf("repeated runtime cleanup after successful collection removal: %v", err)
	}
	if _, err = os.Lstat(target.Collection); !os.IsNotExist(err) {
		t.Errorf("runtime cleanup resurrected removed collection: %v", err)
	}
	// Unregistering the stopped local reservation has no daemon mutation or
	// account query; only the earlier explicit private registration used the fake.
	for _, call := range daemon.journal {
		if strings.HasPrefix(call, "POST") {
			t.Fatal("stopped reservation retirement mutated daemon")
		}
	}
}

func TestTunnelRetirementJournalBearingLeftoversPreserveUserFiles(t *testing.T) {
	for _, adHoc := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed", true: "ad-hoc"}[adHoc], func(t *testing.T) {
			var caller, target *Context
			if adHoc {
				root := adHocFixture(t)
				target = initAdHoc(t, filepath.Join(root, "journal-leftovers"))
				caller = target
			} else {
				caller = newWorkspaceFixture(t)
				made, err := caller.NewCollection(NewOptions{Slug: "journal-leftovers", Repos: []string{"widget"}})
				if err != nil {
					t.Fatal(err)
				}
				target, err = OpenCollection(made.Collection)
				if err != nil {
					t.Fatal(err)
				}
			}
			entry := tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", Options: tunnelProviderOptions{ListenerPort: 8443}}
			if err := target.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
				t.Fatal(err)
			}
			state, err := target.readTunnelJournal()
			if err != nil || state.Policy == nil {
				t.Fatal("policy journal prerequisite", err)
			}
			keep := filepath.Join(target.Collection, "keep.txt")
			fixtureFile(t, keep, "synthetic user-owned leftovers\n", 0600)
			opt := RetireOptions{Name: filepath.Base(target.Collection), Force: true, Self: adHoc, InPlace: adHoc}
			result, err := caller.RetireCollection(opt)
			if err != nil || result.FolderRemoved || len(result.Leftovers) != 1 || result.Leftovers[0] != "keep.txt" {
				t.Fatal("journal-bearing retirement with user leftovers", result, err)
			}
			if data, err := os.ReadFile(keep); err != nil || string(data) != "synthetic user-owned leftovers\n" {
				t.Fatal("user leftover changed", err)
			}
			for _, path := range []string{target.tunnelJournalPath(), target.runtimeRoot(), filepath.Join(target.Collection, ".wtc-runtime.lock"), filepath.Join(target.Collection, ".config/tunnels.toml")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Errorf("retirement recreated or retained generated path %q: %v", path, err)
				}
			}
			if RetiredFolder(target.Collection) == adHoc {
				t.Fatal("established managed/ad-hoc leftover guard behavior changed")
			}
			if !adHoc {
				if err := os.Remove(keep); err != nil {
					t.Fatal(err)
				}
				finished, err := caller.RetireCollection(RetireOptions{Name: opt.Name})
				if err != nil || !finished.FolderRemoved {
					t.Fatal("guarded leftover finish failed", finished, err)
				}
			}
		})
	}
}

func TestTunnelRetireRuntimeWithoutServicesDoesNotCreateJournalOrSignal(t *testing.T) {
	c := runtimeFixture(t)
	if err := c.RetireRuntime(); err != nil {
		t.Fatal(err)
	}
	if !c.ServicesChangedAt().IsZero() {
		t.Fatal("empty runtime retirement published services signal")
	}
	if _, err := os.Lstat(c.runtimeRoot()); !os.IsNotExist(err) {
		t.Fatal("empty runtime retirement created generated root", err)
	}
}
