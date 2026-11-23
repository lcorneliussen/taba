package wtc

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The machine-wide Tailscale reservation inventory reads every sibling's
// tunnel journal. It must not depend on the sibling's unrelated environment
// validation (#482), and it must keep refusing on conflicting, unreadable or
// unverifiable evidence. Everything here is synthetic: the LocalAPI is a fake
// transport scoped to a synthetic socket, and no provider executable runs.

func reservation482Receiver(t *testing.T) (*Context, *tunnelProviderRuntime, *tailscaleFakeDaemon) {
	t.Helper()
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
	target := tailscaleSyntheticTarget()
	entry := tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", Options: tunnelProviderOptions{ListenerPort: target.Port, Path: target.Path}, Inputs: tunnelInputRefs{Socket: target.Socket, NodeID: target.NodeID, DNSName: target.DNSName}}
	if err := c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.renderRuntime(); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[{"path":"widget/web/server","state":"ready"}]}`, 0600)
	daemon := tailscaleSyntheticDaemon(`{}`)
	return c, &tunnelProviderRuntime{c: c, Local: daemon.transport(t, target)}, daemon
}

// reservation482Sibling is a legacy sibling collection in the same workspace
// whose generated environment names a stale control root, so OpenCollection
// refuses it exactly like the reported case.
func reservation482Sibling(t *testing.T, receiver *Context, name string) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(receiver.Collection), name)
	fixtureFile(t, filepath.Join(dir, "harness/.harness-repos.yml"), "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n", 0644)
	fixtureFile(t, filepath.Join(dir, ".env.collection"), "TABA_CONFIG_ROOT=/synthetic/stale/control-root\n", 0644)
	if _, err := OpenCollection(dir); err == nil || !strings.Contains(err.Error(), "conflicts with receiving secrets policy") {
		t.Fatalf("synthetic sibling must fail environment validation, got %v", err)
	}
	return dir
}

// reservation482Journal writes a sibling journal owned by that sibling and
// holding one Tailscale reservation for path.
func reservation482Journal(t *testing.T, sibling, path string) {
	t.Helper()
	owner := (&Context{Collection: sibling}).tunnelOwner()
	target := tailscaleSyntheticTarget()
	target.Path = path
	receipt, err := target.resource(owner)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Kind, receipt.Lifetime = "reservation", "registration"
	state := tunnelState{Schema: 1, Owner: owner, Entries: []tunnelEntryState{{
		Key: tunnelKey{"widget", "web", "other"}, Provider: "tailscale", Mode: "serve",
		Definition: tunnelDigest([]byte("synthetic-sibling-definition")), Registered: true,
		Resources: []tunnelResourceRef{receipt},
	}}}
	data, err := encodeTunnelState(state, owner)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(sibling, ".wtc/runtime/tunnels.json"), string(data), 0600)
}

// reservation482Tree fingerprints every path, mode, size and mtime under dir,
// plus the content of owner-readable regular files, so a test can prove the
// inventory did not write, repair or create anything in the sibling.
func reservation482Tree(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %v %d %d\n", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() && info.Mode().Perm()&0400 != 0 {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func reservation482Register(c *Context, p *tunnelProviderRuntime, ctx context.Context) error {
	return c.runManagedTunnel(ctx, TunnelOptions{Service: "web", Name: "preview", Public: true}, "register", p)
}

func reservation482Reserved(t *testing.T, c *Context) bool {
	t.Helper()
	s, err := c.readTunnelJournal()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range s.Entries {
		for _, r := range e.Resources {
			if r.Provider == "tailscale" && r.Kind == "reservation" {
				return true
			}
		}
	}
	return false
}

func reservation482NoMutation(t *testing.T, d *tailscaleFakeDaemon) {
	t.Helper()
	for _, call := range d.journal {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatal("provider mutation before reservation admission:", call)
		}
	}
}

func TestTunnelReservation482AbsentSiblingJournalIgnoresUnrelatedEnvironment(t *testing.T) {
	c, p, d := reservation482Receiver(t)
	sibling := reservation482Sibling(t, c, "sibling")
	before := reservation482Tree(t, sibling)
	if err := reservation482Register(c, p, context.Background()); err != nil {
		t.Fatal("unrelated sibling environment blocked the reservation:", err)
	}
	if !reservation482Reserved(t, c) {
		t.Fatal("reservation receipt was not recorded")
	}
	if reservation482Tree(t, sibling) != before {
		t.Fatal("reservation inventory changed the sibling collection")
	}
	reservation482NoMutation(t, d)
}

func TestTunnelReservation482NonconflictingSiblingJournalIgnoresUnrelatedEnvironment(t *testing.T) {
	c, p, _ := reservation482Receiver(t)
	sibling := reservation482Sibling(t, c, "sibling")
	reservation482Journal(t, sibling, "/elsewhere")
	before := reservation482Tree(t, sibling)
	if err := reservation482Register(c, p, context.Background()); err != nil {
		t.Fatal("nonconflicting sibling reservation blocked the reservation:", err)
	}
	if !reservation482Reserved(t, c) || reservation482Tree(t, sibling) != before {
		t.Fatal("reservation not recorded or sibling changed")
	}
}

func TestTunnelReservation482ConflictingSiblingStillRefuses(t *testing.T) {
	for _, path := range []string{"/preview", "/preview/deeper", "/"} {
		t.Run(path, func(t *testing.T) {
			c, p, d := reservation482Receiver(t)
			sibling := reservation482Sibling(t, c, "sibling")
			reservation482Journal(t, sibling, path)
			err := reservation482Register(c, p, context.Background())
			if err == nil || !strings.Contains(err.Error(), "Tailscale listener/path is reserved by another named entry") {
				t.Fatal("conflicting sibling reservation was not refused:", err)
			}
			if reservation482Reserved(t, c) {
				t.Fatal("refused reservation was recorded")
			}
			reservation482NoMutation(t, d)
		})
	}
}

func TestTunnelReservation482UninspectableSiblingEvidenceRefuses(t *testing.T) {
	cases := map[string]func(t *testing.T, sibling string){
		"malformed": func(t *testing.T, sibling string) {
			fixtureFile(t, filepath.Join(sibling, ".wtc/runtime/tunnels.json"), "{not json", 0600)
		},
		"unknown field": func(t *testing.T, sibling string) {
			owner := (&Context{Collection: sibling}).tunnelOwner()
			fixtureFile(t, filepath.Join(sibling, ".wtc/runtime/tunnels.json"), `{"schema":1,"owner":"`+owner+`","entries":[],"extra":true}`, 0600)
		},
		"stale owner": func(t *testing.T, sibling string) {
			// A journal copied from another collection is not this sibling's evidence.
			reservation482Journal(t, sibling, "/elsewhere")
			data, _ := os.ReadFile(filepath.Join(sibling, ".wtc/runtime/tunnels.json"))
			other := filepath.Join(filepath.Dir(sibling), "moved-away")
			fixtureFile(t, filepath.Join(sibling, ".wtc/runtime/tunnels.json"), strings.ReplaceAll(string(data), (&Context{Collection: sibling}).tunnelOwner(), (&Context{Collection: other}).tunnelOwner()), 0600)
		},
		"not a regular file": func(t *testing.T, sibling string) {
			if err := os.MkdirAll(filepath.Join(sibling, ".wtc/runtime/tunnels.json"), 0700); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked runtime": func(t *testing.T, sibling string) {
			elsewhere := t.TempDir()
			if err := os.MkdirAll(filepath.Join(sibling, ".wtc"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, filepath.Join(sibling, ".wtc/runtime")); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable": func(t *testing.T, sibling string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads mode 000 files")
			}
			reservation482Journal(t, sibling, "/elsewhere")
			if err := os.Chmod(filepath.Join(sibling, ".wtc/runtime/tunnels.json"), 0); err != nil {
				t.Fatal(err)
			}
		},
		"managed journal missing": func(t *testing.T, sibling string) {
			root := (&Context{Collection: sibling}).runtimeRoot()
			fixtureFile(t, filepath.Join(root, "manifest.json"), `{"schema":2,"root":"`+root+`","binary":"/synthetic/bin/taba"}`, 0600)
		},
		"unverifiable collection": func(t *testing.T, sibling string) {
			fixtureFile(t, filepath.Join(sibling, CollectionDescriptorFile), "schema_version = 7\n", 0644)
		},
		"terminal control in name": nil,
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			c, p, d := reservation482Receiver(t)
			siblingName := "sibling"
			if damage == nil {
				siblingName = "sib\x1b]0;x\x07ling"
				damage = cases["malformed"]
			}
			sibling := reservation482Sibling(t, c, siblingName)
			damage(t, sibling)
			err := reservation482Register(c, p, context.Background())
			if err == nil {
				t.Fatal("uninspectable sibling evidence was ignored")
			}
			msg := err.Error()
			if !strings.Contains(msg, "tunnel reservation") || strings.ContainsAny(msg, "\x1b\x07\r\n") {
				t.Fatalf("refusal lacks bounded reservation context: %q", msg)
			}
			if !strings.Contains(msg, "sib") {
				t.Fatalf("refusal does not name the sibling collection: %q", msg)
			}
			if reservation482Reserved(t, c) {
				t.Fatal("refused reservation was recorded")
			}
			reservation482NoMutation(t, d)
		})
	}
}

// The workspace policy lock still serializes the whole inventory and the
// receipt write, so two collections cannot both admit the same listener.
func TestTunnelReservation482InventoryStaysUnderWorkspaceLock(t *testing.T) {
	c, p, d := reservation482Receiver(t)
	reservation482Sibling(t, c, "sibling")
	lock := filepath.Join(filepath.Dir(c.Collection), ".config", ".tunnels-policy.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0700); err != nil {
		t.Fatal(err)
	}
	held, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, err := lockDevFile(held, lock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- reservation482Register(c, p, context.Background()) }()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			unlock()
			t.Fatal("reservation bypassed the workspace lock:", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reservation482Reserved(t, c) {
		unlock()
		t.Fatal("reservation recorded while another holder owned the workspace lock")
	}
	unlock()
	if err = <-done; err != nil {
		t.Fatal("reservation after lock release:", err)
	}
	reservation482NoMutation(t, d)
	if !reservation482Reserved(t, c) {
		t.Fatal("reservation receipt was not recorded after lock release")
	}
}

// Collections outside the receiving project are not read; a member's malformed
// journal still refuses.
func TestTunnelReservation482SkipsCollectionsOutsideReceivingProject(t *testing.T) {
	c, _, _ := reservation482Receiver(t)
	sibling := reservation482Sibling(t, c, "sibling")
	fixtureFile(t, filepath.Join(sibling, ".wtc/runtime/tunnels.json"), "{not json", 0600)
	outside := ReceivingContext{Collection: c.Collection, Project: &ProjectContext{Root: t.TempDir()}}
	if _, member, err := tunnelReservationJournal(context.Background(), outside, sibling); err != nil || member {
		t.Fatal("collection outside the receiving project was read:", member, err)
	}
	receiver, err := c.receivingContext()
	if err != nil || receiver.Project == nil {
		t.Fatal("receiver has no project", err)
	}
	if _, _, err = tunnelReservationJournal(context.Background(), receiver, sibling); err == nil {
		t.Fatal("member's malformed journal was ignored")
	}
}
