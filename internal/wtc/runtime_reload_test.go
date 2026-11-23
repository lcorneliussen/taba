package wtc

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runtimeReloadFake is a dekit stand-in that records its calls. A legacy
// client has no reload command; runner status names the runner's binary.
func runtimeReloadFake(t *testing.T, root string, legacy bool, runner, reloadError string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "dekit")
	t.Setenv("RUNTIME_TEST_LEGACY", map[bool]string{true: "1"}[legacy])
	t.Setenv("RUNTIME_TEST_RUNNER", runner)
	t.Setenv("RUNTIME_TEST_RELOAD_ERROR", reloadError)
	fixtureFile(t, binary, `#!/bin/sh
root=$2
shift 3
printf '%s\n' "$*" >> "$root/calls"
case "$*" in
reload*)
 if [ -n "$RUNTIME_TEST_LEGACY" ]; then
  echo 'Error: unknown command or unsupported file: `+"`reload`"+` (expected a subcommand or a .js script)' >&2
  exit 1
 fi
 if [ "$*" = 'reload --yes' ] && [ -n "$RUNTIME_TEST_RELOAD_ERROR" ]; then
  printf '%s\n' "$RUNTIME_TEST_RELOAD_ERROR" >&2
  exit 1
 fi ;;
'runner status')
 printf '{"status":"running","version":"0.10.3","binary":"%s"}\n' "$RUNTIME_TEST_RUNNER"
 exit 0 ;;
esac
echo '{}'
`, 0755)
	return binary
}

func runtimeReloadCalls(t *testing.T, root string) string {
	t.Helper()
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(calls)
}

func TestRuntimeReloadConfigLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name, reloadError string
		legacy, other     bool
		fails, restart    bool
	}{
		{name: "native reload"},
		{name: "native reload against another runner binary", other: true},
		{name: "legacy client and its own runner", legacy: true, restart: true},
		{name: "legacy client and another runner binary", legacy: true, other: true, fails: true},
		{name: "invalid configuration", reloadError: "invalid task configuration", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			root := c.runtimeRoot()
			runner := filepath.Join(root, "dekit")
			if tc.other {
				runner = filepath.Join(t.TempDir(), "dekit")
				fixtureFile(t, runner, "#!/bin/sh\n", 0755)
			}
			binary := runtimeReloadFake(t, root, tc.legacy, runner, tc.reloadError)
			err := runtimeReloadConfig(&runtimeManifest{Root: root, Binary: binary})
			if (err != nil) != tc.fails {
				t.Fatalf("reload error = %v", err)
			}
			calls := runtimeReloadCalls(t, root)
			if strings.Contains(calls, "runner restart") != tc.restart {
				t.Fatalf("unexpected legacy restart: %s", calls)
			}
			if tc.legacy && tc.other {
				if !strings.Contains(err.Error(), "not applied") || !strings.Contains(err.Error(), "0.10.3") || strings.Contains(err.Error(), root) {
					t.Fatalf("diagnosis is not actionable or leaks a path: %v", err)
				}
			}
		})
	}
}

// The check runs before rendering writes the new config or removes a task:
// a refusal must leave nothing for the next render to mistake as applied.
func TestRuntimeReloadPreflight(t *testing.T) {
	for _, tc := range []struct {
		name          string
		legacy, other bool
		fails         bool
	}{
		{name: "native reload"},
		{name: "legacy client and its own runner", legacy: true},
		{name: "legacy client and another runner binary", legacy: true, other: true, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			root := c.runtimeRoot()
			runner := filepath.Join(root, "dekit")
			if tc.other {
				runner = filepath.Join(t.TempDir(), "dekit")
				fixtureFile(t, runner, "#!/bin/sh\n", 0755)
			}
			binary := runtimeReloadFake(t, root, tc.legacy, runner, "")
			err := runtimeReloadPreflight(&runtimeManifest{Root: root, Binary: binary})
			if (err != nil) != tc.fails {
				t.Fatalf("preflight error = %v", err)
			}
			calls := runtimeReloadCalls(t, root)
			// Only read-only probes: no --yes, no restart of runner or tasks.
			for _, call := range strings.Split(strings.TrimSpace(calls), "\n") {
				if call != "reload" && call != "runner status" {
					t.Fatalf("preflight changed state: %q", calls)
				}
			}
		})
	}
}

// A runner started by a newer dekit keeps running after the harness pins the
// older, reload-less client. Restarting that runner imports nothing, so the
// render must refuse before it writes, leave the running task untouched, and
// let down then up take the change.
//
//	WTC_TEST_DEKIT=/abs/dekit-0.10.0 WTC_TEST_DEKIT_RELOAD=/abs/dekit-0.10.3 \
//	  go test ./internal/wtc -run TestDekitRuntimeReloadRunnerMismatch -v
func TestDekitRuntimeReloadRunnerMismatch(t *testing.T) {
	legacy, current := os.Getenv("WTC_TEST_DEKIT"), os.Getenv("WTC_TEST_DEKIT_RELOAD")
	if legacy == "" || current == "" {
		t.Skip("set WTC_TEST_DEKIT to dekit 0.10.0 and WTC_TEST_DEKIT_RELOAD to a dekit with reload")
	}
	var err error
	if legacy, err = filepath.Abs(legacy); err != nil {
		t.Fatal(err)
	}
	if current, err = filepath.Abs(current); err != nil {
		t.Fatal(err)
	}
	c := runtimeFixture(t)
	t.Cleanup(func() {
		if err := c.RetireRuntime(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	fragment := filepath.Join(c.Collection, "widget/.harness/dekit.tasks.yaml")
	define := func(marker string) {
		fixtureFile(t, fragment, fmt.Sprintf(`tasks:
 web/server:
  cmd: [sh, -c, 'echo %s; echo SERVER_READY; exec sleep 60']
  autostart: true
  ready: {log: SERVER_READY, timeout: 3s}
`, marker), 0644)
	}
	logged := func() string {
		t.Helper()
		path, err := c.RuntimeLogPath("widget/web/server")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	generated := func() string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(c.runtimeRoot(), "dekit.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256(data))
	}

	c.Config.Runtime.Binary = current
	define("VERSION_ONE")
	if _, err := c.RuntimeAction("up", "widget/web", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	before := generated()

	c.Config.Runtime.Binary = legacy
	define("VERSION_TWO")
	_, err = c.RuntimeAction("up", "widget/web", 5*time.Second)
	if err == nil {
		t.Fatalf("render reported success although the runner kept the old definition; log: %q", logged())
	}
	if !strings.Contains(err.Error(), "not applied") {
		t.Fatalf("unexpected error: %v", err)
	}
	if generated() != before {
		t.Fatal("refused render still wrote the generated config")
	}
	if log := logged(); strings.Contains(log, "VERSION_TWO") || strings.Count(log, "SERVER_READY") != 1 {
		t.Fatalf("refused render bounced or changed the running task: %q", log)
	}

	if _, err := c.RuntimeAction("down", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RuntimeAction("up", "widget/web", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if log := logged(); !strings.Contains(log, "VERSION_TWO") {
		t.Fatalf("down then up did not take the new definition: %q", log)
	}
}
