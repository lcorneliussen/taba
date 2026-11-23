package wtc

import (
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ports handed to runtime tasks come from below the kernels' ephemeral ranges
// (Linux 32768-60999 by default, macOS/BSD 49152-65535). A port picked with
// listen(":0") and released goes back into that range, where a concurrently
// running test process's listen(":0") or outgoing connect can be given it
// before the task binds it; the task then exits with "address already in use".
const runtimePortLow, runtimePortHigh = 20000, 32000

var runtimePortsTaken = struct {
	sync.Mutex
	ports map[int]bool
}{ports: map[int]bool{}}

func runtimeFreePort(t *testing.T) int {
	t.Helper()
	if data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range"); err == nil {
		if fields := strings.Fields(string(data)); len(fields) == 2 {
			if low, err := strconv.Atoi(fields[0]); err == nil && low < runtimePortHigh {
				t.Fatalf("ephemeral ports start at %d, inside the runtime test range %d-%d", low, runtimePortLow, runtimePortHigh)
			}
		}
	}
	runtimePortsTaken.Lock()
	defer runtimePortsTaken.Unlock()
	for range 500 {
		port := runtimePortLow + rand.IntN(runtimePortHigh-runtimePortLow)
		if runtimePortsTaken.ports[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		listener.Close()
		runtimePortsTaken.ports[port] = true
		return port
	}
	t.Fatalf("no free loopback port in %d-%d", runtimePortLow, runtimePortHigh)
	return 0
}

// runtimeTaskLogs returns each task's log, so a failed start shows why the
// task exited.
func runtimeTaskLogs(c *Context, paths ...string) string {
	var out strings.Builder
	for _, path := range paths {
		log, err := c.RuntimeLogPath(path)
		if err != nil {
			fmt.Fprintf(&out, "%s: no log: %v\n", path, err)
			continue
		}
		data, err := os.ReadFile(log)
		if err != nil {
			fmt.Fprintf(&out, "%s: no log: %v\n", path, err)
			continue
		}
		fmt.Fprintf(&out, "%s log:\n%s\n", path, data)
	}
	return out.String()
}
func runtimeGet(t *testing.T, port int) string {
	t.Helper()
	client := http.Client{Timeout: time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
func runtimePortClosed(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("port %d survived shutdown", port)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// CI sets WTC_TEST_REQUIRE_RUNTIME so a missing tool fails instead of skipping.
func runtimeToolsSkip(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("WTC_TEST_REQUIRE_RUNTIME") != "" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}

func TestDekitRuntimeConfigReload(t *testing.T) {
	binary := os.Getenv("WTC_TEST_DEKIT")
	if binary == "" {
		runtimeToolsSkip(t, "set WTC_TEST_DEKIT to run the native configuration reload contract")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	c := runtimeFixture(t)
	c.Config.Runtime.Binary = binary
	t.Cleanup(func() {
		if err := c.RetireRuntime(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	fragment := filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml")
	definition := func(version string, extra bool) string {
		body := fmt.Sprintf(`tasks:
 web/server:
  cmd: [sh, -c, 'echo %s; echo SERVER_READY; exec sleep 60']
  autostart: true
  ready: {log: SERVER_READY, timeout: 3s}
`, version)
		if extra {
			body += ` web/extra:
  cmd: [sh, -c, 'echo EXTRA_READY; exec sleep 60']
  autostart: true
  ready: {log: EXTRA_READY, timeout: 3s}
`
		}
		return body
	}
	up := func(body string, wantExtra bool) {
		t.Helper()
		fixtureFile(t, fragment, body, 0644)
		if _, err := c.RuntimeAction("up", "widget/web", 5*time.Second); err != nil {
			t.Fatal(err)
		}
		m, err := c.runtimeManifest()
		if err != nil || m == nil {
			t.Fatalf("runtime manifest: %v", err)
		}
		data, err := runtimeCommand(m, false, "ls")
		if err != nil {
			t.Fatal(err)
		}
		var native struct {
			Tasks []RuntimeItem `json:"tasks"`
		}
		if err := json.Unmarshal(data, &native); err != nil {
			t.Fatal(err)
		}
		seen := map[string]string{}
		for _, item := range native.Tasks {
			seen[item.Path] = item.State
		}
		if seen["widget/web/server"] != "ready" || (seen["widget/web/extra"] == "ready") != wantExtra {
			t.Fatalf("unexpected tasks after importing configuration: %+v", seen)
		}
		if !wantExtra && seen["widget/web/extra"] != "" {
			t.Fatalf("removed task remains in runner: %+v", seen)
		}
	}
	up(definition("VERSION_ONE", false), false)
	up(definition("VERSION_ONE", true), true)
	up(definition("VERSION_TWO", true), true)
	logPath, err := c.RuntimeLogPath("widget/web/server")
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(log), "VERSION_TWO") {
		t.Fatalf("running task did not take its new definition: %q, %v", log, err)
	}
	up(definition("VERSION_TWO", false), false)
	if _, err := c.RuntimeAction("down", "", 0); err != nil {
		t.Fatal(err)
	}
	up(definition("VERSION_TWO", true), true)
}

// Run explicitly against the pinned upstream release, without replacing a host
// installation: WTC_TEST_DEKIT=/absolute/path/to/dekit go test ./internal/wtc -run TestDekitRuntimeIntegration -v
func TestDekitRuntimeIntegration(t *testing.T) {
	binary := os.Getenv("WTC_TEST_DEKIT")
	if binary == "" {
		runtimeToolsSkip(t, "set WTC_TEST_DEKIT to the pinned dekit 0.10.0 binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	owner := newWorkspaceFixture(t)
	shared := filepath.Join(owner.Workspace, "shared-resource")
	fixtureFile(t, shared, "external synthetic resource", 0600)
	var collections []*Context
	var ports [][2]int
	for _, name := range []string{"runtime-one", "runtime-two"} {
		result, err := owner.NewCollection(NewOptions{Slug: name, Repos: []string{"widget"}})
		if err != nil {
			t.Fatal(err)
		}
		c, err := OpenCollection(result.Collection)
		if err != nil {
			t.Fatal(err)
		}
		c.Config.Runtime.Backend = "dekit"
		c.Config.Runtime.Binary = binary
		collections = append(collections, c)
		t.Cleanup(func() {
			if err := c.RetireRuntime(); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		})
		port, relay := runtimeFreePort(t), runtimeFreePort(t)
		ports = append(ports, [2]int{port, relay})
		app := filepath.Join(c.Collection, "widget")
		for _, file := range []string{"dev.py", "server.py", "relay.py"} {
			data, err := os.ReadFile(filepath.Join("testdata/runtime", file))
			if err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, filepath.Join(app, file), string(data), 0644)
		}
		for _, file := range []string{"runtime-provision.sh", "runtime-teardown.sh"} {
			data, err := os.ReadFile(filepath.Join("testdata/runtime", file))
			if err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, filepath.Join(app, ".harness", file), string(data), 0755)
		}
		// These values represent generated collection ports and endpoint metadata.
		fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), fmt.Sprintf("PORT=%d\nRELAY_PORT=%d\nWEB_URL='http://127.0.0.1:%d'\nRELAY_URL='http://127.0.0.1:%d'\n", port, relay, port, relay), 0600)
		fragment := fmt.Sprintf(`tasks:
 web/server:
  cmd: [%q, dev.py]
  autostart: true
  ready: {log: WEB_READY, timeout: 3s}
  x-wtc: {url_env: WEB_URL}
 web/tunnel:
  cmd: [%q, relay.py]
  deps: [web/server]
  autostart: true
  tags: [endpoint]
  ready: {log: ENDPOINT_READY, timeout: 3s}
  x-wtc: {url_env: RELAY_URL}
`, python, python)
		fixtureFile(t, filepath.Join(app, ".harness/dekit.tasks.yaml"), fragment, 0644)
		if items := c.RuntimeStatus(); len(items) != 2 || items[0].Path != "widget/web/server" || items[1].Path != "widget/web/tunnel" || items[0].State != "unconfigured" || items[1].State != "unconfigured" {
			t.Fatal("missing pre-start declarations", items)
		}
		if _, err := os.Stat(c.runtimeRoot()); !os.IsNotExist(err) {
			t.Fatal("status unexpectedly created runtime", err)
		}
		if _, err := c.RuntimeAction("up", "", 5*time.Second); err != nil {
			t.Fatalf("%v\n%s", err, runtimeTaskLogs(c, "widget/web/server", "widget/web/tunnel"))
		}
		if got := runtimeGet(t, port); got != name {
			t.Fatal(got, name)
		}
		if got := runtimeGet(t, relay); got != name {
			t.Fatal(got, name)
		}
		items := c.RuntimeStatus()
		if len(items) != 3 || items[0].State != "ready" || items[1].Kind != "endpoint" || items[1].URL != fmt.Sprintf("http://127.0.0.1:%d", relay) {
			t.Fatal(items)
		}
	}
	one, two := collections[0], collections[1]
	fixtureFile(t, filepath.Join(one.Collection, "widget/.runtime-data/database"), "persistent fixture data", 0600)
	// CLI launch processes have already exited; both detached runners still serve.
	if _, err := one.RuntimeAction("restart", "widget/web", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := runtimeGet(t, ports[1][1]); got != "runtime-two" {
		t.Fatal(got)
	}
	logPath, err := one.RuntimeLogPath("widget/web/server")
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(log), "BUILD cached") {
		t.Fatalf("cached build and persistent log missing: %q %v", log, err)
	}
	childData, err := os.ReadFile(filepath.Join(one.Collection, "widget/child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(string(childData))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := one.RuntimeAction("down", "widget/web", 0); err != nil {
		t.Fatal(err)
	}
	runtimePortClosed(t, ports[0][0])
	runtimePortClosed(t, ports[0][1])
	deadline := time.Now().Add(3 * time.Second)
	for unix.Kill(child, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("nested child survived group stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := runtimeGet(t, ports[1][0]); got != "runtime-two" {
		t.Fatal(got)
	}
	if _, err := one.RuntimeAction("up", "widget/web", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := one.RuntimeAction("down", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := one.RuntimeLogPath("widget/web/server"); err != nil {
		t.Fatal("logs lost after down", err)
	}
	if _, err := os.Stat(filepath.Join(one.Collection, "widget/.runtime-data/database")); err != nil {
		t.Fatal("down removed data", err)
	}
	persisted, _ := os.ReadFile(filepath.Join(one.Collection, "widget/.runtime-data/database"))
	if string(persisted) != "persistent fixture data" {
		t.Fatal("idempotent provision lost data")
	}
	// Changed definitions after down must replace dekit's saved snapshot.
	fragment := filepath.Join(one.Collection, "widget/.harness/dekit.tasks.yaml")
	data, _ := os.ReadFile(fragment)
	fixtureFile(t, fragment, strings.ReplaceAll(string(data), "ENDPOINT_READY", "NEVER_READY"), 0644)
	if _, err := one.RuntimeAction("up", "", 5*time.Second); err == nil {
		t.Fatal("readiness failure reported success")
	}
	found := false
	for _, item := range one.RuntimeStatus() {
		if item.Path == "widget/web/tunnel" && item.State == "exited" {
			found = true
		}
	}
	if !found {
		t.Fatal("failed startup is not visible", one.RuntimeStatus())
	}
	// A teardown failure must preserve real worktrees and its ownership receipt.
	local := filepath.Join(one.Collection, ".env.collection.local")
	baseEnv, _ := os.ReadFile(local)
	fixtureFile(t, local, string(baseEnv)+"RUNTIME_TEARDOWN_FAIL=1\n", 0600)
	if _, err := owner.RetireCollection(RetireOptions{Name: "runtime-one", Force: true}); err == nil {
		t.Fatal("retirement ignored resource teardown failure")
	}
	if _, err := os.Stat(filepath.Join(one.Collection, "widget/.runtime-data/owner")); err != nil {
		t.Fatal("retirement removed receipt on failure", err)
	}
	fixtureFile(t, local, string(baseEnv), 0600)
	// Retirement is scoped, including saved runner state; second stays alive.
	if err := one.RetireRuntime(); err != nil {
		t.Fatal(err)
	}
	runtimePortClosed(t, ports[0][0])
	runtimePortClosed(t, ports[0][1])
	if got := runtimeGet(t, ports[1][1]); got != "runtime-two" {
		t.Fatal(got)
	}
	if err := two.RetireRuntime(); err != nil {
		t.Fatal(err)
	}
	runtimePortClosed(t, ports[1][0])
	runtimePortClosed(t, ports[1][1])
	// Existing local command has a useful fallback with no WTC/dekit environment.
	cmd := exec.Command(python, "server.py")
	cmd.Dir = filepath.Join(one.Collection, "widget")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "PORT=0"}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	line := make([]byte, 100)
	n, err := stdout.Read(line)
	if err != nil || !strings.Contains(string(line[:n]), "WEB_READY port=") {
		t.Fatalf("standalone failed: %q %v", line[:n], err)
	}
	standalonePort, err := strconv.Atoi(strings.TrimSpace(strings.Split(string(line[:n]), "port=")[1]))
	if err != nil {
		t.Fatal(err)
	}
	if got := runtimeGet(t, standalonePort); got != "standalone" {
		t.Fatal(got)
	}
	cmd.Process.Kill()
	cmd.Wait()
	for _, name := range []string{"runtime-one", "runtime-two"} {
		result, err := owner.RetireCollection(RetireOptions{Name: name, Force: true})
		if err != nil || !result.FolderRemoved {
			t.Fatalf("verified retirement failed: %+v %v", result, err)
		}
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatal("removed shared resource", err)
	}
}

// A tunnel whose URL exists only after startup: each client output shape is
// discovered from the task log, serves through that URL, and a tunnel restart
// replaces it. Loopback only.
func TestDekitTunnelURLDiscovery(t *testing.T) {
	binary := os.Getenv("WTC_TEST_DEKIT")
	if binary == "" {
		runtimeToolsSkip(t, "set WTC_TEST_DEKIT to the pinned dekit 0.10.0 binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	owner := newWorkspaceFixture(t)
	for style, pattern := range map[string]string{
		"cloudflared": `INF \|\s+(http://127\.0\.0\.1:\d+)\s+\|`,
		"ngrok":       `msg="started tunnel".* url=(\S+)`,
		"devtunnel":   `Connect via browser: (\S+)`,
	} {
		t.Run(style, func(t *testing.T) {
			result, err := owner.NewCollection(NewOptions{Slug: "tunnel-" + style, Repos: []string{"widget"}})
			if err != nil {
				t.Fatal(err)
			}
			c, err := OpenCollection(result.Collection)
			if err != nil {
				t.Fatal(err)
			}
			c.Config.Runtime.Backend, c.Config.Runtime.Binary = "dekit", binary
			t.Cleanup(func() {
				if err := c.RetireRuntime(); err != nil {
					t.Errorf("cleanup: %v", err)
				}
			})
			app := filepath.Join(c.Collection, "widget")
			for _, file := range []string{"dev.py", "server.py", "tunnel.py"} {
				data, err := os.ReadFile(filepath.Join("testdata/runtime", file))
				if err != nil {
					t.Fatal(err)
				}
				fixtureFile(t, filepath.Join(app, file), string(data), 0644)
			}
			fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), fmt.Sprintf("PORT=%d\nTUNNEL_STYLE=%s\nTUNNEL_WARMUP=1.5\n", runtimeFreePort(t), style), 0600)
			fixtureFile(t, filepath.Join(app, ".harness/dekit.tasks.yaml"), fmt.Sprintf(`tasks:
 web/server:
  cmd: [%q, dev.py]
  autostart: true
  ready: {log: WEB_READY, timeout: 3s}
 web/tunnel:
  cmd: [%q, tunnel.py]
  deps: [web/server]
  autostart: true
  tags: [endpoint]
  ready: {log: TUNNEL_READY, timeout: 3s}
  x-wtc: {url_log: '%s'}
`, python, python, pattern), 0644)
			endpoint := func() RuntimeItem {
				for _, item := range c.RuntimeStatus() {
					if item.Path == "widget/web/tunnel" {
						return item
					}
				}
				t.Fatal("tunnel missing from status")
				return RuntimeItem{}
			}
			through := func(url string) string {
				response, err := (&http.Client{Timeout: 5 * time.Second}).Get(url + "/")
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, _ := io.ReadAll(response.Body)
				return string(body)
			}
			// The fixture announces its URL 1.5s before it answers.
			if _, err := c.RuntimeAction("up", "", 500*time.Millisecond); err == nil || !strings.Contains(err.Error(), "endpoint URL not reachable yet") {
				t.Fatalf("up returned before the tunnel answered: %v", err)
			}
			if _, err := c.RuntimeAction("up", "", 10*time.Second); err != nil {
				t.Fatal(err)
			}
			first := endpoint()
			if first.State != "ready" || first.Kind != "endpoint" || !strings.HasPrefix(first.URL, "http://127.0.0.1:") {
				t.Fatalf("URL not discovered: %+v", first)
			}
			if got := through(first.URL); got != "tunnel-"+style {
				t.Fatalf("tunnel served %q", got)
			}
			if _, err := c.RuntimeAction("restart", "widget/web/tunnel", 10*time.Second); err != nil {
				t.Fatal(err)
			}
			second := endpoint()
			if second.URL == first.URL || second.State != "ready" || through(second.URL) != "tunnel-"+style {
				t.Fatalf("restart kept %q, now %+v", first.URL, second)
			}
			// The tunnel depends on the service, so dekit restarts it with the
			// service and a URL assigned at startup changes. Status must follow.
			if _, err := c.RuntimeAction("restart", "widget/web/server", 10*time.Second); err != nil {
				t.Fatal(err)
			}
			third := endpoint()
			if third.State != "ready" || through(third.URL) != "tunnel-"+style {
				t.Fatalf("after service restart: %+v", third)
			}
			t.Logf("%s: %s, tunnel restart %s, service restart %s", style, first.URL, second.URL, third.URL)
		})
	}
}
