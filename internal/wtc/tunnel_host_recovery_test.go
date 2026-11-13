package wtc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// This helper exists only in the test binary. Its provider program is a local
// inert shell fixture; neither this test nor its subprocess makes network calls.
func TestTunnelHostCrashFakeParent(t *testing.T) {
	if os.Getenv("TUNNEL_TEST_FAKE_PARENT") != "1" {
		return
	}
	dir := os.Getenv("TUNNEL_TEST_DIRECTORY")
	parent, err := currentTunnelProcess()
	if err != nil {
		t.Fatal(err)
	}
	r := tunnelHostReady{Schema: 1, Parent: parent, State: "starting", ChildPhase: "not-launched"}
	write := func() error {
		data, _ := json.Marshal(r)
		return runtimeWrite(filepath.Join(dir, "observed.json"), data)
	}
	d := testTunnelDefinition()
	d.Entry.Policy = tunnelPolicyEntry{Provider: "cloudflare", Mode: "quick", Access: "public", Inputs: tunnelInputRefs{Executable: filepath.Join(dir, "fake-provider")}}
	_, err = (&tunnelProviderRuntime{}).runHost(context.Background(), tunnelEffectCall{Definition: d, Entry: tunnelEntryState{Provider: "cloudflare", Mode: "quick"}}, dir, func(string) error { r.State = "active"; return write() }, func(phase string, child tunnelProcessIdentity) error {
		r.ChildPhase = phase
		if phase == "owned" {
			r.Children = []tunnelProcessIdentity{child}
		}
		return write()
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestTunnelHostCrashRetainsLiveExternalChildAndStopsExactInstance(t *testing.T) {
	dir := t.TempDir()
	fixtureFile(t, filepath.Join(dir, "fake-provider"), "#!/bin/sh\ntrap 'exit 0' TERM\nprintf 'https://synthetic.trycloudflare.com\\n'\nwhile :; do /bin/sleep 0.05; done\n", 0755)
	cmd := exec.Command(os.Args[0], "-test.run=^TestTunnelHostCrashFakeParent$")
	cmd.Env = append(minimalTunnelEnvironment(dir), "TUNNEL_TEST_FAKE_PARENT=1", "TUNNEL_TEST_DIRECTORY="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var r tunnelHostReady
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		for _, child := range r.Children {
			_ = stopTunnelOwnedChild(context.Background(), child)
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(dir, "observed.json"))
		_ = json.Unmarshal(data, &r)
		if r.State == "active" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake host did not initialize", string(data))
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !r.live() || len(r.Children) != 1 {
		t.Fatal("real fake-host initialization lacks exact live child evidence", r)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if got := tunnelProcessState(r.Parent, readTunnelProcess); got != "absent" {
		t.Fatal("killed owned parent remains", got)
	}
	if got := tunnelProcessState(r.Children[0], readTunnelProcess); got != "alive" {
		t.Fatal("fake provider child did not survive parent-kill boundary", got)
	}
	if r.live() || r.disposed() == nil {
		t.Fatal("parent absence was mistaken for external child disposal")
	}
	if err := r.stopProcesses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.disposed(); err != nil {
		t.Fatal(err)
	}
}
func TestTunnelOwnedProcessNeverSignalsReplacementOrUncertainExecution(t *testing.T) {
	original := tunnelProcessIdentity{PID: 12345, Start: "original", Boot: "boot", Execution: tunnelDigest([]byte("owned"))}
	for _, mode := range []string{"reused", "exec-changed", "unknown", "vanished-on-second-read"} {
		t.Run(mode, func(t *testing.T) {
			reads, signals := 0, 0
			read := func(int) (tunnelProcessIdentity, string) {
				reads++
				current := original
				switch mode {
				case "reused":
					current.Start = "replacement"
				case "exec-changed":
					current.Execution = tunnelDigest([]byte("foreign"))
				case "unknown":
					return current, "unknown"
				case "vanished-on-second-read":
					if reads > 1 {
						return current, "absent"
					}
				}
				return current, "alive"
			}
			err := stopTunnelChildWith(context.Background(), original, read, func() error { signals++; return nil })
			if signals != 0 {
				t.Fatal("signalled an unproven owned instance")
			}
			if (mode == "exec-changed" || mode == "unknown") && err == nil {
				t.Fatal("uncertain process accepted")
			}
		})
	}
}
func TestTunnelHostAmbiguousLaunchRetainsReceipt(t *testing.T) {
	parent, err := currentTunnelProcess()
	if err != nil {
		t.Fatal(err)
	}
	r := tunnelHostReady{Parent: parent, ChildPhase: "launching"}
	if r.disposed() == nil || r.stopProcesses(context.Background()) == nil {
		t.Fatal("ambiguous child startup accepted as cleanup")
	}
}

func TestTunnelProductionCrashedSessionStatusStopAndReconcile(t *testing.T) {
	for _, action := range []string{"stop", "reconcile", "runner-loss", "callback-receipt", "missing-observation"} {
		t.Run(action, func(t *testing.T) {
			c := runtimeFixture(t)
			fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
			executable := filepath.Join(t.TempDir(), "inert-provider")
			fixtureFile(t, executable, "#!/bin/sh\nexit 93\n", 0755)
			entry := tunnelPolicyEntry{Provider: "cloudflare", Mode: "quick", Access: "public", Inputs: tunnelInputRefs{Executable: executable}}
			if err := c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
				t.Fatal(err)
			}
			if _, err := c.renderRuntime(); err != nil {
				t.Fatal(err)
			}
			plan, _, err := c.runtimePlanWithTunnels(c.Config.Runtime.Binary, false)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := c.effectiveTunnels(plan)
			if err != nil {
				t.Fatal(err)
			}
			d, err := c.tunnelDefinitionFor(entries[0], plan)
			if err != nil {
				t.Fatal(err)
			}
			consent, err := newTunnelConsent(d, true, tunnelAccessEvidence{})
			if err != nil {
				t.Fatal(err)
			}
			p := &tunnelProviderRuntime{c: c}
			l := c.managedTunnelLifecycle(p)
			original := l.Effects.Provider
			var hostReceipt tunnelResourceRef
			var observation tunnelHostReady
			l.Effects.Provider = func(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
				if call.Stage != "start" {
					return original(ctx, call)
				}
				_, task, err := p.hostTask(call)
				if err != nil {
					return tunnelEffectResult{}, err
				}
				hostReceipt = p.hostReceipt(call, task)
				parent, err := currentTunnelProcess()
				if err != nil {
					return tunnelEffectResult{}, err
				}
				parent.Start = "synthetic-ended-host"
				address, _ := parseTunnelAddress("https://synthetic.trycloudflare.com")
				childCmd := exec.Command("/bin/sleep", "30")
				childCmd.Env = minimalTunnelEnvironment(c.Collection)
				if err = childCmd.Start(); err != nil {
					return tunnelEffectResult{}, err
				}
				child, childState := readTunnelProcess(childCmd.Process.Pid)
				go childCmd.Wait()
				t.Cleanup(func() { _ = childCmd.Process.Kill() })
				if childState != "alive" {
					return tunnelEffectResult{}, fmt.Errorf("fake child identity unavailable")
				}
				observation = tunnelHostReady{Schema: 1, Owner: call.Owner, OperationID: call.OperationID, Definition: call.Entry.Definition, State: "active", Address: address, Parent: parent, ChildPhase: "owned", Children: []tunnelProcessIdentity{child}}
				return tunnelEffectResult{Address: &address, Add: []tunnelResourceRef{hostReceipt}}, nil
			}
			if err = l.Execute(context.Background(), tunnelLifecycleRequest{ID: "synthetic-start", Action: "start", Definition: d, Consent: consent}); err != nil {
				t.Fatal(err)
			}
			callback := tunnelResourceRef{Provider: "callback", Kind: "synthetic-hook", Scope: c.tunnelOwner(), ID: "retained-effect", Ownership: "owned", Owner: c.tunnelOwner(), Fingerprint: tunnelDigest([]byte("callback")), Lifetime: "session"}
			if action == "callback-receipt" || action == "missing-observation" {
				if err = c.tunnelRuntimeStore().Update(context.Background(), func(s *tunnelState) error {
					s.Entries[0].Resources = append(s.Entries[0].Resources, callback)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(c.tunnelHostDir(entries[0].Key), "ready.json")
			if action != "missing-observation" {
				data, _ := json.Marshal(observation)
				fixtureFile(t, path, string(data), 0600)
			}
			fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[{"path":"`+hostReceipt.ID+`","state":"exited"}]}`, 0600)
			if action != "runner-loss" {
				fixtureFile(t, filepath.Join(c.runtimeRoot(), "running"), "", 0600)
			}
			before, _ := os.ReadFile(c.tunnelJournalPath())
			found := false
			for _, item := range c.recordedTunnelItems(c.runtimeDeclarations()) {
				if item.ManagedTunnel {
					found = true
					if item.State != "pending" || item.URL != "" {
						t.Fatal("crashed host advertised live", item)
					}
				}
			}
			after, _ := os.ReadFile(c.tunnelJournalPath())
			if !found || string(before) != string(after) {
				t.Fatal("pure recovery status missing or mutated journal")
			}
			requested := action
			if action == "missing-observation" || action == "callback-receipt" || action == "runner-loss" {
				requested = "stop"
			}
			err = c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview"}, requested, p)
			if err != nil && action != "missing-observation" {
				t.Logf("local observation after refusal: parent=%s child=%s disposed=%v", tunnelProcessState(observation.Parent, readTunnelProcess), tunnelProcessState(observation.Children[0], readTunnelProcess), observation.disposed())
			}
			s, readErr := c.readTunnelJournal()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if action == "missing-observation" {
				if err == nil || s.Entries[0].Active == nil || len(s.Entries[0].Resources) != 2 {
					t.Fatal("missing observation discarded ambiguous receipts", err)
				}
				return
			}
			e := s.Entries[0]
			if action == "callback-receipt" {
				if err == nil || e.Active != nil || len(e.Resources) != 1 || e.Resources[0] != callback {
					t.Fatal("ambiguous callback was discarded or reported complete", err, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.Active != nil || len(e.Resources) != 0 || !lifecycleComplete(e.Operation) {
				t.Fatal("confirmed local cleanup lost callback or kept stale session", e)
			}
		})
	}
}

func TestTunnelOwnedProcessWaitsForPositiveExitAfterTransientUnknown(t *testing.T) {
	p := tunnelProcessIdentity{PID: 12345, Start: "owned", Boot: "boot", Execution: tunnelDigest([]byte("owned"))}
	signals, afterSignal := 0, 0
	err := stopTunnelChildWith(context.Background(), p, func(int) (tunnelProcessIdentity, string) {
		if signals == 0 {
			return p, "alive"
		}
		afterSignal++
		if afterSignal == 1 {
			return p, "unknown"
		}
		return p, "absent"
	}, func() error { signals++; return nil })
	if err != nil || signals != 1 || afterSignal != 2 {
		t.Fatal("transient exit uncertainty changed signal/disposal contract", err, signals, afterSignal)
	}
}
