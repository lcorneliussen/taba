package wtc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const cloudflareSyntheticAccount = "11111111111111111111111111111111"
const cloudflareSyntheticZone = "22222222222222222222222222222222"
const cloudflareSyntheticTunnel = "11111111-1111-4111-8111-111111111111"

func cloudflareSyntheticToken(account, id string) string {
	data, _ := json.Marshal(map[string]any{"a": account, "t": id, "s": make([]byte, 32)})
	return base64.StdEncoding.EncodeToString(data)
}

func cloudflareSyntheticReceipt(kind, scope, id string) tunnelResourceRef {
	return tunnelResourceRef{Provider: "cloudflare", Scope: scope, Kind: kind, ID: id, Ownership: "owned", Owner: "synthetic-owner", Fingerprint: tunnelDigest([]byte("synthetic-owned-baseline"))}
}

func cloudflareFakeEnvelope(result any) *http.Response {
	data, _ := json.Marshal(map[string]any{"success": true, "errors": []any{}, "result": result})
	return tunnelProviderResponse(200, string(data))
}

func TestTunnelCloudflareExactManagementConfigurationDNSAndCleanup(t *testing.T) {
	var journal []string
	token := cloudflareSyntheticToken(cloudflareSyntheticAccount, cloudflareSyntheticTunnel)
	client := tunnelHTTP{Auth: "Bearer synthetic-tunnel-management", RoundTrip: tunnelProviderTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.cloudflare.com" || r.Header.Get("Authorization") != "Bearer synthetic-tunnel-management" {
			t.Fatal("wrong management scope")
		}
		journal = append(journal, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "POST":
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["name"] != "synthetic-owner-web" || body["config_src"] != "cloudflare" {
				t.Fatal("unexpected create body")
			}
			return cloudflareFakeEnvelope(map[string]string{"id": cloudflareSyntheticTunnel, "name": "synthetic-owner-web", "config_src": "cloudflare"}), nil
		case r.Method == "PUT":
			var body struct {
				Config struct {
					Ingress []struct{ Hostname, Service string }
				}
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Config.Ingress) != 2 || body.Config.Ingress[0].Hostname != "preview.example.test" || body.Config.Ingress[0].Service != "http://127.0.0.1:3000" || body.Config.Ingress[1].Service != "http_status:404" {
				t.Fatal("unsafe ingress/catch-all")
			}
			return cloudflareFakeEnvelope(map[string]any{}), nil
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/token"):
			return cloudflareFakeEnvelope(token), nil
		case r.Method == "DELETE" && r.URL.Path == "/client/v4/accounts/"+cloudflareSyntheticAccount+"/cfd_tunnel/"+cloudflareSyntheticTunnel:
			return cloudflareFakeEnvelope(map[string]string{"id": cloudflareSyntheticTunnel}), nil
		default:
			t.Fatal("unexpected management target")
		}
		return nil, nil
	})}
	r, err := createCloudflareTunnel(context.Background(), client, cloudflareSyntheticAccount, "synthetic-owner-web")
	if err != nil || !r.IdentityVerified || r.ID != cloudflareSyntheticTunnel {
		t.Fatal("create did not retain verified identity")
	}
	config := cloudflareSyntheticReceipt("configuration", cloudflareSyntheticAccount, r.ID)
	if err := putCloudflareConfiguration(context.Background(), client, config.Owner, config, tunnelResourceObservation{Resource: config}, "preview.example.test", "http://127.0.0.1:3000"); err != nil {
		t.Fatal(err)
	}
	tunnel := cloudflareSyntheticReceipt("tunnel", cloudflareSyntheticAccount, r.ID)
	got, err := getCloudflareHostToken(context.Background(), client, tunnel.Owner, tunnel, tunnelResourceObservation{Resource: tunnel})
	if err != nil || got != token {
		t.Fatal("exact host capability missing")
	}
	dnsClient := tunnelHTTP{Auth: "Bearer synthetic-DNS-management", RoundTrip: tunnelProviderTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/client/v4/zones/"+cloudflareSyntheticZone+"/dns_records" || r.Header.Get("Authorization") != "Bearer synthetic-DNS-management" {
			t.Fatal("DNS scope widened")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["type"] != "CNAME" || body["content"] != cloudflareSyntheticTunnel+".cfargotunnel.com" || body["proxied"] != true || body["comment"] != "taba-owner:synthetic-owner" {
			t.Fatal("wrong owned DNS mapping")
		}
		body["id"] = "33333333333333333333333333333333"
		return cloudflareFakeEnvelope(body), nil
	})}
	dns, err := createCloudflareDNS(context.Background(), dnsClient, cloudflareSyntheticZone, "preview.example.test", r.ID, tunnel.Owner)
	if err != nil || dns.Receipt == nil || dns.ID == "" {
		t.Fatal("missing owned DNS receipt")
	}
	dnsClient.RoundTrip = tunnelProviderTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "DELETE" || r.URL.Path != "/client/v4/zones/"+cloudflareSyntheticZone+"/dns_records/"+dns.ID {
			t.Fatal("DNS deletion widened")
		}
		journal = append(journal, "delete exact DNS")
		return cloudflareFakeEnvelope(map[string]string{"id": dns.ID}), nil
	})
	if err := deleteCloudflareResource(context.Background(), dnsClient, tunnel.Owner, *dns.Receipt, tunnelResourceObservation{Resource: *dns.Receipt}, true); err != nil {
		t.Fatal(err)
	}
	if err := deleteCloudflareResource(context.Background(), client, tunnel.Owner, tunnel, tunnelResourceObservation{Resource: tunnel}, true); err != nil {
		t.Fatal(err)
	}
	if journal[len(journal)-2] != "delete exact DNS" || !strings.HasPrefix(journal[len(journal)-1], "DELETE /client/v4/accounts/") {
		t.Fatal("separate cleanup steps not observed")
	}
}

func TestTunnelCloudflareOwnershipAndResponseRefusals(t *testing.T) {
	calls := 0
	client := tunnelHTTP{Auth: "Bearer synthetic", RoundTrip: tunnelProviderTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return cloudflareFakeEnvelope(map[string]any{}), nil
	})}
	r := cloudflareSyntheticReceipt("tunnel", cloudflareSyntheticAccount, cloudflareSyntheticTunnel)
	for _, change := range []func(*tunnelResourceRef){func(r *tunnelResourceRef) { r.Ownership = "borrowed" }, func(r *tunnelResourceRef) { r.Owner = "another-owner" }, func(r *tunnelResourceRef) { r.ID = "../foreign" }} {
		bad := r
		change(&bad)
		if deleteCloudflareResource(context.Background(), client, r.Owner, bad, tunnelResourceObservation{Resource: bad}, true) == nil {
			t.Fatal("unsafe cleanup allowed")
		}
	}
	if deleteCloudflareResource(context.Background(), client, r.Owner, r, tunnelResourceObservation{Resource: r}, false) == nil || calls != 0 {
		t.Fatal("running connector reached deletion")
	}
	changed := r
	changed.Fingerprint = tunnelDigest([]byte("external change"))
	if deleteCloudflareResource(context.Background(), client, r.Owner, r, tunnelResourceObservation{Resource: changed}, true) == nil || calls != 0 {
		t.Fatal("changed resource reached deletion")
	}
	for _, response := range []*http.Response{tunnelProviderResponse(302, `{"token":"synthetic-sensitive-value"}`), tunnelProviderResponse(200, `{"success":false,"errors":[{"message":"synthetic-sensitive-value"}]}`)} {
		client.RoundTrip = tunnelProviderTransport(func(*http.Request) (*http.Response, error) { calls++; return response, nil })
		if err := deleteCloudflareResource(context.Background(), client, r.Owner, r, tunnelResourceObservation{Resource: r}, true); err == nil || strings.Contains(err.Error(), "synthetic-sensitive-value") {
			t.Fatal("provider failure leaked or followed redirect")
		}
	}
	client.RoundTrip = tunnelProviderTransport(func(*http.Request) (*http.Response, error) {
		return cloudflareFakeEnvelope(map[string]any{"id": "33333333333333333333333333333333", "name": "foreign.example.test", "identityVerified": true}), nil
	})
	dns, err := createCloudflareDNS(context.Background(), client, cloudflareSyntheticZone, "preview.example.test", r.ID, r.Owner)
	if err == nil || dns.ID == "" || dns.Receipt != nil {
		t.Fatal("unverified DNS assertion discarded or trusted")
	}
}

func TestTunnelCloudflareProviderJSONCannotSetLocalEvidence(t *testing.T) {
	var registration cloudflareTunnelRegistration
	var dns cloudflareDNSRegistration
	data := []byte(`{"identityVerified":true,"IdentityVerified":true,"Receipt":{"ownership":"owned","owner":"foreign"}}`)
	if json.Unmarshal(data, &registration) != nil || registration.IdentityVerified || json.Unmarshal(data, &dns) != nil || dns.Receipt != nil {
		t.Fatal("provider populated local ownership evidence")
	}
}

func TestTunnelCloudflareCommandUsesOnlyBoundSuppliedTokenAndDedicatedConfig(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "cloudflared.yml")
	binary := filepath.Join(dir, "fake-cloudflared")
	fixtureFile(t, config, "{}\n", 0600)
	token := cloudflareSyntheticToken(cloudflareSyntheticAccount, cloudflareSyntheticTunnel)
	env := []string{"HOME=" + dir, "PATH=" + dir}
	cmd, err := cloudflareHostCommand(context.Background(), binary, config, cloudflareSyntheticAccount, cloudflareSyntheticTunnel, token, env)
	if err != nil || strings.Contains(strings.Join(cmd.Args, " "), token) || !reflect.DeepEqual(cmd.Env, append(env, "TUNNEL_TOKEN="+token)) {
		t.Fatal("host token leaked or environment inherited")
	}
	for _, bad := range []string{"", "invalid", cloudflareSyntheticToken(cloudflareSyntheticZone, cloudflareSyntheticTunnel)} {
		if _, err := cloudflareHostCommand(context.Background(), binary, config, cloudflareSyntheticAccount, cloudflareSyntheticTunnel, bad, env); err == nil {
			t.Fatal("unbound token permitted fallback")
		}
	}
	if err := os.WriteFile(config, []byte("{}"+strings.Repeat(" ", 70)+"\ncredentials-file: foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudflareHostCommand(context.Background(), binary, config, cloudflareSyntheticAccount, cloudflareSyntheticTunnel, token, env); err == nil {
		t.Fatal("non-dedicated config accepted")
	}
}
