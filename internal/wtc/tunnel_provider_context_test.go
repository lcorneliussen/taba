package wtc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tunnelContextTransport func(*http.Request) (*http.Response, error)

func (f tunnelContextTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTunnelCleanupCredentialRotationNeverTurnsForeign404IntoAbsence(t *testing.T) {
	for _, provider := range []string{"ngrok", "devtunnel", "cloudflare"} {
		for _, rotation := range []string{"before-first-lookup", "after-positive-lookup", "positive-rebind", "foreign-positive"} {
			t.Run(provider+"/"+rotation, func(t *testing.T) {
				c, _, _ := runtimeTunnelControllerFixture(t)
				scope, err := c.ValidateControlRoot()
				if err != nil {
					t.Fatal(err)
				}
				ref := "provider/cleanup.token"
				path := filepath.Join(scope.Root, ref)
				fixtureFile(t, path, "synthetic-token-A", 0600)
				d := testTunnelDefinition()
				d.Owner = c.tunnelOwner()
				d.ControlScope = ""
				scopeBytes, _ := json.Marshal([2]string{scope.OwnerRoot, scope.Root})
				d.ControlScope = tunnelDigest(scopeBytes)
				d.Entry.Policy.Inputs.CleanupFile = ref
				d.Entry.Policy.Provider = provider
				d.Entry.Policy.Mode = "domain"
				d.Entry.Policy.Options = tunnelProviderOptions{Domain: "web.example.test"}
				if provider == "devtunnel" {
					d.Entry.Policy.Mode = "persistent"
					d.Entry.Policy.Options = tunnelProviderOptions{}
				}
				if provider == "cloudflare" {
					d.Entry.Policy.Mode = "named"
					d.Entry.Policy.Options = tunnelProviderOptions{Hostname: "web.example.test", AccountID: strings.Repeat("a", 32), ZoneID: strings.Repeat("b", 32)}
				}
				digest, err := d.digest()
				if err != nil {
					t.Fatal(err)
				}
				receipt := tunnelResourceRef{Provider: provider, Scope: "synthetic-cluster", Kind: "domain", ID: "rd_synthetic", Ownership: "owned", Owner: d.Owner, Lifetime: "registration"}
				positive := `{"id":"rd_synthetic","domain":"web.example.test","metadata":"registration-A"}`
				receipt.Fingerprint = tunnelDigest([]byte("rd_synthetic/web.example.test/registration-A"))
				if provider == "devtunnel" {
					receipt.Kind = "tunnel"
					receipt.Scope = "usw2"
					receipt.ID = "synthetic-tunnel"
					positive = `{"tunnelId":"synthetic-tunnel","clusterId":"usw2","ports":[{"portNumber":3000}]}`
					receipt.Fingerprint = tunnelDigest([]byte("usw2/synthetic-tunnel/3000"))
				}
				if provider == "cloudflare" {
					receipt.Kind = "tunnel"
					receipt.Scope = strings.Repeat("a", 32)
					receipt.ID = "11111111-1111-1111-1111-111111111111"
					r := cloudflareTunnelRegistration{ID: receipt.ID, Name: "taba-synthetic", ConfigSource: "cloudflare", AccountTag: receipt.Scope}
					receipt.Fingerprint = cloudflareTunnelFingerprint(receipt.Scope, r)
					raw, _ := json.Marshal(r)
					positive = `{"success":true,"result":` + string(raw) + `}`
				}
				receipt.CredentialContext = tunnelDigest([]byte(provider + "\x00" + d.ControlScope + "\x00Bearer synthetic-token-A"))
				state := tunnelState{Schema: 1, Owner: d.Owner, Entries: []tunnelEntryState{{Key: d.Entry.Key, Provider: provider, Mode: d.Entry.Policy.Mode, Definition: digest, Registered: true, Resources: []tunnelResourceRef{receipt}, Operation: &tunnelOperation{ID: "cleanup", Action: "unregister", Definition: digest, Steps: []tunnelEffectStep{{"unregister", "pending"}}, Lifecycle: &tunnelLifecycleOperation{Definition: d, Stages: []string{"unregister", "event-unregistered"}}}}}}
				before, _ := tunnelSnapshotForEvent(nil)
				state.Entries[0].Operation.Lifecycle.Before = before
				data, err := encodeTunnelState(state, d.Owner)
				if err != nil {
					t.Fatal(err)
				}
				fixtureFile(t, c.tunnelJournalPath(), string(data), 0600)
				if rotation == "before-first-lookup" || rotation == "positive-rebind" || rotation == "foreign-positive" {
					fixtureFile(t, path, "synthetic-token-B", 0600)
				}
				wrong, deleted := 0, 0
				p := &tunnelProviderRuntime{c: c, HTTP: tunnelContextTransport(func(r *http.Request) (*http.Response, error) {
					body, status := positive, 200
					if r.Header.Get("Authorization") == "Bearer synthetic-token-B" {
						wrong++
						if rotation == "positive-rebind" && r.Method == http.MethodGet {
							body = positive
							status = 200
						} else if rotation == "foreign-positive" {
							body = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(positive, "registration-A", "foreign-registration"), "3000", "3001"), "taba-synthetic", "foreign-name")
							status = 200
						} else {
							body = "{}"
							status = 404
						}
					}
					if r.Method == http.MethodDelete {
						deleted++
						if rotation == "positive-rebind" {
							current, err := c.readTunnelJournal()
							if err != nil || len(current.Entries[0].Resources) != 1 || current.Entries[0].Resources[0].CredentialContext != tunnelCredentialContext(d, "Bearer synthetic-token-B") {
								t.Fatal("rebind not persisted before deletion", err)
							}
						}
					}
					if r.Method == http.MethodGet && r.Header.Get("Authorization") == "Bearer synthetic-token-A" && rotation == "after-positive-lookup" {
						if err := os.WriteFile(path, []byte("synthetic-token-B"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				entry := state.Entries[0]
				call := tunnelEffectCall{Owner: d.Owner, OperationID: "cleanup", Stage: "unregister", Definition: d, Entry: entry}
				result, err := p.unregister(context.Background(), call)
				if err == nil && rotation != "positive-rebind" {
					t.Fatalf("credential rotation accepted as resource absence: removed=%+v wrong-context=%d deletes=%d", result.Remove, wrong, deleted)
				}
				after, readErr := c.readTunnelJournal()
				if readErr != nil {
					t.Fatal(readErr)
				}
				if rotation == "positive-rebind" {
					if err != nil || deleted != 1 || len(after.Entries[0].Resources) != 0 {
						t.Fatal("exact positive capability rebind failed", err)
					}
				} else if len(after.Entries[0].Resources) != 1 || after.Entries[0].Resources[0] != receipt {
					t.Fatal("original A receipt was forgotten or rebound without proof")
				}
				encoded, _ := encodeTunnelState(after, d.Owner)
				if strings.Contains(string(encoded), "synthetic-token-") {
					t.Fatal("raw credential entered journal")
				}
				if rotation == "after-positive-lookup" && (deleted != 0 || wrong != 0) {
					t.Fatal(fmt.Sprintf("rotated credential reached deletion (%d)", deleted))
				}
				if rotation != "positive-rebind" {
					// The explicit lost-response reconciliation path must not
					// reinterpret B's absence as proof about A either.
					_, confirmed, confirmErr := p.confirm(context.Background(), call)
					if confirmed || confirmErr == nil {
						t.Fatal("reconciliation accepted a foreign credential context")
					}
					afterConfirm, err := c.readTunnelJournal()
					if err != nil || afterConfirm.Entries[0].Resources[0] != receipt {
						t.Fatal("reconciliation forgot the original receipt", err)
					}
				}
			})
		}
	}
}

func TestTunnelPositiveObservationCannotRebindAnotherCapability(t *testing.T) {
	c, _, _ := runtimeTunnelControllerFixture(t)
	p := &tunnelProviderRuntime{c: c}
	receipt := testTunnelResource("tunnel", "synthetic-resource")
	observed := tunnelResourceObservation{Resource: receipt, CredentialContext: receipt.CredentialContext}
	// Identical resource contents from A cannot prove that B sees that resource.
	// This is refused before any journal checkpoint or remote effect.
	if _, err := p.bindProviderObservation(context.Background(), tunnelEffectCall{}, tunnelHTTP{CredentialContext: tunnelDigest([]byte("other-capability"))}, receipt, observed); err == nil {
		t.Fatal("positive observation crossed capability contexts")
	}
}
