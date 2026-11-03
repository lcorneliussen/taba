package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func runtimeIntentFixture(t *testing.T) *Context {
	t.Helper()
	c := runtimeFixture(t)
	data, err := os.ReadFile("testdata/runtime/intent-runner.py")
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, c.Config.Runtime.Binary, string(data), 0755)
	fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	fixtureFile(t, fragment, `tasks:
 web/server:
  cmd: [synthetic-server]
  autostart: true
 web/tunnel:
  cmd: [synthetic-provider]
  deps: [web/server]
  tags: [endpoint]
  x-wtc: {tunnel: devtunnel, public: false}
`, 0644)
	return c
}

func intentTunnel(t *testing.T, c *Context) RuntimeItem {
	t.Helper()
	for _, item := range c.RuntimeStatus() {
		if item.Path == "widget/web/tunnel" {
			return item
		}
	}
	t.Fatal("tunnel missing from status")
	return RuntimeItem{}
}

func intentAction(t *testing.T, c *Context, action, target string) {
	t.Helper()
	if _, err := c.RuntimeAction(action, target, 0); err != nil {
		t.Fatalf("%s %s: %v", action, target, err)
	}
}

func chooseTunnel(t *testing.T, c *Context, stop bool) {
	t.Helper()
	if _, err := c.Tunnel(TunnelOptions{Service: "web", Stop: stop}); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelIntentSurvivesServiceLifecycle(t *testing.T) {
	for _, target := range []string{"web", "widget/web", "widget", "", "widget/web/server"} {
		t.Run(strings.ReplaceAll(target, "/", "-"), func(t *testing.T) {
			c := runtimeIntentFixture(t)
			chooseTunnel(t, c, false)
			intentAction(t, c, "down", target)
			item := intentTunnel(t, c)
			if item.TunnelIntent != "on" || item.Lifecycle == "" {
				t.Fatalf("down lost intent: %+v", item)
			}
			intentAction(t, c, "up", target)
			if item := intentTunnel(t, c); item.State != "ready" || item.Lifecycle != "" {
				t.Fatalf("up did not restore: %+v", item)
			}
			// A missing intended tunnel is restored by restart too.
			intentAction(t, c, "down", "widget/web/tunnel")
			intentAction(t, c, "restart", target)
			if item := intentTunnel(t, c); item.State != "ready" {
				t.Fatalf("restart did not restore: %+v", item)
			}
			chooseTunnel(t, c, true)
			for _, action := range []string{"up", "restart"} {
				intentAction(t, c, action, target)
				if item := intentTunnel(t, c); item.State != "idle" || item.TunnelIntent != "off" || item.Lifecycle != "" {
					t.Fatalf("explicit off lost after %s: %+v", action, item)
				}
			}
		})
	}
}

func TestTunnelIntentIsCollectionLocalAndPreservesDefaults(t *testing.T) {
	c := runtimeIntentFixture(t)
	other := runtimeIntentFixture(t)
	chooseTunnel(t, c, false)
	intentAction(t, other, "up", "web")
	if item := intentTunnel(t, other); item.TunnelIntent != "" || item.State != "idle" {
		t.Fatalf("choice leaked collections: %+v", item)
	}
	fragment := runtimeSharedFragment(filepath.Join(other.Collection, "widget"))
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, strings.Replace(string(data), "  tags: [endpoint]", "  tags: [endpoint]\n  autostart: true", 1), 0644)
	intentAction(t, other, "up", "web")
	if item := intentTunnel(t, other); item.State != "ready" || item.TunnelIntent != "" {
		t.Fatalf("declaration default lost: %+v", item)
	}
	chooseTunnel(t, other, true)
	intentAction(t, other, "down", "")
	intentAction(t, other, "up", "")
	if item := intentTunnel(t, other); item.State != "idle" {
		t.Fatalf("off did not override autostart: %+v", item)
	}
}

func TestTunnelIntentRejectsChangedOrReusedDeclaration(t *testing.T) {
	for _, change := range []string{"provider", "exposure", "command", "removed"} {
		t.Run(change, func(t *testing.T) {
			c := runtimeIntentFixture(t)
			chooseTunnel(t, c, false)
			fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
			data, _ := os.ReadFile(fragment)
			changed := string(data)
			switch change {
			case "provider":
				changed = strings.Replace(changed, "devtunnel", "ngrok", 1)
			case "exposure":
				changed = strings.Replace(changed, "public: false", "public: true", 1)
			case "command":
				changed = strings.Replace(changed, "synthetic-provider", "other-synthetic-provider", 1)
			case "removed":
				fixtureFile(t, fragment, strings.Split(changed, " web/tunnel:")[0], 0644)
				intentAction(t, c, "up", "web")
			}
			fixtureFile(t, fragment, changed, 0644)
			intentAction(t, c, "restart", "web")
			item := intentTunnel(t, c)
			if item.State == "ready" || item.TunnelIntent != "stale" || item.Lifecycle == "" {
				t.Fatalf("stale approval reused: %+v", item)
			}
			if change != "removed" { // Explicit re-approval accepts the current declaration.
				if _, err := c.Tunnel(TunnelOptions{Service: "web", Public: true}); err != nil {
					t.Fatal(err)
				}
				if item := intentTunnel(t, c); item.State != "ready" || item.TunnelIntent != "on" {
					t.Fatalf("new choice not saved: %+v", item)
				}
			}
		})
	}
}

func TestTunnelIntentFailuresRetainChoiceAndDiagnostics(t *testing.T) {
	c := runtimeIntentFixture(t)
	chooseTunnel(t, c, false)
	intentAction(t, c, "down", "web")
	runtimeLoginCheck = func(string) string { return "synthetic login expired" }
	if _, err := c.RuntimeAction("up", "web", 0); err == nil || !strings.Contains(err.Error(), "login expired") {
		t.Fatalf("missing provider failure: %v", err)
	}
	item := intentTunnel(t, c)
	if item.TunnelIntent != "on" || item.Lifecycle == "" || item.Login == "" || item.Exposure != ExposureProtected {
		t.Fatalf("diagnostics conflated: %+v", item)
	}
	if text := RuntimeText([]RuntimeItem{item}); !strings.Contains(text, item.Lifecycle) || !strings.Contains(text, item.Login) {
		t.Fatalf("diagnostics missing: %s", text)
	}
	runtimeLoginCheck = func(string) string { return "" }
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fail-action"), "start widget/web/tunnel", 0600)
	if _, err := c.RuntimeAction("up", "web", 0); err == nil {
		t.Fatal("start failure hidden")
	}
	if item := intentTunnel(t, c); item.TunnelIntent != "on" || item.Lifecycle == "" {
		t.Fatalf("failure forgot choice: %+v", item)
	}
	os.Remove(filepath.Join(c.runtimeRoot(), "fail-action"))
	intentAction(t, c, "up", "web")
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fail-action"), "veto widget/web/tunnel", 0600)
	if _, err := c.Tunnel(TunnelOptions{Service: "web", Stop: true}); err == nil {
		t.Fatal("stop failure hidden")
	}
	m, err := c.runtimeManifest()
	if err != nil || m.TunnelIntents["widget/web/tunnel"].On {
		t.Fatalf("failed stop forgot off: %v %+v", err, m)
	}
	// The native config must suppress declaration defaults after any choice.
	var native struct {
		Tasks map[string]map[string]any `yaml:"tasks"`
	}
	data, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "dekit.yaml"))
	if err := yaml.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	if native.Tasks["widget/web/tunnel"]["autostart"] != false {
		t.Fatal("runner can bypass choice")
	}
	data, _ = json.Marshal(item)
	if !strings.Contains(string(data), `"tunnel_intent":"on"`) || !strings.Contains(string(data), `"lifecycle":`) {
		t.Fatalf("JSON omitted intent: %s", data)
	}
}

func TestTunnelIntentOffDoesNotWaitForAutostartTunnel(t *testing.T) {
	c := runtimeIntentFixture(t)
	fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, strings.Replace(string(data), "  tags: [endpoint]", "  tags: [endpoint]\n  autostart: true", 1), 0644)
	chooseTunnel(t, c, true)
	if _, err := c.RuntimeAction("up", "", time.Second); err != nil {
		t.Fatal(err)
	}
	if item := intentTunnel(t, c); item.State != "idle" {
		t.Fatalf("disabled tunnel started: %+v", item)
	}
}

func TestTunnelIntentRemovingLastServiceInvalidatesApproval(t *testing.T) {
	c := runtimeIntentFixture(t)
	fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	data, _ := os.ReadFile(fragment)
	chooseTunnel(t, c, false)
	if _, err := c.RemoveService("web", true); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, fragment, string(data), 0644)
	intentAction(t, c, "up", "web")
	if item := intentTunnel(t, c); item.State == "ready" || item.TunnelIntent != "stale" {
		t.Fatalf("removed approval reused: %+v", item)
	}
}

func TestTunnelIntentUntargetedRestartLeavesStoppedServiceOff(t *testing.T) {
	c := runtimeIntentFixture(t)
	chooseTunnel(t, c, false)
	intentAction(t, c, "down", "web")
	// Keep another service live so untargeted restart has work to do.
	fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, string(data)+" worker/main:\n  cmd: [synthetic-worker]\n", 0644)
	intentAction(t, c, "up", "worker")
	intentAction(t, c, "restart", "")
	if item := intentTunnel(t, c); item.State != "idle" || item.TunnelIntent != "on" {
		t.Fatalf("restart revived stopped service: %+v", item)
	}
	runtimeLoginCheck = func(string) string { return "synthetic login expired" }
	if _, err := c.RuntimeAction("restart", "widget/web", 0); err == nil || !strings.Contains(err.Error(), "login expired") {
		t.Fatalf("restart hid provider failure: %v", err)
	}
	if item := intentTunnel(t, c); item.State != "idle" || item.Lifecycle == "" || item.Login == "" {
		t.Fatalf("restart diagnostics missing: %+v", item)
	}
}

func TestTunnelIntentUntargetedUpRespectsStoppedNonAutostartService(t *testing.T) {
	c := runtimeIntentFixture(t)
	fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, strings.Replace(string(data), "autostart: true", "autostart: false", 1), 0644)
	chooseTunnel(t, c, false)
	intentAction(t, c, "down", "web")
	if _, err := c.RuntimeAction("up", "", time.Second); err != nil {
		t.Fatal(err)
	}
	if item := intentTunnel(t, c); item.State != "idle" || item.TunnelIntent != "on" {
		t.Fatalf("up revived stopped nonautostart service: %+v", item)
	}
	intentAction(t, c, "up", "widget/web/server")
	intentAction(t, c, "down", "widget/web/tunnel")
	intentAction(t, c, "up", "")
	if item := intentTunnel(t, c); item.State != "ready" {
		t.Fatalf("up did not restore live service's tunnel: %+v", item)
	}
}

func TestTunnelIntentUnknownRunnerRemainsUnknown(t *testing.T) {
	c := runtimeIntentFixture(t)
	chooseTunnel(t, c, false)
	for _, command := range []string{"runner status", "ls"} {
		fixtureFile(t, filepath.Join(c.runtimeRoot(), "fail-action"), command, 0600)
		item := intentTunnel(t, c)
		if item.State != "unknown" || item.Error == "" || item.Lifecycle != "" || item.TunnelIntent != "on" {
			t.Fatalf("unknown misreported as missing: %+v", item)
		}
	}
}

func TestTunnelIntentExplicitOffCannotBeBypassedByExactTarget(t *testing.T) {
	c := runtimeIntentFixture(t)
	chooseTunnel(t, c, false)
	chooseTunnel(t, c, true)
	for _, action := range []string{"up", "restart"} {
		if _, err := c.RuntimeAction(action, "widget/web/tunnel", 0); err == nil {
			t.Fatalf("%s bypassed explicit off", action)
		}
		if item := intentTunnel(t, c); item.State != "idle" || item.TunnelIntent != "off" {
			t.Fatalf("%s opened off tunnel: %+v", action, item)
		}
	}
}

func TestTunnelIntentStopAllContinuesAfterProviderFailure(t *testing.T) {
	c := runtimeIntentFixture(t)
	fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, string(data)+` web/zzz:
  cmd: [second-synthetic-provider]
  deps: [web/server]
  tags: [endpoint]
  x-wtc: {tunnel: ngrok, public: false}
`, 0644)
	for _, provider := range []string{"devtunnel", "ngrok"} {
		if _, err := c.Tunnel(TunnelOptions{Service: "web", Provider: provider}); err != nil {
			t.Fatal(err)
		}
	}
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fail-action"), "veto widget/web/tunnel", 0600)
	if err := os.Remove(c.statusSignalPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tunnel(TunnelOptions{Service: "web", Stop: true}); err == nil {
		t.Fatal("provider stop failure hidden")
	}
	if c.ServicesChangedAt().IsZero() {
		t.Fatal("partial provider stop failed to notify status")
	}
	m, err := c.runtimeManifest()
	if err != nil {
		t.Fatal(err)
	}
	for path, intent := range m.TunnelIntents {
		if intent.On {
			t.Fatalf("stop all left %s intended on", path)
		}
	}
	for _, item := range c.RuntimeStatus() {
		if item.Path == "widget/web/zzz" && item.State != "idle" {
			t.Fatalf("one failure prevented other provider stop: %+v", item)
		}
	}
}

func TestTunnelIntentStopNewDeclarationBeforeAutostart(t *testing.T) {
	c := runtimeIntentFixture(t)
	intentAction(t, c, "up", "web")
	fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, string(data)+` web/new:
  cmd: [new-synthetic-provider]
  deps: [web/server]
  autostart: true
  tags: [endpoint]
  x-wtc: {tunnel: ngrok, public: false}
`, 0644)
	if _, err := c.Tunnel(TunnelOptions{Service: "web", Provider: "ngrok", Stop: true}); err != nil {
		t.Fatal(err)
	}
	intentAction(t, c, "up", "")
	for _, item := range c.RuntimeStatus() {
		if item.Path == "widget/web/new" && (item.TunnelIntent != "off" || item.State != "idle") {
			t.Fatalf("new declaration bypassed off: %+v", item)
		}
	}
}

func TestTunnelIntentConfirmsStaleTunnelStoppedBeforeReload(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "delayed stop", true: "unconfirmed stop"}[fails], func(t *testing.T) {
			c := runtimeIntentFixture(t)
			chooseTunnel(t, c, false)
			fixtureFile(t, filepath.Join(c.runtimeRoot(), "slow-stop"), "", 0600)
			if fails {
				fixtureFile(t, filepath.Join(c.runtimeRoot(), "stop-query-fails"), "", 0600)
			}
			fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
			data, _ := os.ReadFile(fragment)
			fixtureFile(t, fragment, strings.Replace(string(data), "synthetic-provider", "changed-synthetic-provider", 1), 0644)
			config := filepath.Join(c.runtimeRoot(), "dekit.yaml")
			before, _ := os.ReadFile(config)
			_, err := c.RuntimeAction("up", "web", 0)
			if fails {
				if err == nil || !strings.Contains(err.Error(), "shutdown not confirmed") {
					t.Fatalf("unconfirmed stop ignored: %v", err)
				}
				after, _ := os.ReadFile(config)
				if string(after) != string(before) {
					t.Fatal("config changed before stop confirmation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if item := intentTunnel(t, c); item.State != "idle" || item.TunnelIntent != "stale" {
					t.Fatalf("unapproved definition started: %+v", item)
				}
			}
		})
	}
}

func TestTunnelIntentExactRestartChecksProviderLogin(t *testing.T) {
	c := runtimeIntentFixture(t)
	chooseTunnel(t, c, false)
	runtimeLoginCheck = func(string) string { return "synthetic login expired" }
	os.Remove(filepath.Join(c.runtimeRoot(), "calls"))
	if _, err := c.RuntimeAction("restart", "widget/web/tunnel", 0); err == nil || !strings.Contains(err.Error(), "login expired") {
		t.Fatalf("restart skipped provider: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "calls"))
	if strings.Contains(string(calls), "restart widget/web/tunnel") {
		t.Fatalf("restart ran before login check: %s", calls)
	}
}

func TestTunnelIntentUnknownStaleRunnerCannotReload(t *testing.T) {
	c := runtimeIntentFixture(t)
	chooseTunnel(t, c, false)
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fail-action"), "ls", 0600)
	fragment := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, strings.Replace(string(data), "synthetic-provider", "changed-synthetic-provider", 1), 0644)
	config := filepath.Join(c.runtimeRoot(), "dekit.yaml")
	before, _ := os.ReadFile(config)
	if _, err := c.RuntimeAction("up", "web", 0); err == nil || !strings.Contains(err.Error(), "shutdown not confirmed") {
		t.Fatalf("unknown stale state ignored: %v", err)
	}
	after, _ := os.ReadFile(config)
	if string(after) != string(before) {
		t.Fatal("config changed without a known stop")
	}
}
