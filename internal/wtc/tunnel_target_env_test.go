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

// namedTargetFixture declares widget/web with the given x-wtc metadata and
// collection env, and selects a Tailscale serve entry for it. Everything is
// synthetic: the provider is a fake LocalAPI transport.
func namedTargetFixture(t *testing.T, metadata, env string) *Context {
	t.Helper()
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: "+metadata+"\n", 0644)
	if env != "" {
		fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), env, 0600)
	}
	target := tailscaleSyntheticTarget()
	entry := tunnelPolicyEntry{Provider: "tailscale", Mode: "serve", Access: "tailnet", Options: tunnelProviderOptions{ListenerPort: target.Port, Path: target.Path}, Inputs: tunnelInputRefs{Socket: target.Socket, NodeID: target.NodeID, DNSName: target.DNSName}}
	if err := c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
		t.Fatal(err)
	}
	return c
}

func namedTargetStart(t *testing.T, c *Context) (*tailscaleFakeDaemon, error) {
	t.Helper()
	daemon := tailscaleSyntheticDaemon(`{}`)
	p := &tunnelProviderRuntime{c: c, Local: daemon.transport(t, tailscaleSyntheticTarget())}
	opt := TunnelOptions{Service: "web", Name: "preview"}
	for _, action := range []string{"register", "start"} {
		if err := c.runManagedTunnel(context.Background(), opt, action, p); err != nil {
			return daemon, err
		}
	}
	return daemon, nil
}

// Named tunnel actions plan with namedTunnelPlan. A target declared through
// port_env or url_env resolves from the collection env, exactly as the
// rendered runtime resolves it.
func TestNamedTunnelTargetResolvesFromCollectionEnv(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, env, want string
	}{
		{"literal url", "{url: 'http://127.0.0.1:3000'}", "", "http://127.0.0.1:3000"},
		{"port_env", "{port_env: WIDGET_PORT}", "WIDGET_PORT=4173\n", "http://127.0.0.1:4173"},
		{"url_env", "{url_env: WIDGET_URL}", "WIDGET_URL=http://localhost:5173\n", "http://localhost:5173"},
		{"literal url wins over port_env", "{url: 'http://127.0.0.1:3000', port_env: WIDGET_PORT}", "WIDGET_PORT=4173\n", "http://127.0.0.1:3000"},
		{"url_env wins over literal url", "{url: 'http://127.0.0.1:3000', url_env: WIDGET_URL}", "WIDGET_URL=http://127.0.0.1:5173\n", "http://127.0.0.1:5173"},
		{"url_env wins over port_env", "{url_env: WIDGET_URL, port_env: WIDGET_PORT}", "WIDGET_URL=http://127.0.0.1:5173\nWIDGET_PORT=4173\n", "http://127.0.0.1:5173"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := namedTargetFixture(t, tc.metadata, tc.env)
			if _, err := c.renderRuntime(); err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[{"path":"widget/web/server","state":"ready"}]}`, 0600)
			daemon, err := namedTargetStart(t, c)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(daemon.config, `"Proxy":"`+tc.want+`"`) {
				t.Fatalf("served target is not %s: %s", tc.want, daemon.config)
			}
		})
	}
}

// The definition approved at render time and the one checked before provider
// use must be the same, or the provider refuses with "definition changed".
func TestNamedTunnelTargetDefinitionMatchesRenderedPlan(t *testing.T) {
	c := namedTargetFixture(t, "{port_env: WIDGET_PORT}", "WIDGET_PORT=4173\n")
	binary, err := c.runtimeBinary()
	if err != nil {
		t.Fatal(err)
	}
	rendered, _, err := c.runtimePlan(binary)
	if err != nil {
		t.Fatal(err)
	}
	action, err := c.namedTunnelPlan()
	if err != nil {
		t.Fatal(err)
	}
	digests := []string{}
	for _, plan := range []*runtimeManifest{rendered, action} {
		entries, err := c.effectiveTunnels(plan)
		if err != nil || len(entries) != 1 {
			t.Fatal(entries, err)
		}
		d, err := c.tunnelDefinitionFor(entries[0], plan)
		if err != nil {
			t.Fatal(err)
		}
		if d.Target != "http://127.0.0.1:4173" {
			t.Fatal("target", d.Target)
		}
		digest, _ := d.digest()
		digests = append(digests, digest)
	}
	if digests[0] != digests[1] {
		t.Fatal("render and action definitions differ")
	}
}

// Unset, malformed and non-loopback values never reach a provider.
func TestNamedTunnelTargetRefusesUnusableEnvValues(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, env, want string
	}{
		{"port_env unset", "{port_env: WIDGET_PORT}", "", "WIDGET_PORT"},
		{"port_env not a number", "{port_env: WIDGET_PORT}", "WIDGET_PORT=http\n", "WIDGET_PORT"},
		{"port_env too long", "{port_env: WIDGET_PORT}", "WIDGET_PORT=123456\n", "WIDGET_PORT"},
		{"url_env unset", "{url_env: WIDGET_URL}", "", "WIDGET_URL"},
		{"url_env with credentials", "{url_env: WIDGET_URL}", "WIDGET_URL=http://user:pw@127.0.0.1:5173\n", "without credentials"},
		{"url_env not loopback", "{url_env: WIDGET_URL}", "WIDGET_URL=http://192.0.2.10:5173\n", "loopback"},
		{"neither declared", "{mark: w}", "", "explicit local URL/port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := namedTargetFixture(t, tc.metadata, tc.env)
			daemon, err := namedTargetStart(t, c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error mentioning %q, got %v", tc.want, err)
			}
			if len(daemon.journal) != 0 {
				t.Fatal("unusable target reached the provider", daemon.journal)
			}
		})
	}
}

// The other providers read the same definition. ngrok registration is enough
// to show the target resolved before any provider request.
func TestNamedTunnelTargetPortEnvIsProviderIndependent(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {port_env: WIDGET_PORT}\n", 0644)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "WIDGET_PORT=4173\n", 0600)
	scope, err := c.ValidateControlRoot()
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(scope.Root, "provider/manage.token"), "synthetic-management", 0600)
	fixtureFile(t, filepath.Join(scope.Root, "provider/cleanup.token"), "synthetic-cleanup", 0600)
	entry := tunnelPolicyEntry{Provider: "ngrok", Mode: "domain", Access: "public", Options: tunnelProviderOptions{Domain: "web.example.test", Reserve: true}, Inputs: tunnelInputRefs{ManagementFile: "provider/manage.token", CleanupFile: "provider/cleanup.token"}}
	if err = c.SetTunnelPolicy("widget", "web", "preview", entry, false); err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := &tunnelProviderRuntime{c: c, HTTP: tunnelContextTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct{ Domain, Metadata string }
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Fatal("unexpected provider request", r.Method)
		}
		body := fmt.Sprintf(`{"id":"rd_synthetic","domain":%q,"metadata":%q}`, request.Domain, request.Metadata)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	if err = c.runManagedTunnel(context.Background(), TunnelOptions{Service: "web", Name: "preview", Public: true}, "register", p); err != nil {
		t.Fatal(err)
	}
	state, err := c.readTunnelJournal()
	if err != nil || len(state.Entries) != 1 || calls != 1 || state.Entries[0].Operation.Lifecycle.Definition.Target != "http://127.0.0.1:4173" {
		t.Fatal("registration did not use the port_env target", calls, err)
	}
}

// Discovery, dry-run render and recorded status still never source the
// collection env: it is executable receiver code.
func TestNamedTunnelTargetLeavesReadOnlyPlansUnresolved(t *testing.T) {
	c := namedTargetFixture(t, "{port_env: WIDGET_PORT, url_env: WIDGET_URL}", "")
	marker := filepath.Join(c.Collection, "forbidden")
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "touch "+marker+"\nWIDGET_PORT=4173\n", 0600)
	plan, _, err := c.runtimePlanWithTunnels("dekit", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.RenderRuntime(true); err != nil {
		t.Fatal(err)
	}
	c.recordedTunnelItems(c.runtimeDeclarations())
	for _, task := range plan.Tasks {
		if task.URL != "" {
			t.Fatal("read-only plan resolved a URL", task.URL)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("read-only plan sourced the collection env")
	}
}

// A receiving collection's start_with_service entry starts with `svc up`. Its
// target comes from the collection's own env files, through the receiving
// root checks, and reaches only the synthetic LocalAPI socket.
func TestNamedTunnelTargetPortEnvStartsWithServiceInReceivingCollection(t *testing.T) {
	for _, name := range []string{"WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT", "WTC_PROJECT_ROOT", "TABA_PROJECT_ROOT"} {
		t.Setenv(name, "")
	}
	owner := newWorkspaceFixture(t)
	socket, daemon := acceptanceSocket(t)
	if err := owner.SetTunnelPolicy("widget", "web", "preview", acceptancePolicy(socket, "/preview"), true); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(owner.Workspace, "source-widget")
	fixtureFile(t, filepath.Join(source, ".harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  autostart: true\n  x-wtc: {port_env: WIDGET_PORT}\n", 0644)
	fixtureGit(t, "-C", source, "add", "-A")
	fixtureGit(t, "-C", source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic app")
	made, err := owner.NewCollection(NewOptions{Slug: "receiving", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := OpenCollection(made.Collection)
	if err != nil {
		t.Fatal(err)
	}
	acceptanceRunner(t, c)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "WIDGET_PORT=4173\n", 0600)
	if _, err = c.RuntimeAction("up", "widget/web", 0); err != nil {
		t.Fatal(err)
	}
	config, calls := daemon.snapshot()
	if calls == 0 || !strings.Contains(config, `"Proxy":"http://127.0.0.1:4173"`) {
		t.Fatalf("start_with_service did not serve the port_env target: %s", config)
	}
}

// svc up reads the collection env for a named tunnel only when it is about to
// start one. Without a wanted entry it sources the env exactly as often as a
// collection with no tunnel policy at all.
func TestNamedTunnelTargetServiceUpWithoutWantedTunnelAddsNoEnvRead(t *testing.T) {
	sourced := func(c *Context) int {
		t.Helper()
		counter := filepath.Join(c.Collection, "sourced")
		fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "echo x >> "+counter+"\nWIDGET_PORT=4173\n", 0600)
		if _, err := c.RuntimeAction("up", "widget/web", 0); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(counter)
		return strings.Count(string(data), "x")
	}
	baseline := runtimeFixture(t)
	fixtureFile(t, filepath.Join(baseline.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n web/server:\n  cmd: [echo, synthetic]\n  x-wtc: {port_env: WIDGET_PORT}\n", 0644)
	want := sourced(baseline)
	unwanted := namedTargetFixture(t, "{port_env: WIDGET_PORT}", "")
	if got := sourced(unwanted); want == 0 || got != want {
		t.Fatalf("env sourced %d times with an unwanted named tunnel, %d without policy", got, want)
	}
}

// A plan that never read the env cannot know whether a variable is set, so it
// gives no advice about one.
func TestNamedTunnelTargetUnresolvedPlanGivesNoEnvAdvice(t *testing.T) {
	for _, metadata := range []string{"{port_env: WIDGET_PORT}", "{url_env: WIDGET_URL}"} {
		c := namedTargetFixture(t, metadata, "WIDGET_PORT=4173\nWIDGET_URL=http://127.0.0.1:5173\n")
		plan, _, err := c.runtimePlanWithTunnels("dekit", false)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := c.effectiveTunnels(plan)
		if err != nil || len(entries) != 1 {
			t.Fatal(entries, err)
		}
		if _, err = c.tunnelDefinitionFor(entries[0], plan); err == nil || err.Error() != "named tunnel service needs an explicit local URL/port" {
			t.Fatalf("%s: unresolved plan gave env advice: %v", metadata, err)
		}
	}
}
