package wtc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func runtimeTunnelControllerFixture(t *testing.T) (*Context, tunnelLifecycle, tunnelLifecycleRequest) {
	t.Helper()
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
	if err := c.SetTunnelPolicy("widget", "web", "preview", testTunnelDefinition().Entry.Policy, false); err != nil {
		t.Fatal(err)
	}
	plan, _, err := c.runtimePlan(c.Config.Runtime.Binary)
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
	l := tunnelLifecycle{Owner: d.Owner, Store: c.tunnelRuntimeStore(), Effects: tunnelLifecycleEffects{
		Authorize: func(context.Context, tunnelEffectCall) error { _, err := c.ValidateControlRoot(); return err },
		Provider: func(_ context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
			m, err := c.runtimeManifest()
			if err != nil || m == nil || m.Schema != 2 {
				t.Fatal("provider effect before durable production forward fence", err)
			}
			state, err := c.readTunnelJournal()
			if err != nil {
				t.Fatal(err)
			}
			op := state.Entries[0].Operation
			if op.ID != call.OperationID || op.Steps[len(op.Steps)-1].Status != "pending" {
				t.Fatal("provider effect before durable production intent")
			}
			address, _ := parseTunnelAddress("https://web.example.test")
			r := tunnelResourceRef{Provider: "cloudflare", Scope: d.ProviderScope, Kind: "tunnel", ID: "synthetic-provider-id", Ownership: "owned", Owner: d.Owner, Fingerprint: tunnelDigest([]byte("synthetic-exact-receipt")), Lifetime: "registration"}
			return tunnelEffectResult{Address: &address, Add: []tunnelResourceRef{r}}, nil
		},
		Publish: func(context.Context, tunnelHostSnapshot) error { return nil },
	}}
	return c, l, tunnelLifecycleRequest{ID: "synthetic-register", Action: "register", Definition: d, Consent: consent}
}

func TestTunnelRuntimeProductionIntentAndSchemaFencePrecedeEffects(t *testing.T) {
	c, l, r := runtimeTunnelControllerFixture(t)
	if err := l.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s, err := c.readTunnelJournal()
	if err != nil || !s.Entries[0].Registered || s.Entries[0].Active != nil || !s.Entries[0].needsCleanup() {
		t.Fatal("stopped registration not retained", err)
	}
	m, err := c.renderRuntime()
	if err != nil || m.Schema != 2 {
		t.Fatal("render lost forward fence", err)
	}
	m.Schema = 1
	if err = runtimeSaveManifest(m); err != nil {
		t.Fatal(err)
	}
	m, err = c.runtimeManifest()
	if err != nil || m.Schema != 2 {
		t.Fatal("manifest regeneration downgraded fence", err)
	}
	if err = os.Remove(filepath.Join(c.runtimeRoot(), "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err = c.RetireRuntime(); err == nil {
		t.Fatal("missing manifest hid persistent registration")
	}
	s, err = c.readTunnelJournal()
	if err != nil || !s.Entries[0].needsCleanup() {
		t.Fatal("refusal lost ownership", err)
	}
}

func TestTunnelRuntimeForwardSchemaAndCorruptionFailClosed(t *testing.T) {
	c, l, r := runtimeTunnelControllerFixture(t)
	if err := l.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.runtimeRoot(), "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"schema":3}`, `{"schema":`} {
		fixtureFile(t, path, bad, 0600)
		if _, err = c.renderRuntime(); err == nil {
			t.Fatal("render accepted unknown/damaged manifest")
		}
		if err = c.RetireRuntime(); err == nil {
			t.Fatal("retire accepted unknown/damaged manifest")
		}
		if err = l.Store.Fence(context.Background()); err == nil {
			t.Fatal("fence overwrote unknown/damaged manifest")
		}
	}
	fixtureFile(t, path, string(data), 0600)
	fixtureFile(t, c.tunnelJournalPath(), `{"schema":1,"owner":"foreign"}`, 0600)
	if _, err = c.renderRuntime(); err == nil {
		t.Fatal("render ignored corrupt journal")
	}
	if err = c.RetireRuntime(); err == nil {
		t.Fatal("retire ignored corrupt journal")
	}
}

func TestTunnelRuntimeSchemaOneRemainsCompatibleWithoutManagedObligations(t *testing.T) {
	c := runtimeFixture(t)
	m, err := c.renderRuntime()
	if err != nil || m.Schema != 1 {
		t.Fatal("legacy runtime schema changed", err)
	}
	s, err := c.readTunnelJournal()
	if err != nil || tunnelJournalObligations(s) {
		t.Fatal(err)
	}
	if _, err = os.Stat(c.tunnelJournalPath()); !os.IsNotExist(err) {
		t.Fatal("pure read created journal")
	}
}
