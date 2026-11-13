package wtc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTunnelProductionStrictCallbackFailureRetainsOriginalEventWithoutReplay(t *testing.T) {
	c, l, r := runtimeTunnelControllerFixture(t)
	path := filepath.Join(c.Collection, "widget", "event.sh")
	fixtureFile(t, path, "#!/bin/sh\ncat > event-input.json\nprintf x >> event-count\nprintf 'sensitive-provider-response' >&2\nexit 7\n", 0755)
	r.Definition.Entry.Policy.Events = map[string]tunnelEventCommand{"registered": {Command: []string{"event.sh"}}}
	if err := c.SetTunnelPolicy("widget", "web", "preview", r.Definition.Entry.Policy, false); err != nil {
		t.Fatal(err)
	}
	var err error
	r.Definition.HooksDigest, err = c.tunnelCallbackDigest("widget", r.Definition.Entry.Policy.Events)
	if err != nil {
		t.Fatal(err)
	}
	r.Consent, err = newTunnelConsent(r.Definition, true, tunnelAccessEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	l.Effects.Event = c.dispatchTunnelEvent
	if err = l.Execute(context.Background(), r); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("strict hook failure was swallowed or leaked output", err)
	}
	state, err := c.readTunnelJournal()
	if err != nil {
		t.Fatal(err)
	}
	op := state.Entries[0].Operation
	if !state.Entries[0].Registered || !lifecycleUncertain(op) || op.Lifecycle.Event == nil {
		t.Fatal("failure lost registration or retained event")
	}
	data, err := os.ReadFile(filepath.Join(c.Collection, "widget/event-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var event tunnelEvent
	if json.Unmarshal(data, &event) != nil || event.ID != op.Lifecycle.Event.ID || event.Type != "registered" {
		t.Fatal("callback did not receive original typed event")
	}
	if err = l.Execute(context.Background(), r); err == nil {
		t.Fatal("unknown hook replayed")
	}
	if err = l.Reconcile(context.Background(), r.Definition.Entry.Key, r.ID); err == nil {
		t.Fatal("unknown arbitrary hook fabricated confirmation")
	}
	count, err := os.ReadFile(filepath.Join(c.Collection, "widget/event-count"))
	if err != nil || string(count) != "x" {
		t.Fatal("hook repeated", err)
	}
	changed := *op.Lifecycle.Event
	changed.ID = "other-event"
	if _, err = c.dispatchTunnelEvent(context.Background(), changed); err == nil {
		t.Fatal("dispatcher accepted different retained input")
	}
	fixtureFile(t, path, "#!/bin/sh\nprintf x >> event-count\n", 0755)
	if _, err = c.dispatchTunnelEvent(context.Background(), *op.Lifecycle.Event); err == nil {
		t.Fatal("changed callback code accepted old approval")
	}
}

func TestTunnelReloadGraphExcludesTunnelAndDetectsCycles(t *testing.T) {
	plan := &runtimeManifest{Tasks: []runtimeTask{
		{RuntimeItem: RuntimeItem{Path: "widget/db/init", Kind: "job"}},
		{RuntimeItem: RuntimeItem{Path: "widget/api/server", Kind: "service"}, Deps: []string{"widget/db/init"}},
		{RuntimeItem: RuntimeItem{Path: "widget/web/server", Kind: "service"}, Deps: []string{"widget/api/server"}},
		{RuntimeItem: RuntimeItem{Path: "widget/web/tunnel-preview", Kind: "endpoint", ManagedTunnel: true}},
	}}
	order, err := tunnelReloadOrder(plan, []string{"widget/web/server", "widget/api/server"})
	if err != nil || strings.Join(order, ",") != "widget/api/server,widget/web/server" {
		t.Fatal(order, err)
	}
	if _, err = tunnelReloadOrder(plan, []string{"widget/web/tunnel-preview"}); err == nil {
		t.Fatal("tunnel restart loop accepted")
	}
	plan.Tasks[1].Deps = append(plan.Tasks[1].Deps, "widget/web/server")
	if _, err = tunnelReloadOrder(plan, []string{"widget/web/server"}); err == nil {
		t.Fatal("reload cycle accepted")
	}
}
