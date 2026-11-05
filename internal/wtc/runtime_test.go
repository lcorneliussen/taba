package wtc

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func runtimeFixture(t *testing.T) *Context {
	t.Helper()
	// Never this machine's tunnel clients or network: status looks at both.
	login, answer := runtimeLoginCheck, runtimeAnswerCheck
	t.Cleanup(func() { runtimeLoginCheck, runtimeAnswerCheck = login, answer })
	runtimeLoginCheck = func(string) string { return "" }
	runtimeAnswerCheck = func(string) (bool, string) { return true, "" }
	c := fixture(t)
	fixtureGit(t, "-C", c.Harness, "init", "-q", "-b", "main")
	fixtureGit(t, "-C", c.Harness, "remote", "add", "origin", "https://example.invalid/harness-example.git")
	app := filepath.Join(c.Collection, "widget")
	fixtureFile(t, filepath.Join(app, "README.md"), "fixture\n", 0644)
	fixtureGit(t, "-C", app, "init", "-q", "-b", "main")
	c.Config.Runtime.Backend = "dekit"
	binary := filepath.Join(t.TempDir(), "dekit")
	fixtureFile(t, binary, `#!/bin/sh
if [ "$1" = --version ]; then echo 'dekit 0.10.0'; exit; fi
root=$2
shift 3
printf '%s\n' "$*" >> "$root/calls"
case "$1 $2" in
 'runner status') if [ -f "$root/running" ]; then echo '{"status":"running"}'; else echo '{"status":"absent"}'; fi ;;
 'runner stop') if [ -f "$root/running" ] || [ -f "$root/saved" ]; then rm -f "$root/running" "$root/saved"; echo '{}'; else echo 'Runner is not running.' >&2; exit 1; fi ;;
 'ls ') cat "$root/fake-tasks.json" ;;
 'down ') rm -f "$root/running"; touch "$root/saved"; echo '{}' ;;
 *) touch "$root/running"; printf '%s' "$RUNTIME_TEST_VALUE" > "$root/env-seen"; echo '{"matched":1}' ;;
esac
`, 0755)
	c.Config.Runtime.Binary = binary
	fixtureFile(t, filepath.Join(app, ".harness/dekit.tasks.yaml"), `tasks:
 web/server:
  cmd: [sh, -c, 'sleep 60']
  autostart: true
  ready: {log: READY}
 web/tunnel:
  cmd: [sh, -c, 'sleep 60']
  deps: [web/server]
  tags: [endpoint]
`, 0644)
	return c
}

func TestRuntimeReloadConfigLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		legacy, fails bool
	}{
		{name: "native reload"},
		{name: "legacy command", message: "unknown command or unsupported file: `reload` (expected a subcommand or a .js script)", legacy: true},
		{name: "invalid configuration", message: "invalid task configuration", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "dekit")
			t.Setenv("RUNTIME_TEST_RELOAD_ERROR", tc.message)
			fixtureFile(t, binary, `#!/bin/sh
root=$2
shift 3
printf '%s\n' "$*" >> "$root/calls"
if [ "$*" = 'reload --yes' ] && [ -n "$RUNTIME_TEST_RELOAD_ERROR" ]; then
 printf '%s\n' "$RUNTIME_TEST_RELOAD_ERROR" >&2
 exit 1
fi
echo '{}'
`, 0755)
			err := runtimeReloadConfig(&runtimeManifest{Root: root, Binary: binary})
			if (err != nil) != tc.fails {
				t.Fatalf("reload error = %v", err)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "runner restart") != tc.legacy {
				t.Fatalf("unexpected legacy restart: %s", calls)
			}
		})
	}
}

func TestRuntimeOptInPlanAndReadOnlyStatus(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "touch should-not-run\n", 0600)
	if items := c.RuntimeStatus(); len(items) != 2 || items[0].State != "unconfigured" {
		t.Fatal(items)
	}
	if _, err := os.Stat(c.runtimeRoot()); !os.IsNotExist(err) {
		t.Fatal("status created runtime")
	}
	m, data, err := c.runtimePlan("dekit")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tasks) != 2 || m.Tasks[1].Kind != "endpoint" || m.Tasks[1].Deps[0] != "widget/web/server" {
		t.Fatalf("bad ownership: %+v", m.Tasks)
	}
	var native struct {
		Tasks map[string]map[string]any `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	cwd, _ := filepath.EvalSymlinks(filepath.Join(c.Collection, "widget"))
	if native.Tasks["widget/web/server"]["cwd"] != cwd {
		t.Fatal("cwd was not rebased")
	}
	if _, err := os.Stat(filepath.Join(c.Collection, "should-not-run")); !os.IsNotExist(err) {
		t.Fatal("read-only operation sourced env")
	}
	c.Config.Runtime.Backend = ""
	if _, _, err := c.runtimePlan("dekit"); err == nil {
		t.Fatal("accepted unconfigured backend")
	}
}

func TestRuntimeRejectsUnsafeDefinitions(t *testing.T) {
	for _, fragment := range []string{
		"tasks: {../bad: {cmd: [true]}}\n",
		"tasks: {web: {cmd: [true], deps: [host::db]}}\n",
		"tasks: {web: {cmd: [true], deps: [missing]}}\n",
		"tasks: {web: {cmd: [true], cwd: ../..}}\n",
		"tasks: {web: {cmd: [true]}}\non_init: up\n",
		"tasks: {}\n---\ntasks: {}\n",
		"tasks: {web: {cmd: [true], x-wtc: {url: 'https://user:password@example.invalid'}}}\n",
	} {
		t.Run(fragment, func(t *testing.T) {
			c := runtimeFixture(t)
			fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), fragment, 0644)
			if _, _, err := c.runtimePlan("dekit"); err == nil {
				t.Fatal("accepted unsafe config")
			}
		})
	}
	c := runtimeFixture(t)
	if err := os.Symlink(t.TempDir(), c.runtimeRoot()); err == nil {
		t.Fatal("unexpected parent directory")
	}
	if err := os.MkdirAll(filepath.Dir(c.runtimeRoot()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), c.runtimeRoot()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenderRuntime(false); err == nil {
		t.Fatal("accepted symlink runtime")
	}
}

func TestRuntimeLifecycleHooksAndSavedConfig(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "RUNTIME_TEST_VALUE='scoped value'\n", 0600)
	app := filepath.Join(c.Collection, "widget/.harness")
	fixtureFile(t, filepath.Join(app, "runtime-provision.sh"), "#!/bin/sh\necho provision\nexit 7\n", 0755)
	fixtureFile(t, filepath.Join(app, "runtime-teardown.sh"), "#!/bin/sh\necho teardown\nexit 9\n", 0755)
	if _, err := c.RuntimeAction("up", "", 0); err == nil {
		t.Fatal("ignored provisioning failure")
	}
	if _, err := os.Stat(filepath.Join(c.runtimeRoot(), "running")); !os.IsNotExist(err) {
		t.Fatal("started after failed provision")
	}
	m, err := c.runtimeManifest()
	if err != nil || m == nil || len(m.Resources) != 1 || m.Resources[0].State != "provision-attempted" {
		t.Fatalf("lost partial resource ownership: %+v %v", m, err)
	}
	if err := c.RetireRuntime(); err == nil {
		t.Fatal("ignored teardown failure")
	}
	if _, err := os.Stat(filepath.Join(c.runtimeRoot(), "manifest.json")); err != nil {
		t.Fatal("removed manifest on failure")
	}
	fixtureFile(t, filepath.Join(app, "runtime-provision.sh"), "#!/bin/sh\necho provision\n", 0755)
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[{"path":"widget/web/server","state":"ready"},{"path":"widget/web/tunnel","state":"ready"}]}`, 0600)
	if _, err := c.RuntimeAction("up", "widget/web", time.Second); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "env-seen"))
	if string(got) != "scoped value" {
		t.Fatalf("wrong env %q", got)
	}
	fragment := filepath.Join(app, "dekit.tasks.yaml")
	data, _ := os.ReadFile(fragment)
	// A changed config applies live: reload, then restart the running tasks
	// whose definition changed; a removed task is removed from the runner.
	fixtureFile(t, fragment, strings.Replace(string(data), "sleep 60", "sleep 61", 1), 0644)
	os.Remove(filepath.Join(c.runtimeRoot(), "calls"))
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "calls"))
	if !strings.Contains(string(calls), "reload --yes\nrestart widget/web/server\n") || strings.Contains(string(calls), "restart widget/web/tunnel") || strings.Contains(string(calls), "runner stop") {
		t.Fatalf("change not applied live to the changed task only:\n%s", calls)
	}
	data, _ = os.ReadFile(fragment)
	fixtureFile(t, fragment, strings.Replace(string(data), " web/tunnel:\n  cmd: [sh, -c, 'sleep 60']\n  deps: [web/server]\n  tags: [endpoint]\n", "", 1), 0644)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(filepath.Join(c.runtimeRoot(), "calls")); !strings.Contains(string(calls), "rm widget/web/tunnel\n") {
		t.Fatalf("removed task left running:\n%s", calls)
	}
	// Restored while down, the change goes through the stopped runner.
	fixtureFile(t, fragment, string(data), 0644)
	if _, err := c.RuntimeAction("down", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(filepath.Join(c.runtimeRoot(), "calls"))
	if !strings.Contains(string(calls), "runner stop") {
		t.Fatal("did not discard saved definitions")
	}
	fixtureFile(t, filepath.Join(app, "runtime-teardown.sh"), "#!/bin/sh\necho teardown\n", 0755)
	// Preserve resource ownership across changed task definitions.
	data, _ = os.ReadFile(fragment)
	fixtureFile(t, fragment, strings.ReplaceAll(string(data), "web/tunnel:", "web/endpoint:"), 0644)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	m, _ = c.runtimeManifest()
	m.Tasks = nil
	if err := runtimeSaveManifest(m); err != nil {
		t.Fatal(err)
	}
	if err := c.RetireRuntime(); err != nil {
		t.Fatal(err)
	}
	m, err = c.runtimeManifest()
	if err != nil || m == nil || len(m.Resources) != 0 {
		t.Fatalf("did not clear resource ledger: %v %v", m, err)
	}
	log, _ := os.ReadFile(filepath.Join(m.Root, "hook-logs/widget/provision.log"))
	if !strings.Contains(string(log), "provision") {
		t.Fatal("hook log unavailable")
	}
}

func TestRuntimeLogParentsAndSelectors(t *testing.T) {
	c := runtimeFixture(t)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	m, _ := c.runtimeManifest()
	for _, target := range []string{"host::db", "../widget", "widget/**", "+all", "unknown"} {
		if _, _, err := runtimeSelect(m, target); err == nil {
			t.Fatal("accepted selector", target)
		}
	}
	if selector, tasks, err := runtimeSelect(m, "widget/web"); err != nil || selector != "widget/web/**" || len(tasks) != 2 {
		t.Fatal(selector, tasks, err)
	}
	dir := filepath.Join(m.Root, "logs/widget/web")
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RuntimeLogPath("widget/web/server"); err == nil {
		t.Fatal("followed log parent symlink")
	}
}

func TestRetireRuntimeFailurePreservesWorktrees(t *testing.T) {
	c := newWorkspaceFixture(t)
	result, err := c.NewCollection(NewOptions{Slug: "runtime-failure", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := OpenCollection(result.Collection)
	if err != nil {
		t.Fatal(err)
	}
	fake := runtimeFixture(t)
	target.Config.Runtime = fake.Config.Runtime
	fixtureFile(t, filepath.Join(target.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks: {web: {cmd: [sh, -c, 'sleep 60']}}\n", 0644)
	fixtureFile(t, filepath.Join(target.Collection, "widget/.harness/runtime-teardown.sh"), "#!/bin/sh\nexit 8\n", 0755)
	if _, err := target.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	// Teardown runs only for what this collection provisioned.
	m, _ := target.runtimeManifest()
	m.Resources = []runtimeResource{{Dir: "widget", Repo: "widget", State: "provision-hook-completed"}}
	if err := runtimeSaveManifest(m); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RetireCollection(RetireOptions{Name: "runtime-failure", Force: true}); err == nil || !strings.Contains(err.Error(), "runtime cleanup") {
		t.Fatal("ignored cleanup failure", err)
	}
	for _, dir := range []string{"harness", "widget", ".wtc/runtime"} {
		if _, err := os.Stat(filepath.Join(result.Collection, dir)); err != nil {
			t.Fatal("lost state", dir, err)
		}
	}
}

func TestRuntimeStatusIncludesResourceHookFacts(t *testing.T) {
	c := runtimeFixture(t)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	m, _ := c.runtimeManifest()
	m.Resources = []runtimeResource{{Dir: "widget", Repo: "widget", State: "provision-hook-completed"}}
	if err := runtimeSaveManifest(m); err != nil {
		t.Fatal(err)
	}
	items := c.RuntimeStatus()
	if len(items) != 3 || items[2].State != "provision-hook-completed" {
		t.Fatal(items)
	}
	data, _ := json.Marshal(items)
	if strings.Contains(string(data), "scoped value") {
		t.Fatal("leaked env")
	}
}

func TestRuntimeWaitsForFiniteJobs(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks:\n migrate:\n  cmd: [true]\n  type: job\n  autostart: true\n", 0644)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	response := filepath.Join(c.runtimeRoot(), "fake-tasks.json")
	fixtureFile(t, response, `{"tasks":[{"path":"widget/migrate","state":"running"}]}`, 0600)
	if _, err := c.RuntimeAction("up", "widget/migrate", 25*time.Millisecond); err == nil {
		t.Fatal("running job counted as complete")
	}
	fixtureFile(t, response, `{"tasks":[{"path":"widget/migrate","state":"done"}]}`, 0600)
	if _, err := c.RuntimeAction("up", "widget/migrate", time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRestartWaitsForRunningDependents(t *testing.T) {
	for _, dependent := range []string{"ready: {log: READY}", "type: job"} {
		t.Run(dependent, func(t *testing.T) {
			c := runtimeFixture(t)
			fragment := "tasks:\n web/server:\n  cmd: [true]\n  ready: {log: READY}\n web/dependent:\n  cmd: [true]\n  deps: [web/server]\n  " + dependent + "\n"
			fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), fragment, 0644)
			if _, err := c.RuntimeAction("up", "", 0); err != nil {
				t.Fatal(err)
			}
			response := filepath.Join(c.runtimeRoot(), "fake-tasks.json")
			fixtureFile(t, response, `{"tasks":[{"path":"widget/web/server","state":"ready"},{"path":"widget/web/dependent","state":"running"}]}`, 0600)
			if _, err := c.RuntimeAction("restart", "widget/web/server", 25*time.Millisecond); err == nil || !strings.Contains(err.Error(), "readiness timed out") {
				t.Fatalf("running dependent counted as ready: %v", err)
			}
		})
	}
}

func TestRuntimeEndpointMetadataRerendersWithoutConfigChange(t *testing.T) {
	c := runtimeFixture(t)
	fragment := filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml")
	base, _ := os.ReadFile(fragment)
	withURL := func(url string) {
		fixtureFile(t, fragment, string(base)+"  x-wtc: {url: '"+url+"'}\n", 0644)
	}
	withURL("https://one.example.invalid")
	first, err := c.RenderRuntime(false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := c.runtimeManifest()
	withURL("https://two.example.invalid")
	second, err := c.RenderRuntime(false)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := c.runtimeManifest()
	if first.Items[1].URL != "https://one.example.invalid" || second.Items[1].URL != "https://two.example.invalid" || after.Tasks[1].URL != "https://two.example.invalid" {
		t.Fatalf("stale endpoint: %+v %+v", second.Items, after.Tasks)
	}
	if before.Digest != after.Digest {
		t.Fatal("metadata changed the generated config")
	}
}

func TestRuntimeReadinessProbeURLIsShownOnlyWhenPlain(t *testing.T) {
	for probe, want := range map[string]string{
		"http://127.0.0.1:3000/health":                 "http://127.0.0.1:3000/health",
		"http://user:password@127.0.0.1:3000/health":   "",
		"http://127.0.0.1:3000/health?token=synthetic": "",
	} {
		c := runtimeFixture(t)
		fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), "tasks: {web: {cmd: [true], ready: {http: '"+probe+"'}}}\n", 0644)
		m, data, err := c.runtimePlan("dekit")
		if err != nil {
			t.Fatal(err)
		}
		if m.Tasks[0].URL != want || !m.Tasks[0].Ready || !strings.Contains(string(data), probe) {
			t.Fatalf("probe %s shown as %q", probe, m.Tasks[0].URL)
		}
	}
}

func TestRuntimeCommandTruncatesNoisyStderr(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dekit")
	fixtureFile(t, binary, "#!/bin/sh\nhead -c 20000 /dev/zero | tr '\\0' x >&2\n[ \"$4\" = fail ] && exit 3\necho '{}'\n", 0755)
	m := &runtimeManifest{Root: t.TempDir(), Binary: binary}
	if out, err := runtimeCommand(m, false, "ls"); err != nil || strings.TrimSpace(string(out)) != "{}" {
		t.Fatalf("noisy success failed: %q %v", out, err)
	}
	if _, err := runtimeCommand(m, false, "fail"); err == nil || len(err.Error()) > 4300 || !strings.Contains(err.Error(), "xxxx") {
		t.Fatalf("failure diagnostics not truncated: %d", len(fmt.Sprint(err)))
	}
}

func TestRuntimeManifestSurvivesSymlinkedCollectionPath(t *testing.T) {
	c := runtimeFixture(t)
	fixtureFile(t, filepath.Join(c.Harness, "bin/dekit"), "#!/bin/sh\nexec "+c.Config.Runtime.Binary+" \"$@\"\n", 0755)
	c.Config.Runtime.Binary = "bin/dekit"
	fragment := filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml")
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, string(data)+" web/job:\n  script: bin/job\n  type: job\n", 0644)
	first, err := c.renderRuntime()
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(c.Workspace, alias); err != nil {
		t.Fatal(err)
	}
	other, err := OpenCollection(filepath.Join(alias, filepath.Base(c.Collection)))
	if err != nil {
		t.Fatal(err)
	}
	other.Config = c.Config
	m, err := other.runtimeManifest()
	if err != nil || m == nil || len(m.Tasks) != 3 {
		t.Fatalf("second spelling refused the runtime: %+v %v", m, err)
	}
	if again, _, err := other.runtimePlan(first.Binary); err != nil || again.Digest != first.Digest {
		t.Fatalf("second spelling changed the generated config: %v", err)
	}
	if binary, err := other.runtimeBinary(); err != nil || binary != first.Binary {
		t.Fatalf("second spelling changed the binary: %s %v", binary, err)
	}
}

// Output shapes are modelled on the real clients; the hosts are illustrative.
func TestRuntimeLogURLReadsTunnelClientOutput(t *testing.T) {
	for name, c := range map[string]struct{ pattern, log, want string }{
		"cloudflared quick tunnel": {`https://[a-z0-9-]+\.trycloudflare\.com`,
			"INF |  Your quick Tunnel has been created! Visit it at:  |\nINF |  https://calm-river-example.trycloudflare.com  |\n", "https://calm-river-example.trycloudflare.com"},
		"ngrok logfmt": {`msg="started tunnel".* url=(https://\S+)`,
			`t=2026-01-01T00:00:00+0000 lvl=info msg="started tunnel" obj=tunnels name=command_line addr=http://localhost:3000 url=https://1a2b-203-0-113-7.ngrok-free.app` + "\n", "https://1a2b-203-0-113-7.ngrok-free.app"},
		"devtunnel host": {`Connect via browser: (https://\S+)`,
			"Hosting port: 3000\nConnect via browser: https://abc123xy-3000.euw.devtunnels.ms\nInspect network activity: https://abc123xy-3000-inspect.euw.devtunnels.ms\n", "https://abc123xy-3000.euw.devtunnels.ms"},
		"tailscale serve": {`Available within your tailnet:\s+(https://\S+?)/?\s`,
			"Available within your tailnet:\n\nhttps://workstation.example-tailnet.ts.net/\n|-- proxy http://127.0.0.1:3000\n", "https://workstation.example-tailnet.ts.net"},
		"newest run wins":         {`url=(https://\S+)`, "url=https://old.example.invalid\nrestarted\nurl=https://new.example.invalid\n", "https://new.example.invalid"},
		"unsafe match is skipped": {`url=(\S+)`, "url=https://ok.example.invalid\nurl=https://user:secret@bad.example.invalid\n", "https://ok.example.invalid"},
		"no match":                {`url=(https://\S+)`, "starting\n", ""},
	} {
		path := filepath.Join(t.TempDir(), "tunnel.log")
		fixtureFile(t, path, c.log, 0600)
		if got := runtimeLogURL(path, c.pattern); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	big := filepath.Join(t.TempDir(), "big.log")
	fixtureFile(t, big, "url=https://early.example.invalid\n"+strings.Repeat("noise\n", 100000)+"url=https://late.example.invalid\n", 0600)
	if got := runtimeLogURL(big, `url=(https://\S+)`); got != "https://late.example.invalid" {
		t.Fatalf("large log: %q", got)
	}
}

func TestRuntimeStatusShowsDiscoveredURLOnlyWhenReady(t *testing.T) {
	c := runtimeFixture(t)
	fragment := filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml")
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, string(data)+"  x-wtc: {url_log: 'url=(https://\\S+)'}\n", 0644)
	if _, err := c.RuntimeAction("up", "", 0); err != nil {
		t.Fatal(err)
	}
	m, _ := c.runtimeManifest()
	tasks := filepath.Join(c.runtimeRoot(), "fake-tasks.json")
	url := func() string {
		for _, item := range c.RuntimeStatus() {
			if item.Path == "widget/web/tunnel" {
				return item.URL
			}
		}
		return "missing"
	}
	fixtureFile(t, m.Tasks[1].Log, "url=https://first.example.invalid\n", 0600)
	fixtureFile(t, tasks, `{"tasks":[{"path":"widget/web/server","state":"ready"},{"path":"widget/web/tunnel","state":"starting"}]}`, 0600)
	if got := url(); got != "" {
		t.Fatalf("showed a URL before readiness: %q", got)
	}
	fixtureFile(t, tasks, `{"tasks":[{"path":"widget/web/server","state":"ready"},{"path":"widget/web/tunnel","state":"ready"}]}`, 0600)
	if got := url(); got != "https://first.example.invalid" {
		t.Fatalf("did not discover the URL: %q", got)
	}
	fixtureFile(t, m.Tasks[1].Log, "url=https://first.example.invalid\nurl=https://second.example.invalid\n", 0600)
	if got := url(); got != "https://second.example.invalid" {
		t.Fatalf("kept the old URL after a restart: %q", got)
	}
	fixtureFile(t, fragment, string(data)+"  x-wtc: {mark: p}\n", 0644)
	if m, _, err := c.runtimePlan("dekit"); err != nil || m.Tasks[1].Mark != "p" {
		t.Fatalf("mark not recorded: %v", err)
	}
	fixtureFile(t, fragment, string(data)+"  x-wtc: {mark: web}\n", 0644)
	if _, _, err := c.runtimePlan("dekit"); err == nil {
		t.Fatal("accepted a long mark")
	}
	fixtureFile(t, fragment, string(data)+"  x-wtc: {public: true}\n", 0644)
	if m, _, err := c.runtimePlan("dekit"); err != nil || m.Tasks[1].Exposure != ExposurePublic || m.Tasks[0].Exposure != "" {
		t.Fatalf("public endpoint not recorded: %v", err)
	}
	fixtureFile(t, fragment, strings.Replace(string(data), "  ready: {log: READY}\n", "  ready: {log: READY}\n  x-wtc: {public: true}\n", 1), 0644)
	if _, _, err := c.runtimePlan("dekit"); err == nil {
		t.Fatal("accepted a public service")
	}
	fixtureFile(t, fragment, string(data)+"  x-wtc: {url_log: '(a)(b)'}\n", 0644)
	if _, _, err := c.runtimePlan("dekit"); err == nil {
		t.Fatal("accepted an ambiguous url_log")
	}
}

func TestRuntimeTeardownSkipsReposItNeverProvisioned(t *testing.T) {
	c := runtimeFixture(t)
	marker := filepath.Join(c.Collection, "torn-down")
	fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/runtime-teardown.sh"), "#!/bin/sh\ntouch '"+marker+"'\n", 0755)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	if err := c.RetireRuntime(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("teardown ran for a repo with nothing provisioned: %v", err)
	}
}

func TestRuntimeStatusRunnerStartingDiagnostics(t *testing.T) {
	for _, sweep := range []bool{false, true} {
		t.Run(fmt.Sprintf("sweep=%v", sweep), func(t *testing.T) {
			c := runtimeFixture(t)
			if _, err := c.RenderRuntime(false); err != nil {
				t.Fatal(err)
			}
			m, err := c.runtimeManifest()
			if err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, m.Binary, `#!/bin/sh
if [ "$1" = --version ]; then echo 'dekit 0.10.0'; exit; fi
shift 3
printf '%s\n' "$*" >> "$0.calls"
case "$*" in
 'runner status') echo '{"status":"starting"}' ;;
 *) exit 99 ;;
esac
`, 0755)
			fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "exit 99\n", 0600)
			m.Resources = []runtimeResource{{Dir: "widget", Repo: "widget", State: "provision-hook-completed"}}
			m.Tasks[1].TunnelIntent = "on"
			m.Tasks[1].URL = "https://example.invalid"
			if err := runtimeSaveManifest(m); err != nil {
				t.Fatal(err)
			}
			before := discoverySnapshot(t, c.Collection)
			var items []RuntimeItem
			if sweep {
				var snapshot StatusSnapshot
				c.statusRuntime(&snapshot, true)
				items = snapshot.Runtime
			} else {
				items = c.RuntimeStatus()
			}
			if len(items) != 3 {
				t.Fatalf("items = %+v", items)
			}
			for _, item := range items[:2] {
				if item.State != "checking" || !strings.Contains(item.Reason, "runner reports starting") || !strings.Contains(item.Reason, "task state is unavailable") || item.Error != "" {
					t.Errorf("runner readiness presented as task observation: %+v", item)
				}
			}
			if items[2].State != "provision-hook-completed" {
				t.Fatalf("lost resource fact: %+v", items[2])
			}
			if items[1].TunnelIntent != "on" || items[1].URL != "https://example.invalid" || items[1].Lifecycle != "" {
				t.Fatalf("lost saved tunnel facts: %+v", items[1])
			}
			encoded, err := json.Marshal(items)
			if err != nil || strings.Contains(string(encoded), "runnerStarting") || strings.Contains(string(encoded), "runner_starting") {
				t.Fatalf("published internal provenance: %s, %v", encoded, err)
			}

			calls, err := os.ReadFile(m.Binary + ".calls")
			if err != nil || string(calls) != "runner status\n" {
				t.Fatalf("unexpected backend actions %q: %v", calls, err)
			}
			if after := discoverySnapshot(t, c.Collection); !reflect.DeepEqual(before, after) {
				t.Fatal("status mutated collection state")
			}
		})
	}
}

func TestRuntimeStatusTaskObservationsAndControlPolling(t *testing.T) {
	c := runtimeFixture(t)
	if _, err := c.RenderRuntime(false); err != nil {
		t.Fatal(err)
	}
	m, err := c.runtimeManifest()
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, m.Binary, `#!/bin/sh
if [ "$1" = --version ]; then echo 'dekit 0.10.0'; exit; fi
root=$2
shift 3
printf '%s\n' "$*" >> "$0.calls"
case "$*" in
 'runner status') cat "$root/runner-response" ;;
 'ls') cat "$root/task-response" ;;
 *) exit 99 ;;
esac
`, 0755)
	for _, tc := range []struct {
		name, runner, tasks, state, reason string
		hasError                           bool
	}{
		{name: "runner starting control remains pending", runner: `{"status":"starting"}`, state: "starting"},
		{name: "observed starting", runner: `{"status":"running"}`, tasks: `{"tasks":[{"path":"widget/web/server","state":"starting","reason":"waiting for startup probe"}]}`, state: "starting", reason: "waiting for startup probe"},
		{name: "observed ready", runner: `{"status":"running"}`, tasks: `{"tasks":[{"path":"widget/web/server","state":"ready"}]}`, state: "ready"},
		{name: "observed stopped", runner: `{"status":"running"}`, tasks: `{"tasks":[{"path":"widget/web/server","state":"exited","exit_code":17,"reason":"task stopped"}]}`, state: "exited", reason: "task stopped"},
		{name: "missing task", runner: `{"status":"running"}`, tasks: `{"tasks":[]}`, state: "missing"},
		{name: "absent runner", runner: `{"status":"absent"}`, state: "absent"},
		{name: "unknown runner", runner: `{"status":"unknown"}`, state: "unknown"},
		{name: "malformed runner", runner: `{`, state: "unknown", hasError: true},
		{name: "malformed task response", runner: `{"status":"running"}`, tasks: `{`, state: "unknown", hasError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixtureFile(t, filepath.Join(m.Root, "runner-response"), tc.runner, 0600)
			fixtureFile(t, filepath.Join(m.Root, "task-response"), tc.tasks, 0600)
			raw := c.runtimeStatusOnly()
			if raw[0].State != tc.state || raw[0].Reason != tc.reason || (raw[0].Error != "") != tc.hasError {
				t.Fatalf("control observation changed: %+v", raw[0])
			}
			visible := c.RuntimeStatus()
			wantState, wantReason := tc.state, tc.reason
			if tc.name == "runner starting control remains pending" {
				wantState, wantReason = "checking", "runner reports starting; task state is unavailable"
			}
			if visible[0].State != wantState || visible[0].Reason != wantReason || (visible[0].Error != "") != tc.hasError {
				t.Fatalf("display observation = %+v", visible[0])
			}
			if tc.name == "observed stopped" && (visible[0].ExitCode == nil || *visible[0].ExitCode != 17) {
				t.Fatal("lost task exit status")
			}
		})
	}
	calls, err := os.ReadFile(m.Binary + ".calls")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if call != "runner status" && call != "ls" {
			t.Fatalf("status executed %q", call)
		}
	}
}

func TestRuntimeUpRunnerStartingWaitAndTimeout(t *testing.T) {
	for _, scenario := range []string{"ready", "timeout", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			c := runtimeFixture(t)
			fixtureFile(t, filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml"), `tasks:
 web/server:
  cmd: [sh, -c, 'sleep 60']
  autostart: true
  ready: {log: READY}
`, 0644)
			if _, err := c.RenderRuntime(false); err != nil {
				t.Fatal(err)
			}
			m, err := c.runtimeManifest()
			if err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, m.Tasks[0].Log, "synthetic startup output\n", 0600)
			t.Setenv("RUNTIME_TEST_STARTING_MODE", scenario)
			fixtureFile(t, m.Binary, `#!/bin/sh
if [ "$1" = --version ]; then echo 'dekit 0.10.0'; exit; fi
root=$2
shift 3
printf '%s\n' "$*" >> "$0.calls"
case "$*" in
 'start widget/web/server') echo '{}' ;;
 'runner status')
  if [ "$RUNTIME_TEST_STARTING_MODE" = timeout ] || [ ! -f "$0.checked" ]; then
   touch "$0.checked"
   echo '{"status":"starting"}'
  else echo '{"status":"running"}'; fi ;;
 'ls')
  if [ "$RUNTIME_TEST_STARTING_MODE" = failed ]; then
   echo '{"tasks":[{"path":"widget/web/server","state":"failed","exit_code":17}]}'
  else echo '{"tasks":[{"path":"widget/web/server","state":"ready"}]}'; fi ;;
 *) exit 99 ;;
esac
`, 0755)
			wait := 5 * time.Second
			if scenario == "timeout" {
				wait = 50 * time.Millisecond
			}
			result, err := c.RuntimeAction("up", "widget/web/server", wait)
			if scenario == "ready" {
				if err != nil || len(result.Items) != 1 || result.Items[0].State != "ready" {
					t.Fatalf("startup did not wait for task observation: %+v, %v", result, err)
				}
			} else {
				if err == nil {
					t.Fatal("unconfirmed startup succeeded")
				}
				if scenario == "timeout" && !strings.Contains(err.Error(), "timed out") {
					t.Fatalf("unexpected timeout: %v", err)
				}
				if scenario == "failed" && !strings.Contains(err.Error(), "failed") {
					t.Fatalf("unexpected failure: %v", err)
				}
				if len(result.Logs) != 1 || result.Logs[0].Tail != "synthetic startup output" {
					t.Fatalf("lost existing startup failure tail: %+v", result)
				}
			}
			calls, err := os.ReadFile(m.Binary + ".calls")
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if call != "start widget/web/server" && call != "runner status" && call != "ls" {
					t.Fatalf("unexpected backend operation: %q", call)
				}
			}
		})
	}
}
