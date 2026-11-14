package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestRuntimeTunnelRecognisesProvidersFromTheCommand(t *testing.T) {
	c := runtimeFixture(t)
	fragment := filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml")
	data, _ := os.ReadFile(fragment)
	tunnel := func(cmd, extra string) runtimeTask {
		t.Helper()
		fixtureFile(t, fragment, strings.Replace(string(data), " web/tunnel:\n  cmd: [sh, -c, 'sleep 60']\n", " web/tunnel:\n  cmd: "+cmd+"\n"+extra, 1), 0644)
		m, _, err := c.runtimePlan("dekit")
		if err != nil {
			t.Fatal(err)
		}
		return m.Tasks[1]
	}
	for _, tc := range []struct {
		cmd, extra, provider, exposure, basis string
	}{
		{`[sh, -c, 'exec tailscale serve --https=8443 http://127.0.0.1:3000']`, "", "tailscale", ExposureTailnet, ExposureCommand},
		{`[tailscale, funnel, "3000"]`, "", "tailscale", ExposurePublic, ExposureCommand},
		{`[cloudflared, tunnel, --url, "http://127.0.0.1:3000"]`, "", "cloudflare", ExposurePublic, ExposureCommand},
		{`[cloudflared, tunnel, run, preview]`, "", "cloudflare", "", ""},
		{`[ngrok, http, "3000"]`, "", "ngrok", ExposurePublic, ExposureCommand},
		{`[ngrok, http, "3000", --oauth=google]`, "", "ngrok", ExposureProtected, ExposureCommand},
		{`[devtunnel, host, -p, "3000"]`, "", "devtunnel", ExposureAccount, ExposureCommand},
		{`[devtunnel, host, -p, "3000", --allow-anonymous]`, "", "devtunnel", ExposurePublic, ExposureCommand},
		// A declaration beats the command; an explicit provider needs no recognisable command.
		{`[ngrok, http, "3000"]`, "  x-wtc: {public: false}\n", "ngrok", ExposureProtected, ExposureDeclared},
		{`[sh, -c, 'sleep 60']`, "  x-wtc: {tunnel: devtunnel}\n", "devtunnel", "", ""},
		{`[sh, -c, 'sleep 60']`, "", "", "", ""},
	} {
		task := tunnel(tc.cmd, tc.extra)
		if task.Provider != tc.provider || task.Exposure != tc.exposure || task.ExposureBasis != tc.basis {
			t.Errorf("%s %s: got %q %q %q, want %q %q %q", tc.cmd, tc.extra, task.Provider, task.Exposure, task.ExposureBasis, tc.provider, tc.exposure, tc.basis)
		}
		// A known client brings its own URL pattern.
		if p, ok := runtimeTunnelProviderNamed(tc.provider); ok && task.URLLog != p.urlLog {
			t.Errorf("%s: url_log = %q", tc.cmd, task.URLLog)
		}
	}
	fixtureFile(t, fragment, string(data)+"  x-wtc: {tunnel: bore}\n", 0644)
	if _, _, err := c.runtimePlan("dekit"); err == nil || !strings.Contains(err.Error(), "tailscale, cloudflare, ngrok, devtunnel") {
		t.Fatalf("accepted an unknown provider: %v", err)
	}
}

func TestRuntimeTunnelURLPatternsFindProviderAddresses(t *testing.T) {
	for name, line := range map[string]string{
		"tailscale":  "Available within your tailnet:\n\nhttps://workstation.example.ts.net:8443/\n|-- proxy http://127.0.0.1:3000",
		"cloudflare": "INF |  https://calm-river-example.trycloudflare.com                                |",
		"ngrok":      `t=2026-10-14 lvl=info msg="started tunnel" obj=tunnels name=command_line addr=http://localhost:3000 url=https://ab12-example.ngrok-free.app`,
		"devtunnel":  "Connect via browser: https://abc123x-3000.euw.devtunnels.ms",
	} {
		p, _ := runtimeTunnelProviderNamed(name)
		log := filepath.Join(t.TempDir(), "task.log")
		fixtureFile(t, log, line+"\n", 0600)
		got := runtimeLogURL(log, p.urlLog)
		if got == "" || RuntimeTunnelProvider(got) != name {
			t.Errorf("%s: found %q", name, got)
		}
	}
}

func TestRuntimeTunnelExposurePrecedence(t *testing.T) {
	served := []byte(`{"Foreground":{"x":{"TCP":{"8443":{"HTTPS":true}},"Web":{"workstation.example.ts.net:8443":{}}}}}`)
	funnel := []byte(`{"Web":{"workstation.example.ts.net:443":{}},"AllowFunnel":{"workstation.example.ts.net:443":true}}`)
	if got := runtimeTailscaleExposure(served, "https://workstation.example.ts.net:8443"); got != ExposureTailnet {
		t.Fatalf("served address = %q", got)
	}
	if got := runtimeTailscaleExposure(funnel, "https://workstation.example.ts.net"); got != ExposurePublic {
		t.Fatalf("funnel address = %q", got)
	}
	if got := runtimeTailscaleExposure(served, "https://other.example.ts.net:8443"); got != "" {
		t.Fatalf("unserved address = %q", got)
	}
	check := func(data []byte) func(string) string {
		return func(raw string) string { return runtimeTailscaleExposure(data, raw) }
	}
	for _, tc := range []struct {
		item            RuntimeItem
		check           func(string) string
		exposure, basis string
	}{
		// The live check overrides a declaration in either direction.
		{RuntimeItem{Kind: "endpoint", URL: "https://workstation.example.ts.net", Exposure: ExposureProtected, ExposureBasis: ExposureDeclared}, check(funnel), ExposurePublic, ExposureVerified},
		{RuntimeItem{Kind: "endpoint", URL: "https://workstation.example.ts.net:8443"}, check(served), ExposureTailnet, ExposureVerified},
		// Without an answer from Tailscale the address alone says nothing.
		{RuntimeItem{Kind: "endpoint", URL: "https://workstation.example.ts.net:8443"}, check(nil), ExposureUnknown, ""},
		{RuntimeItem{Kind: "endpoint", URL: "https://workstation.example.ts.net:8443", Exposure: ExposureTailnet, ExposureBasis: ExposureCommand}, check(nil), ExposureTailnet, ExposureCommand},
		{RuntimeItem{Kind: "endpoint", URL: "https://calm-river.trycloudflare.com"}, nil, ExposurePublic, ExposureDefault},
		{RuntimeItem{Kind: "endpoint", URL: "https://preview.example.com", Provider: "cloudflare"}, nil, ExposureUnknown, ""},
		{RuntimeItem{Kind: "endpoint", URL: "https://abc-3000.euw.devtunnels.ms"}, nil, ExposureAccount, ExposureDefault},
		{RuntimeItem{Kind: "endpoint", URL: "https://preview.example.com"}, nil, ExposureUnknown, ""},
	} {
		item := tc.item
		runtimeTunnelExposure(&item, tc.check)
		if item.Exposure != tc.exposure || item.ExposureBasis != tc.basis {
			t.Errorf("%s: got %q %q, want %q %q", tc.item.URL, item.Exposure, item.ExposureBasis, tc.exposure, tc.basis)
		}
	}
}

func TestRuntimeEnlistedIncludesDeclarationsWithoutTheRunner(t *testing.T) {
	c := runtimeFixture(t)
	if got := c.RuntimeEnlisted(); len(got) != 2 || got[0].State != "unconfigured" {
		t.Fatalf("missing declarations before wtc up: %+v", got)
	}
	if _, err := c.RuntimeAction("up", "", 0); err != nil {
		t.Fatal(err)
	}
	got := c.RuntimeEnlisted()
	if len(got) != 2 || got[0].Path != "widget/web/server" || got[1].Path != "widget/web/tunnel" {
		t.Fatalf("enlisted = %+v", got)
	}
	for _, item := range got {
		if item.State != "checking" {
			t.Fatalf("%s state = %q, want checking", item.Path, item.State)
		}
	}
}

func TestServiceAddTunnelAndRemove(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "ECHO_PORT=43001\n", 0600)
	if _, err := c.AddService(ServiceOptions{Service: "echo", PortEnv: "ECHO_PORT", Cmd: []string{"python3", "-m", "http.server"}, NoStart: true}); err != nil {
		t.Fatal(err)
	}
	local := c.runtimeLocalFragment("widget")
	data, _ := os.ReadFile(local)
	if !strings.Contains(string(data), "echo/main:") || !strings.Contains(string(data), "port_env: ECHO_PORT") {
		t.Fatalf("local task file:\n%s", data)
	}
	shared, _ := os.ReadFile(runtimeSharedFragment(filepath.Join(c.Collection, "widget")))
	if strings.Contains(string(shared), "echo") {
		t.Fatal("add without --shared changed the repository's task file")
	}
	if _, err := c.AddService(ServiceOptions{Service: "echo", Cmd: []string{"true"}, NoStart: true}); err == nil {
		t.Fatal("enlisted the same service twice")
	}
	m, _, err := c.runtimePlan("dekit")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range m.Tasks {
		if task.Path == "widget/echo/main" && (task.URL != "" || task.Source != local) {
			// The fake runtime binary is not absolute, so nothing resolves.
			t.Fatalf("echo: %+v", task)
		}
	}
	// A provider without desired policy is refused before implicit setup or login.
	if _, err := c.Tunnel(TunnelOptions{Service: "echo", Provider: "cloudflare"}); err == nil || !strings.Contains(err.Error(), "configure receiving-local tunnels.toml") {
		t.Fatalf("implicit provider setup was accepted: %v", err)
	}
	if data, _ := os.ReadFile(local); strings.Contains(string(data), "cloudflare") {
		t.Fatal("a refused tunnel was written")
	}
	if _, err := c.Tunnel(TunnelOptions{Service: "echo", Provider: "tailscale", Public: true}); err == nil {
		t.Fatal("made a tailscale tunnel public")
	}
	// The shared web service is the repository's: rm needs --shared.
	if _, err := c.RemoveService("widget/web", false); err == nil || !strings.Contains(err.Error(), "--shared") {
		t.Fatalf("removed a committed service without --shared: %v", err)
	}
	if _, err := c.RemoveService("echo", false); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(local); strings.Contains(string(data), "echo") {
		t.Fatalf("echo still enlisted:\n%s", data)
	}
}

func TestRuntimeDeclaredResourcesShowByName(t *testing.T) {
	c := runtimeFixture(t)
	fragment := filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml")
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, string(data)+"resources:\n  database: {type: postgres}\n  cache: {type: redis, mark: k}\n", 0644)
	m, _, err := c.runtimePlan("dekit")
	if err != nil {
		t.Fatal(err)
	}
	m.Resources = []runtimeResource{{Dir: "widget", Repo: "widget", State: "provision-hook-completed"}}
	items := c.runtimeResourceItems(m, false)
	if len(items) != 2 || items[0].Group != "widget/cache" || items[0].Mark != "k" || items[1].Type != "postgres" || items[1].State != "provision-hook-completed" {
		t.Fatalf("resources: %+v", items)
	}
	m.Resources = nil
	if items := c.runtimeResourceItems(m, false); len(items) != 2 || items[0].State != "unprovisioned" {
		t.Fatalf("declared but not provisioned: %+v", items)
	}
	fixtureFile(t, fragment, string(data)+"resources:\n  Database: {}\n", 0644)
	if _, _, err := c.runtimePlan("dekit"); err == nil {
		t.Fatal("accepted an invalid resource name")
	}
}

func TestRuntimeResourceOnlyChangeReachesTheManifest(t *testing.T) {
	c := runtimeFixture(t)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	fragment := filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml")
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, string(data)+"resources:\n  database: {type: postgres}\n", 0644)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	if m, _ := c.runtimeManifest(); len(m.Declared) != 1 || m.Declared[0].Type != "postgres" {
		t.Fatalf("declared resources not saved: %+v", m.Declared)
	}
	if c.ServicesChangedAt().IsZero() {
		t.Fatal("rendering changed services without telling a status view")
	}
}

func TestNoServicesSignalWithoutServices(t *testing.T) {
	c := runtimeFixture(t)
	if err := c.RetireRuntime(); err != nil {
		t.Fatal(err)
	}
	if !c.ServicesChangedAt().IsZero() {
		t.Fatal("retiring a collection without services left a signal file")
	}
}

func TestServiceExplicitTunnelDeclarationsAndRemovingTheLastService(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "ECHO_PORT=43001\n", 0600)
	if _, err := c.AddService(ServiceOptions{Service: "echo", PortEnv: "ECHO_PORT", Cmd: []string{"python3", "-m", "http.server"}, NoStart: true}); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[]}`, 0600)
	for _, tc := range []struct {
		provider string
		public   bool
		cmd      string
		exposure string
	}{
		{"tailscale", false, `exec tailscale serve --https="$ECHO_PORT" "http://127.0.0.1:$ECHO_PORT"`, ExposureTailnet},
		{"devtunnel", false, `exec devtunnel host -p "$ECHO_PORT"`, ExposureAccount},
		{"cloudflare", true, `exec cloudflared tunnel --no-autoupdate --url "http://127.0.0.1:$ECHO_PORT"`, ExposurePublic},
		{"ngrok", true, `exec ngrok http "$ECHO_PORT" --log stdout`, ExposurePublic},
	} {
		// Compatibility is for explicit receiving declarations. The API no
		// longer invents a command that can use a global provider login.
		if err := c.runtimeChange(c.runtimeLocalFragment("widget"), func(tasks *yaml.Node) error {
			return runtimeAddTask(tasks, "echo/"+tc.provider, map[string]any{"cmd": []string{"sh", "-c", tc.cmd}, "deps": []string{"echo/main"}, "tags": []string{"endpoint"}, "x-wtc": map[string]any{"tunnel": tc.provider}})
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Tunnel(TunnelOptions{Service: "echo", Provider: tc.provider, Public: tc.public}); err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		m, _, err := c.runtimePlan("dekit")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, task := range m.Tasks {
			if task.Path == "widget/echo/"+tc.provider {
				found = true
				if task.Kind != "endpoint" || task.Provider != tc.provider || task.Exposure != tc.exposure || task.Autostart || task.Deps[0] != "widget/echo/main" {
					t.Errorf("%s: %+v", tc.provider, task)
				}
			}
		}
		data, _ := os.ReadFile(c.runtimeLocalFragment("widget"))
		if !found || !strings.Contains(string(data), tc.cmd) {
			t.Fatalf("%s tunnel not written:\n%s", tc.provider, data)
		}
		calls, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "calls"))
		if !strings.Contains(string(calls), "start widget/echo/"+tc.provider) {
			t.Fatalf("%s tunnel not started:\n%s", tc.provider, calls)
		}
	}
	// The committed web service goes with --shared; echo, the last one, after.
	if _, err := c.RemoveService("widget/web", true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(runtimeSharedFragment(filepath.Join(c.Collection, "widget"))); strings.Contains(string(data), "web/") {
		t.Fatalf("web still in the committed file:\n%s", data)
	}
	m, _ := c.runtimeManifest()
	m.Resources = []runtimeResource{{Dir: "widget", Repo: "widget", State: "provision-hook-completed"}}
	if err := runtimeSaveManifest(m); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoveService("echo", false); err != nil {
		t.Fatal(err)
	}
	m, _ = c.runtimeManifest()
	if len(m.Tasks) != 0 || len(m.Resources) != 1 {
		t.Fatalf("after removing the last service: tasks %d, resources %+v", len(m.Tasks), m.Resources)
	}
	for _, item := range c.RuntimeStatus() {
		if item.Kind != "resources" {
			t.Fatalf("removed service still listed: %+v", item)
		}
	}
}

func TestRemovingTheOnlyServiceBeforeAnyUp(t *testing.T) {
	c := runtimeFixture(t)
	if _, err := c.RemoveService("widget/web", true); err != nil {
		t.Fatal(err)
	}
	if m, _ := c.runtimeManifest(); m != nil {
		t.Fatal("removing a service created a runtime")
	}
}

func TestServiceUpAndRestartLeaveAStoppedPublicTunnelClosed(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "ECHO_PORT=43001\n", 0600)
	if _, err := c.AddService(ServiceOptions{Service: "echo", PortEnv: "ECHO_PORT", Cmd: []string{"python3", "-m", "http.server"}, NoStart: true}); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[]}`, 0600)
	// This existing explicit temporary endpoint keeps declaration dispatch.
	if err := c.runtimeChange(c.runtimeLocalFragment("widget"), func(tasks *yaml.Node) error {
		return runtimeAddTask(tasks, "echo/cloudflare", map[string]any{"cmd": []string{"sh", "-c", `exec cloudflared tunnel --no-autoupdate --url "http://127.0.0.1:$ECHO_PORT"`}, "deps": []string{"echo/main"}, "tags": []string{"endpoint"}, "x-wtc": map[string]any{"tunnel": "cloudflare"}})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tunnel(TunnelOptions{Service: "echo", Provider: "cloudflare", Public: true}); err != nil {
		t.Fatal(err)
	}
	// Explicit stop records off, so service actions must leave the public tunnel closed.
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[{"path":"widget/echo/cloudflare","state":"idle"}]}`, 0600)
	if _, err := c.Tunnel(TunnelOptions{Service: "echo", Provider: "cloudflare", Stop: true}); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[]}`, 0600)
	for _, action := range []string{"up", "restart"} {
		os.Remove(filepath.Join(c.runtimeRoot(), "calls"))
		if _, err := c.RuntimeAction(action, "echo", 0); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		calls, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "calls"))
		verb := map[string]string{"up": "start", "restart": "restart"}[action]
		if !strings.Contains(string(calls), verb+" widget/echo/main\n") || strings.Contains(string(calls), "cloudflare") || strings.Contains(string(calls), "/**") {
			t.Fatalf("%s echo reached its public tunnel:\n%s", action, calls)
		}
	}
	// An untargeted restart restarts what runs: here web's server, whose
	// running tunnel follows it, and never echo's closed public tunnel.
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[{"path":"widget/web/server","state":"ready"},{"path":"widget/web/tunnel","state":"ready"},{"path":"widget/echo/cloudflare","state":"idle"}]}`, 0600)
	os.Remove(filepath.Join(c.runtimeRoot(), "calls"))
	if _, err := c.RuntimeAction("restart", "", 0); err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "calls")); !strings.Contains(string(calls), "restart widget/web/server\n") ||
		strings.Contains(string(calls), "restart widget/web/tunnel") || strings.Contains(string(calls), "cloudflare") || strings.Contains(string(calls), "**") {
		t.Fatalf("untargeted restart reached what was not running:\n%s", calls)
	}
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[]}`, 0600)
	if _, err := c.RuntimeAction("restart", "", 0); err == nil || !strings.Contains(err.Error(), "nothing is running") {
		t.Fatalf("restart with nothing running: %v", err)
	}
	// The committed web service starts its private autostart tunnel with it.
	data, _ := os.ReadFile(runtimeSharedFragment(filepath.Join(c.Collection, "widget")))
	fixtureFile(t, runtimeSharedFragment(filepath.Join(c.Collection, "widget")), strings.Replace(string(data), "  tags: [endpoint]\n", "  tags: [endpoint]\n  autostart: true\n  x-wtc: {public: false}\n", 1), 0644)
	os.Remove(filepath.Join(c.runtimeRoot(), "calls"))
	if _, err := c.RuntimeAction("up", "web", 0); err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "calls")); !strings.Contains(string(calls), "start widget/web/tunnel\n") {
		t.Fatalf("private autostart tunnel not started with its service:\n%s", calls)
	}
}

func TestServiceAddReadyTimeoutAndUnresolvedVariables(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "ECHO_PORT=43001\n", 0600)
	if _, err := c.AddService(ServiceOptions{Service: "echo", PortEnv: "ECHO_PORT", ReadyTimeout: 5 * time.Minute, Cmd: []string{"python3", "-m", "http.server"}, NoStart: true}); err != nil {
		t.Fatal(err)
	}
	binary, _ := c.runtimeBinary()
	_, data, err := c.runtimePlan(binary)
	if err != nil {
		t.Fatal(err)
	}
	var native struct {
		Tasks map[string]map[string]any `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	if ready, _ := native.Tasks["widget/echo/main"]["ready"].(map[string]any); ready["tcp"] != "127.0.0.1:43001" || ready["timeout"] != "300s" {
		t.Fatalf("port probe lost the timeout: %v", native.Tasks["widget/echo/main"])
	}
	before, _ := os.ReadFile(c.runtimeLocalFragment("widget"))
	if _, err := c.AddService(ServiceOptions{Service: "api", URLEnv: "API_URL_MISSING", Cmd: []string{"true"}, NoStart: true}); err == nil {
		t.Fatal("enlisted a service whose URL variable does not resolve")
	}
	if after, _ := os.ReadFile(c.runtimeLocalFragment("widget")); string(after) != string(before) {
		t.Fatalf("refused service left in the task file:\n%s", after)
	}
}

func TestRuntimeTextFiltersControlCharacters(t *testing.T) {
	text := RuntimeText([]RuntimeItem{{Collection: "c", Group: "widget/web", Path: "widget/web/main", Kind: "service", State: "failed",
		Label: "web\x1b]8;;https://evil.invalid\x07", Reason: "bad\x1b[2J", Error: "line\rover"}})
	if strings.ContainsAny(text, "\x1b\x07\r") {
		t.Fatalf("control characters reached the terminal: %q", text)
	}
}
