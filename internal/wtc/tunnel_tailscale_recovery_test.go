package wtc

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func tailscaleRecoveryFixture(t *testing.T) (*Context, *tunnelProviderRuntime, *tailscaleFakeDaemon, tailscaleServeTarget) {
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
	p := &tunnelProviderRuntime{c: c, Local: daemon.transport(t, target)}
	for _, action := range []string{"register", "start"} {
		if err := c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, action, p); err != nil {
			t.Fatal(action, err)
		}
	}
	s, err := c.readTunnelJournal()
	if err != nil || s.Entries[0].Active == nil || !s.Entries[0].Registered {
		t.Fatal("initial scoped serve not active/registered", err)
	}
	return c, p, daemon, target
}

// Each case restores an already-established synthetic session. Reusing its
// fixture avoids repeated Git/runtime setup without sharing mutated test state.
func tailscaleRecoveryReset(t *testing.T, c *Context, p *tunnelProviderRuntime, d *tailscaleFakeDaemon, target tailscaleServeTarget) func(*testing.T) {
	t.Helper()
	journal, err := os.ReadFile(c.tunnelJournalPath())
	if err != nil {
		t.Fatal(err)
	}
	_, policy, err := c.tunnelPolicyPaths()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	config, node, etag := d.config, d.nodeID, d.etag
	calls := append([]string(nil), d.journal...)
	return func(t *testing.T) {
		t.Helper()
		fixtureFile(t, c.tunnelJournalPath(), string(journal), 0600)
		fixtureFile(t, policy, string(data), 0600)
		d.config = config
		d.nodeID = node
		d.etag = etag
		d.journal = append([]string(nil), calls...)
		p.Local = d.transport(t, target)
	}
}

func tailscaleRecoveryPosts(d *tailscaleFakeDaemon) int {
	count := 0
	for _, call := range d.journal {
		if strings.HasPrefix(call, "POST ") {
			count++
		}
	}
	return count
}

func testTailscaleLostServeConfigRecoversAtMutationBoundary(t *testing.T, c *Context, p *tunnelProviderRuntime, d *tailscaleFakeDaemon, target tailscaleServeTarget) {
	reset := tailscaleRecoveryReset(t, c, p, d, target)
	for _, action := range []string{"start", "reconcile"} {
		for _, lost := range []string{"null", "empty", "path-only"} {
			t.Run(action+"/"+lost, func(t *testing.T) {
				reset(t)
				unrelated := `{"TCP":{"8443":{"HTTPS":true},"9443":{"TCPForward":"127.0.0.1:9000"}},"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/other":{"Text":"unrelated"}}}},"AllowFunnel":{"node.synthetic.ts.net:9443":true}}`
				switch lost {
				case "null":
					d.config = "null"
				case "empty":
					d.config = "{}"
				case "path-only":
					d.config = unrelated
				}
				before, _ := os.ReadFile(c.tunnelJournalPath())
				calls := len(d.journal)
				config := d.config
				items := c.recordedTunnelItems(c.runtimeDeclarations())
				after, _ := os.ReadFile(c.tunnelJournalPath())
				if string(before) != string(after) || calls != len(d.journal) || d.config != config {
					t.Fatal("pure status queried/mutated daemon or journal")
				}
				found := false
				for _, item := range items {
					if item.ManagedTunnel {
						found = true
						if item.State != "ready" {
							t.Fatal("recorded status unexpectedly queried live daemon", item.State)
						}
					}
				}
				if !found {
					t.Fatal("recorded serve missing")
				}
				s, _ := c.readTunnelJournal()
				old := s.Entries[0]
				posts := tailscaleRecoveryPosts(d)
				if err := c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, action, p); err != nil {
					t.Fatal(action, err)
				}
				s, err := c.readTunnelJournal()
				if err != nil {
					t.Fatal(err)
				}
				e := s.Entries[0]
				if !e.Registered || e.Known == nil || !lifecycleComplete(e.Operation) {
					t.Fatal("recovery discarded registration or stayed incomplete", e)
				}
				if action == "reconcile" {
					if e.Active != nil || tailscaleRecoveryPosts(d) != posts {
						t.Fatal("reconcile did not clear stale session by exact absence", e)
					}
					if err = c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, "start", p); err != nil {
						t.Fatal("restart after absence reconciliation", err)
					}
					s, _ = c.readTunnelJournal()
					e = s.Entries[0]
				}
				if e.Active == nil || !strings.Contains(d.config, `"/preview"`) || tailscaleRecoveryPosts(d) != posts+1 {
					t.Fatal("lost handler was not re-applied once", d.config, e)
				}
				if e.Resources[0] != old.Resources[0] {
					t.Fatal("local registration reservation changed")
				}
				if lost == "path-only" && (!strings.Contains(d.config, `"/other"`) || !strings.Contains(d.config, `"9443"`)) {
					t.Fatal("recovery removed unrelated handlers/listeners")
				}
			})
		}
	}
}

func testTailscaleMutationRefusesUnknownOrForeignActiveScope(t *testing.T, c *Context, p *tunnelProviderRuntime, d *tailscaleFakeDaemon, target tailscaleServeTarget) {
	reset := tailscaleRecoveryReset(t, c, p, d, target)
	for _, action := range []string{"start", "reconcile", "stop"} {
		for _, mode := range []string{"unreachable", "node-drift", "foreign-handler", "foreign-listener", "foreign-hostname", "foreign-session", "public-listener", "overlap", "malformed", "missing-etag", "receipt-drift", "missing-listener", "unknown-shape", "node-drift-during-read", "unknown-nested-shape"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				reset(t)
				switch mode {
				case "unreachable":
					base := p.Local
					p.Local = func(ctx context.Context, socket string, r *http.Request) (*http.Response, error) {
						d.journal = append(d.journal, r.Method+" "+r.URL.RequestURI())
						if r.Method == http.MethodGet {
							return nil, errors.New("synthetic unavailable scoped socket")
						}
						return base(ctx, socket, r)
					}
				case "node-drift":
					d.nodeID = "foreign-node"
				case "foreign-handler":
					d.config = `{"TCP":{"8443":{"HTTPS":true}},"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/preview":{"Proxy":"http://127.0.0.1:9001"}}}}}`
				case "foreign-listener":
					d.config = `{"TCP":{"8443":{"TCPForward":"127.0.0.1:9001"}}}`
				case "foreign-hostname":
					d.config = `{"TCP":{"8443":{"HTTPS":true}},"Web":{"foreign.synthetic.ts.net:8443":{"Handlers":{}}}}`
				case "foreign-session":
					d.config = `{"Foreground":{"foreign":{"TCP":{"8443":{"HTTPS":true}}}}}`
				case "public-listener":
					d.config = `{"AllowFunnel":{"node.synthetic.ts.net:8443":true}}`
				case "overlap":
					d.config = `{"TCP":{"8443":{"HTTPS":true}},"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:9001"}}}}}`
				case "malformed":
					d.config = `{"TCP":`
				case "missing-etag":
					d.config = `{}`
					d.etag = ""
				case "missing-listener":
					d.config = `{"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/preview":{"Proxy":"http://127.0.0.1:3000"}}}}}`
				case "unknown-shape":
					d.config = `{"FutureConfig":{}}`
				case "unknown-nested-shape":
					d.config = `{"Services":{"svc:foreign":{"FutureConfig":{}}}}`
				case "node-drift-during-read":
					d.config = `{}`
					base := p.Local
					p.Local = func(ctx context.Context, socket string, r *http.Request) (*http.Response, error) {
						resp, err := base(ctx, socket, r)
						if r.Method == http.MethodGet && r.URL.Path == "/localapi/v0/serve-config" {
							d.nodeID = "foreign-node"
						}
						return resp, err
					}
				case "receipt-drift":
					if err := c.tunnelRuntimeStore().Update(context.Background(), func(s *tunnelState) error {
						for i := range s.Entries[0].Resources {
							if s.Entries[0].Resources[i].Kind == "serve" {
								s.Entries[0].Resources[i].Fingerprint = tunnelDigest([]byte("foreign target"))
							}
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := os.ReadFile(c.tunnelJournalPath())
				s, _ := c.readTunnelJournal()
				resources := append([]tunnelResourceRef(nil), s.Entries[0].Resources...)
				config := d.config
				posts := tailscaleRecoveryPosts(d)
				err := c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, action, p)
				if err == nil {
					t.Fatal("unknown/foreign scope treated as live or absent")
				}
				s, readErr := c.readTunnelJournal()
				if readErr != nil {
					t.Fatal(readErr)
				}
				if d.config != config || tailscaleRecoveryPosts(d) != posts || s.Entries[0].Active == nil || !s.Entries[0].Registered || !reflect.DeepEqual(s.Entries[0].Resources, resources) {
					t.Fatal("refusal mutated daemon or forgot active obligations", err)
				}
				after, _ := os.ReadFile(c.tunnelJournalPath())
				if action != "stop" && string(before) != string(after) {
					t.Fatal("observation refusal changed journal")
				}
			})
		}
	}
}

func testTailscaleMatchingActiveObservationIsIdempotent(t *testing.T, c *Context, p *tunnelProviderRuntime, d *tailscaleFakeDaemon, target tailscaleServeTarget) {
	s, _ := c.readTunnelJournal()
	before := s.Entries[0]
	config := d.config
	posts := tailscaleRecoveryPosts(d)
	for _, action := range []string{"start", "reconcile"} {
		if err := c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, action, p); err != nil {
			t.Fatal(action, err)
		}
		s, _ = c.readTunnelJournal()
		e := s.Entries[0]
		if d.config != config || tailscaleRecoveryPosts(d) != posts || e.Operation.ID != before.Operation.ID || !reflect.DeepEqual(e.Resources, before.Resources) || e.Active == nil {
			t.Fatal("matching active session changed or replayed", action, e)
		}
	}
}

func testTailscaleLostStopReplyConfirmsOnlyExactScopedAbsence(t *testing.T, c *Context, p *tunnelProviderRuntime, d *tailscaleFakeDaemon, target tailscaleServeTarget) {
	reset := tailscaleRecoveryReset(t, c, p, d, target)
	for _, mode := range []string{"absent", "node-drift", "foreign-handler", "still-present"} {
		t.Run(mode, func(t *testing.T) {
			reset(t)
			originalConfig := d.config
			base := p.Local
			p.Local = func(ctx context.Context, socket string, r *http.Request) (*http.Response, error) {
				resp, err := base(ctx, socket, r)
				if r.Method == http.MethodPost && err == nil {
					return nil, errors.New("synthetic lost stop reply")
				}
				return resp, err
			}
			if err := c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, "stop", p); err == nil {
				t.Fatal("lost stop response reported success")
			}
			s, _ := c.readTunnelJournal()
			pending := s.Entries[0]
			if pending.Active == nil || lifecycleComplete(pending.Operation) {
				t.Fatal("lost stop reply discarded uncertainty")
			}
			p.Local = d.transport(t, target)
			switch mode {
			case "node-drift":
				d.nodeID = "foreign-node"
			case "foreign-handler":
				d.config = `{"TCP":{"8443":{"HTTPS":true}},"Web":{"node.synthetic.ts.net:8443":{"Handlers":{"/preview":{"Proxy":"http://127.0.0.1:9001"}}}}}`
			case "still-present":
				d.config = originalConfig
			}
			posts := tailscaleRecoveryPosts(d)
			config := d.config
			err := c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, "reconcile", p)
			s, _ = c.readTunnelJournal()
			e := s.Entries[0]
			if tailscaleRecoveryPosts(d) != posts || d.config != config {
				t.Fatal("confirmation replayed cleanup")
			}
			if mode != "absent" {
				if err == nil || e.Active == nil || !reflect.DeepEqual(e.Resources, pending.Resources) || e.Operation.ID != pending.Operation.ID {
					t.Fatal("unknown/foreign/present treated as absent", mode, err)
				}
				// Restoring the exact scoped context and observing actual absence can
				// finish the original stop, without another provider mutation.
				d.nodeID = target.NodeID
				d.config = `{}`
				if err = c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, "reconcile", p); err != nil {
					t.Fatal("exact context/absence could not confirm original stop", err)
				}
				s, _ = c.readTunnelJournal()
				e = s.Entries[0]
			} else if err != nil {
				t.Fatal(err)
			}
			if e.Active != nil || !e.Registered || e.Known == nil || len(e.Resources) != 1 || e.Resources[0].Kind != "reservation" || e.Operation.ID != pending.Operation.ID || !lifecycleComplete(e.Operation) || tailscaleRecoveryPosts(d) != posts {
				t.Fatal("absence confirmation forgot registration/replayed stop", e)
			}
		})
	}
}

func testTailscaleLostServeRetainsIndependentCallbackReceipt(t *testing.T, c *Context, p *tunnelProviderRuntime, d *tailscaleFakeDaemon, target tailscaleServeTarget) {
	callback := tunnelResourceRef{Provider: "callback", Kind: "synthetic-hook", Scope: c.tunnelOwner(), ID: "retained-effect", Ownership: "owned", Owner: c.tunnelOwner(), Fingerprint: tunnelDigest([]byte("callback")), Lifetime: "session"}
	if err := c.tunnelRuntimeStore().Update(context.Background(), func(s *tunnelState) error {
		s.Entries[0].Resources = append(s.Entries[0].Resources, callback)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d.config = `{}`
	posts := tailscaleRecoveryPosts(d)
	if err := c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, "reconcile", p); err == nil {
		t.Fatal("unreleased callback reported cleaned")
	}
	s, err := c.readTunnelJournal()
	if err != nil {
		t.Fatal(err)
	}
	e := s.Entries[0]
	if e.Active != nil || !e.Registered || len(e.Resources) != 2 || e.Resources[1] != callback || tailscaleRecoveryPosts(d) != posts {
		t.Fatal("local absence lost independent callback/registration", e)
	}
}

func TestTunnelTailscaleProductionScopedRecovery(t *testing.T) {
	c, p, d, target := tailscaleRecoveryFixture(t)
	reset := tailscaleRecoveryReset(t, c, p, d, target)
	for _, group := range []struct {
		name string
		run  func(*testing.T, *Context, *tunnelProviderRuntime, *tailscaleFakeDaemon, tailscaleServeTarget)
	}{
		{"lost-config", testTailscaleLostServeConfigRecoversAtMutationBoundary},
		{"scope-refusal", testTailscaleMutationRefusesUnknownOrForeignActiveScope},
		{"matching-live", testTailscaleMatchingActiveObservationIsIdempotent},
		{"lost-stop-reply", testTailscaleLostStopReplyConfirmsOnlyExactScopedAbsence},
		{"callback-retention", testTailscaleLostServeRetainsIndependentCallbackReceipt},
	} {
		t.Run(group.name, func(t *testing.T) { reset(t); group.run(t, c, p, d, target) })
	}
}
