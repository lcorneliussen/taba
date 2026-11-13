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

func TestTunnelProductionNamedRegistrationStopCleanupAndPureRead(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
	scope, err := c.ValidateControlRoot()
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(scope.Root, "provider/manage.token"), "synthetic-management", 0600)
	fixtureFile(t, filepath.Join(scope.Root, "provider/cleanup.token"), "synthetic-cleanup", 0600)
	entry := tunnelPolicyEntry{Provider: "ngrok", Mode: "domain", Access: "public", Options: tunnelProviderOptions{Domain: "web.example.test", Reserve: true}, Inputs: tunnelInputRefs{ManagementFile: "provider/manage.token", CleanupFile: "provider/cleanup.token"}}
	for _, name := range []string{"preview", "second"} {
		if err = c.SetTunnelPolicy("widget", "web", name, entry, false); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	metadata := ""
	deleted := false
	p := &tunnelProviderRuntime{c: c, HTTP: tunnelContextTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body := "{}"
		switch r.Method {
		case http.MethodPost:
			if r.Header.Get("Authorization") != "Bearer synthetic-management" {
				t.Fatal("wrong registration role")
			}
			var request struct{ Domain, Metadata string }
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.Domain != "web.example.test" || request.Metadata == "" {
				t.Fatal("invalid exact reservation request")
			}
			metadata = request.Metadata
			body = fmt.Sprintf(`{"id":"rd_synthetic","domain":"web.example.test","metadata":%q}`, metadata)
		case http.MethodGet:
			if r.Header.Get("Authorization") != "Bearer synthetic-cleanup" {
				t.Fatal("wrong cleanup role")
			}
			body = fmt.Sprintf(`{"id":"rd_synthetic","domain":"web.example.test","metadata":%q}`, metadata)
		case http.MethodDelete:
			state, e := c.readTunnelJournal()
			if e != nil {
				t.Fatal(e)
			}
			receipt := state.Entries[0].Resources[0]
			if receipt.CredentialContext != tunnelCredentialContext(state.Entries[0].Operation.Lifecycle.Definition, "Bearer synthetic-cleanup") {
				t.Fatal("cleanup role was not positively rebound before deletion")
			}
			deleted = true
		default:
			t.Fatal("unexpected provider method")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if err = c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Public: true}, "register", p); err == nil || calls != 0 {
		t.Fatal("ambiguous names caused an effect", err)
	}
	opt := TunnelOptions{Service: "web", Name: "preview", Public: true}
	if err = c.runManagedTunnel(context.Background(), opt, "register", p); err != nil {
		t.Fatal(err)
	}
	state, err := c.readTunnelJournal()
	if err != nil || len(state.Entries) != 1 || !state.Entries[0].Registered || state.Entries[0].Active != nil {
		t.Fatal("registration was not distinct from active hosting", err)
	}
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "preview", Stop: true}); err != nil {
		t.Fatal(err)
	}
	state, err = c.readTunnelJournal()
	if err != nil || !state.Entries[0].Registered || len(state.Entries[0].Resources) != 1 || calls != 1 {
		t.Fatal("stop discarded registration or queried a provider", err)
	}
	before, _ := os.ReadFile(c.tunnelJournalPath())
	recorded := c.recordedTunnelItems(c.runtimeDeclarations())
	after, _ := os.ReadFile(c.tunnelJournalPath())
	if string(before) != string(after) || calls != 1 {
		t.Fatal("pure read mutated state or queried provider")
	}
	found := false
	for _, item := range recorded {
		if item.ManagedTunnel && item.TunnelName == "preview" {
			found = item.Registered && item.RegistrationURL == "https://web.example.test" && item.URL == "" && item.TunnelIntent == "off"
		}
	}
	if !found {
		t.Fatal("stopped registered address/selection missing from recorded status")
	}
	if err = c.runManagedTunnel(context.Background(), opt, "unregister", p); err != nil {
		t.Fatal(err)
	}
	state, err = c.readTunnelJournal()
	if err != nil || !deleted || state.Entries[0].needsCleanup() {
		t.Fatal("exact owned cleanup incomplete", err)
	}
}

func TestTunnelProductionTailscaleLifecycleUsesOnlyAuthorizedFakeSocket(t *testing.T) {
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
	initial := `{"TCP":{"9443":{"TCPForward":"127.0.0.1:9000"}},"Web":{"node.synthetic.ts.net:9443":{"Handlers":{"/other":{"Text":"unrelated"}}}},"AllowFunnel":{"node.synthetic.ts.net:9443":true}}`
	daemon := tailscaleSyntheticDaemon(initial)
	p := &tunnelProviderRuntime{c: c, Local: daemon.transport(t, target)}
	opt := TunnelOptions{Service: "web", Name: "preview"}
	for _, action := range []string{"register", "start", "stop", "unregister"} {
		if err := c.runManagedTunnel(context.Background(), opt, action, p); err != nil {
			t.Fatal(action, err)
		}
		s, err := c.readTunnelJournal()
		if err != nil {
			t.Fatal(err)
		}
		switch action {
		case "register":
			if !s.Entries[0].Registered || s.Entries[0].Active != nil {
				t.Fatal("local reservation fabricated an active handler")
			}
			// Explicit project defaults must not tear down this collection's
			// independent override; re-saving that override must also be inert.
			definition := filepath.Join(t.TempDir(), "entry.toml")
			fixtureFile(t, definition, "schema=1\n[tunnels.widget.web.preview]\nprovider='ngrok'\nmode='domain'\naccess='public'\n[tunnels.widget.web.preview.options]\ndomain='other.example.test'\n", 0600)
			if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "preview", Action: "configure", DefinitionFile: definition, Project: true}); err != nil {
				t.Fatal(err)
			}
			local, err := os.ReadFile(filepath.Join(c.Collection, ".config/tunnels.toml"))
			if err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, definition, string(local), 0600)
			if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "preview", Action: "configure", DefinitionFile: definition}); err != nil {
				t.Fatal(err)
			}
			unchanged, err := c.readTunnelJournal()
			if err != nil || !unchanged.Entries[0].Registered || len(unchanged.Entries[0].Resources) != 1 {
				t.Fatal("policy save removed an unrelated/unchanged override registration", err)
			}
		case "start":
			if s.Entries[0].Active == nil || !strings.Contains(daemon.config, "/preview") {
				t.Fatal("confirmed handler/address missing")
			}
		case "stop":
			if !s.Entries[0].Registered || s.Entries[0].Active != nil || strings.Contains(daemon.config, "/preview") {
				t.Fatal("stop lost registration or retained serving")
			}
		case "unregister":
			if s.Entries[0].needsCleanup() {
				t.Fatal("local cleanup remained after confirmed stop")
			}
		}
	}
	var before, after any
	if json.Unmarshal([]byte(initial), &before) != nil || json.Unmarshal([]byte(daemon.config), &after) != nil {
		t.Fatal("invalid fake daemon state")
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatalf("unrelated daemon data changed: %s", a)
	}
	posts := 0
	for _, call := range daemon.journal {
		if strings.HasPrefix(call, "POST") {
			posts++
		}
	}
	if posts != 2 {
		t.Fatal("unexpected mutation count", posts)
	}
	count := len(daemon.journal)
	c.recordedTunnelItems(c.runtimeDeclarations())
	if len(daemon.journal) != count {
		t.Fatal("pure status queried daemon")
	}
	if err := c.RetireRuntime(); err != nil {
		t.Fatal("normal runtime teardown", err)
	}
	if err := c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
		t.Fatal("normal teardown permanently blocked desired policy", err)
	}
	for _, action := range []string{"register", "start", "stop", "unregister"} {
		if err := c.runManagedTunnel(context.Background(), opt, action, p); err != nil {
			t.Fatal("reuse after teardown", action, err)
		}
	}
	// Native removal must never parse desired TOML as a YAML task fragment.
	if _, err := c.RemoveService("web", true); err != nil {
		t.Fatal(err)
	}
	policy, err := readTunnelPolicyFile(filepath.Join(c.Collection, ".config/tunnels.toml"))
	if err != nil || policy.Entries[testTunnelKey()].Provider != "tailscale" {
		t.Fatal("native removal corrupted receiving desired policy", err)
	}
}

func TestTunnelProductionDevAndCloudflareRegistrationCleanupFakeTransports(t *testing.T) {
	for _, provider := range []string{"devtunnel", "cloudflare"} {
		t.Run(provider, func(t *testing.T) {
			c := runtimeFixture(t)
			fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n", 0644)
			scope, err := c.ValidateControlRoot()
			if err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"management", "cleanup"} {
				fixtureFile(t, filepath.Join(scope.Root, role+".token"), "synthetic-role-"+role, 0600)
			}
			entry := tunnelPolicyEntry{Provider: provider, Mode: "persistent", Access: "public", Options: tunnelProviderOptions{Registration: "synthetic-registration"}, Inputs: tunnelInputRefs{ManagementFile: "management.token", CleanupFile: "cleanup.token"}}
			account, zone, id := strings.Repeat("a", 32), strings.Repeat("b", 32), "11111111-1111-1111-1111-111111111111"
			if provider == "cloudflare" {
				entry.Mode = "named"
				entry.Options = tunnelProviderOptions{Registration: "synthetic-registration", AccountID: account, ZoneID: zone, Hostname: "web.example.test"}
			}
			if err = c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
				t.Fatal(err)
			}
			config := any(map[string]any{"ingress": []any{map[string]string{"service": "http_status:404"}}})
			var dns map[string]any
			deletes := []string{}
			p := &tunnelProviderRuntime{c: c, HTTP: tunnelContextTransport(func(r *http.Request) (*http.Response, error) {
				role := "management"
				if r.Method == http.MethodGet || r.Method == http.MethodDelete {
					role = "cleanup"
					if provider == "cloudflare" && dns == nil {
						role = "management"
					}
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-role-"+role {
					t.Fatal("wrong explicit role", r.Method, r.URL.Path)
				}
				var result any
				if provider == "devtunnel" {
					if !strings.HasSuffix(r.URL.Hostname(), ".rel.tunnels.api.visualstudio.com") || !strings.HasSuffix(r.URL.Path, "/synthetic-registration") {
						t.Fatal("wrong devtunnel scope")
					}
					if r.Method == http.MethodPut && r.Header.Get("If-None-Match") != "*" {
						t.Fatal("registration may overwrite an existing tunnel")
					}
					result = map[string]any{"tunnelId": "synthetic-registration", "clusterId": "usw2", "accessTokens": map[string]string{"host": "synthetic-role-host"}, "ports": []any{map[string]any{"portNumber": 3000, "portForwardingUris": []string{"https://synthetic-3000.usw2.devtunnels.ms"}}}}
				} else {
					if r.URL.Hostname() != "api.cloudflare.com" {
						t.Fatal("wrong Cloudflare scope")
					}
					switch {
					case strings.HasSuffix(r.URL.Path, "/configurations"):
						if r.Method == http.MethodPut {
							var body map[string]any
							if json.NewDecoder(r.Body).Decode(&body) != nil {
								t.Fatal("invalid configuration")
							}
							config = body["config"]
						}
						result = map[string]any{"config": config}
					case strings.Contains(r.URL.Path, "/dns_records"):
						if r.Method == http.MethodPost {
							if json.NewDecoder(r.Body).Decode(&dns) != nil {
								t.Fatal("invalid DNS")
							}
							dns["id"] = strings.Repeat("c", 32)
						}
						result = dns
					default:
						result = map[string]any{"id": id, "name": "synthetic-registration", "config_src": "cloudflare", "account_tag": account}
					}
					result = map[string]any{"success": true, "result": result}
				}
				if r.Method == http.MethodDelete {
					deletes = append(deletes, r.URL.Path)
				}
				body, _ := json.Marshal(result)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			opt := TunnelOptions{Service: "web", Name: "preview", Public: true}
			if err = c.runManagedTunnel(context.Background(), opt, "register", p); err != nil {
				t.Fatal(err)
			}
			state, err := c.readTunnelJournal()
			if err != nil || !state.Entries[0].Registered || state.Entries[0].Active != nil || state.Entries[0].Known == nil {
				t.Fatal("registration/hosting conflated", err)
			}
			count := 1
			if provider == "cloudflare" {
				count = 3
			}
			if len(state.Entries[0].Resources) != count {
				t.Fatal("compound ownership receipts missing")
			}
			if err = c.runManagedTunnel(context.Background(), opt, "unregister", p); err != nil {
				t.Fatal(err)
			}
			state, err = c.readTunnelJournal()
			if err != nil || state.Entries[0].needsCleanup() {
				t.Fatal("cleanup retained unexpected obligation", err)
			}
			if provider == "cloudflare" {
				if len(deletes) != 2 || !strings.Contains(deletes[0], "/dns_records/") || !strings.Contains(deletes[1], "/cfd_tunnel/") {
					t.Fatal("wrong owned cleanup order", deletes)
				}
			} else if len(deletes) != 1 {
				t.Fatal("temporary/foreign identities were deleted", deletes)
			}
			journal, _ := os.ReadFile(c.tunnelJournalPath())
			if strings.Contains(string(journal), "synthetic-role-") {
				t.Fatal("provider token escaped into journal")
			}
		})
	}
}

func TestTunnelDeclaredDefinitionCannotSelectOrConsentToNamedPolicy(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {url: 'http://127.0.0.1:3000'}\n web/tunnel:\n  cmd: [echo, synthetic]\n  deps: [web/server]\n  tags: [endpoint]\n", 0644)
	entry := tunnelPolicyEntry{Provider: "cloudflare", Mode: "quick", Access: "public", Inputs: tunnelInputRefs{Executable: "/synthetic/not-executed"}}
	if err := c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
		t.Fatal(err)
	}
	plan, _, err := c.runtimePlanWithTunnels(c.Config.Runtime.Binary, false)
	if err != nil {
		t.Fatal(err)
	}
	definition := ""
	for _, task := range plan.Tasks {
		if task.Kind == "endpoint" {
			definition = task.Definition
		}
	}
	if !validTunnelDigest(definition) {
		t.Fatal("explicit legacy endpoint definition missing")
	}
	// Even when a single named policy exists, a gateway-observed declaration
	// targets only its own native task and cannot approve or allocate that policy.
	result, err := c.Tunnel(TunnelOptions{Service: "web", Declared: true, ExpectedDefinition: definition, Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "up" {
		t.Fatal("declared task did not reach native action", result.Action)
	}
	state, err := c.readTunnelJournal()
	if err != nil || len(state.Entries) != 0 {
		t.Fatal("legacy action consented/allocated named policy", err)
	}
	calls, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "calls"))
	if !strings.Contains(string(calls), "start widget/web/tunnel") || strings.Contains(string(calls), "tunnel-host") {
		t.Fatal("declared action selected a named host", string(calls))
	}
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Declared: true, ExpectedDefinition: tunnelDigest([]byte("stale")), Public: true}); err == nil {
		t.Fatal("changed native declaration accepted")
	}
	if _, err = c.Tunnel(TunnelOptions{Service: "web", Name: "preview", Declared: true, ExpectedDefinition: definition, Public: true}); err == nil {
		t.Fatal("declared selector accepted named entry")
	}
}
