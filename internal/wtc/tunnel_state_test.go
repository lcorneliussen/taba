package wtc

import (
	"reflect"
	"strings"
	"testing"
)

func testTunnelResource(kind, id string) tunnelResourceRef {
	return tunnelResourceRef{CredentialContext: tunnelDigest([]byte("synthetic-provider-context")), Provider: "cloudflare", Scope: "synthetic-account/synthetic-zone", Kind: kind, ID: id, Ownership: "owned", Owner: "synthetic-owner", Fingerprint: tunnelDigest([]byte(kind + ":" + id))}
}

func testTunnelState() tunnelState {
	return tunnelState{Schema: 1, Owner: "synthetic-owner", Entries: []tunnelEntryState{{
		Key: testTunnelKey(), Provider: "cloudflare", Mode: "named", Definition: tunnelDigest([]byte("definition")), Registered: true,
		Resources: []tunnelResourceRef{testTunnelResource("tunnel", "tunnel-1"), testTunnelResource("dns", "dns-1"), testTunnelResource("callback", "callback-1")},
		Operation: &tunnelOperation{ID: "operation-1", Action: "register", Definition: tunnelDigest([]byte("definition")), Steps: []tunnelEffectStep{{"create-tunnel", "complete"}, {"create-dns", "complete"}, {"register-callback", "pending"}}},
	}}}
}

func TestTunnelStatePreservesStoppedRegistrationAndUncertainEffects(t *testing.T) {
	s := testTunnelState()
	data, err := encodeTunnelState(s, s.Owner)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := decodeTunnelState(data, s.Owner)
	if err != nil || !reflect.DeepEqual(recovered, s) {
		t.Fatalf("recovery lost stopped registration or pending callback: %+v %v", recovered, err)
	}
	e := recovered.Entries[0]
	if e.Active != nil || !e.needsCleanup() {
		t.Fatal("no active runner/address incorrectly cleared cleanup obligation")
	}
	op := e.Operation
	if _, err := op.nextStep("register-callback"); err == nil {
		t.Fatal("ambiguous pending hook was eligible for blind retry")
	}
	op.Steps[2].Status = "needs-reconcile"
	if _, err := op.nextStep("unregister-callback"); err == nil {
		t.Fatal("later effect admitted before reconciliation")
	}
	op.Steps[2].Status = "complete"
	if run, err := op.nextStep("register-callback"); err != nil || run {
		t.Fatalf("completed duplicate should be skipped: %v %v", run, err)
	}
	if run, err := op.nextStep("unregister-callback"); err != nil || !run {
		t.Fatalf("reconciled next step refused: %v %v", run, err)
	}
	// Clearing the registration flag is insufficient while exact owned callback
	// and DNS receipts remain. Teardown cannot discard these receipts.
	e.Registered, e.Operation = false, nil
	if !e.needsCleanup() {
		t.Fatal("owned callback/DNS cleanup was forgotten")
	}
	e.Resources = nil
	if e.needsCleanup() {
		t.Fatal("fully cleared entry still needs cleanup")
	}
}

func TestTunnelCleanupUsesExactOwnershipAndCurrentObservation(t *testing.T) {
	tunnel := testTunnelResource("tunnel", "tunnel-1")
	dns := testTunnelResource("dns", "dns-1")
	callback := testTunnelResource("callback", "callback-1")
	borrowed := tunnelResourceRef{Provider: "ngrok", Scope: "synthetic-account", Kind: "wildcard-domain", ID: "domain-1", Ownership: "borrowed"}
	receipts := []tunnelResourceRef{callback, dns, tunnel, borrowed}
	// Synthetic observations stand in for an injected exact-ID provider/HTTP
	// transport. No real executable, account, DNS or callback is touched.
	observations := []tunnelResourceObservation{{Resource: tunnel}, {Resource: dns, Absent: true, CredentialContext: dns.CredentialContext}, {Resource: callback}}
	targets, err := tunnelCleanupTargets("synthetic-owner", receipts, observations)
	if err != nil || !reflect.DeepEqual(targets, []tunnelResourceRef{callback, tunnel}) {
		t.Fatalf("cleanup must preserve borrowed domain and skip exact not-found: %+v %v", targets, err)
	}
	if _, err := tunnelCleanupTargets("synthetic-owner", receipts, observations[:1]); err == nil {
		t.Fatal("missing/failed observation was mistaken for not-found")
	}
	changed := append([]tunnelResourceObservation(nil), observations...)
	changed[0].Resource.Fingerprint = tunnelDigest([]byte("externally modified"))
	if _, err := tunnelCleanupTargets("synthetic-owner", receipts, changed); err == nil {
		t.Fatal("externally changed resource scheduled for deletion")
	}
	changed[0] = observations[0]
	changed[0].Resource.Scope = "other-account"
	if _, err := tunnelCleanupTargets("synthetic-owner", receipts, changed); err == nil {
		t.Fatal("same ID in different account satisfied cleanup observation")
	}
	if _, err := tunnelCleanupTargets("other-owner", receipts, observations); err == nil {
		t.Fatal("different collection's receipts accepted")
	}
	if _, err := tunnelCleanupTargets("synthetic-owner", receipts, append(observations, observations[0])); err == nil {
		t.Fatal("ambiguous duplicate observations accepted")
	}
}

func TestTunnelStateCorruptionCannotHideCleanup(t *testing.T) {
	s := testTunnelState()
	data, err := encodeTunnelState(s, s.Owner)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"wrong owner":                  []byte(strings.ReplaceAll(string(data), "synthetic-owner", "foreign-owner")),
		"unknown schema":               []byte(strings.Replace(string(data), `"schema": 1`, `"schema": 2`, 1)),
		"unknown field":                []byte(strings.Replace(string(data), `"schema": 1`, `"schema": 1, "secret": "synthetic-secret"`, 1)),
		"duplicate resources":          []byte(strings.Replace(string(data), `"resources": [`, `"resources": [], "resources": [`, 1)),
		"case alias resources":         []byte(strings.Replace(string(data), `"resources": [`, `"Resources": [], "resources": [`, 1)),
		"unicode fold alias resources": []byte(strings.Replace(string(data), `"operation": {`, `"reſources": [], "operation": {`, 1)),
		"unicode fold alias entries":   []byte(strings.Replace(string(data), `"entries": [`, `"entries": [], "entrieſ": [`, 1)),
		"duplicate owner":              []byte(strings.Replace(string(data), `"owner": "synthetic-owner"`, `"owner": "foreign-owner", "owner": "synthetic-owner"`, 1)),
		"trailing document":            append(append([]byte(nil), data...), []byte(`{}`)...),
		"bad status":                   []byte(strings.Replace(string(data), `"pending"`, `"retry-automatically"`, 1)),
		"oversized":                    []byte(strings.Repeat(" ", tunnelStateLimit+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTunnelState(data, s.Owner); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("corrupt state accepted or input leaked: %v", err)
			}
		})
	}
	for name, change := range map[string]func(*tunnelState){
		"duplicate entry": func(s *tunnelState) { s.Entries = append(s.Entries, s.Entries[0]) },
		"duplicate resource": func(s *tunnelState) {
			s.Entries[0].Resources = append(s.Entries[0].Resources, s.Entries[0].Resources[0])
		},
		"missing receipt":        func(s *tunnelState) { s.Entries[0].Resources[0].Fingerprint = "" },
		"bad definition":         func(s *tunnelState) { s.Entries[0].Definition = "invalid" },
		"ephemeral registration": func(s *tunnelState) { s.Entries[0].Mode = "quick" },
		"effect after uncertain effect": func(s *tunnelState) {
			s.Entries[0].Operation.Steps = append(s.Entries[0].Operation.Steps, tunnelEffectStep{"next", "complete"})
		},
		"address mismatch": func(s *tunnelState) {
			s.Entries[0].Active = &tunnelAddress{URL: "https://web.example.test", Origin: "https://other.example.test", Hostname: "web.example.test"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fresh := testTunnelState()
			change(&fresh)
			if _, err := encodeTunnelState(fresh, fresh.Owner); err == nil {
				t.Fatal("unsafe state accepted for persistence")
			}
		})
	}
	if _, err := decodeTunnelState([]byte(`{"schema":1,"owner":"synthetic-owner","entries":[]}`), "synthetic-owner"); err != nil {
		t.Fatal(err)
	}
}
