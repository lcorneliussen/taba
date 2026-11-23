package wtc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runtimeReloadFake is a dekit stand-in that records its calls. A legacy
// client has no reload command; runner status prints status verbatim.
func runtimeReloadFake(t *testing.T, root string, legacy bool, status, reloadError string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "dekit")
	t.Setenv("RUNTIME_TEST_LEGACY", map[bool]string{true: "1"}[legacy])
	t.Setenv("RUNTIME_TEST_STATUS", status)
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
 printf '%s\n' "$RUNTIME_TEST_STATUS"
 exit 0 ;;
esac
echo '{}'
`, 0755)
	return binary
}

// runtimeReloadStatus is runner status JSON. binary "" names the pinned
// client; "other" another file. raw, when set, replaces the whole output.
type runtimeReloadStatus struct {
	binary, version, raw string
}

func (s runtimeReloadStatus) json(t *testing.T, pinned string) string {
	t.Helper()
	if s.raw != "" {
		return s.raw
	}
	binary := pinned
	if s.binary == "other" {
		binary = filepath.Join(t.TempDir(), "dekit")
		fixtureFile(t, binary, "#!/bin/sh\n", 0755)
	}
	data, err := json.Marshal(map[string]string{"status": "running", "binary": binary, "version": s.version})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runtimeReloadCalls(t *testing.T, root string) string {
	t.Helper()
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(calls)
}

var (
	runtimeReloadLegacyRunner = runtimeReloadStatus{version: "0.10.0"}
	runtimeReloadNewerRunner  = runtimeReloadStatus{binary: "other", version: "0.10.3"}
)

func TestRuntimeReloadConfigLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name, reloadError, diagnosis string
		status                       runtimeReloadStatus
		legacy, fails, restart       bool
	}{
		{name: "native reload", status: runtimeReloadNewerRunner},
		{name: "invalid configuration", status: runtimeReloadNewerRunner, reloadError: "invalid task configuration", fails: true},
		{name: "legacy client and verified legacy runner", status: runtimeReloadLegacyRunner, legacy: true, restart: true},
		{name: "legacy client and newer runner binary", status: runtimeReloadNewerRunner, legacy: true, fails: true, diagnosis: "(version 0.10.3)"},
		// The pinned file may have been replaced after the runner loaded it.
		{name: "pinned path reporting a newer runner", status: runtimeReloadStatus{version: "0.10.3"}, legacy: true, fails: true, diagnosis: "(version 0.10.3)"},
		{name: "legacy version from another binary", status: runtimeReloadStatus{binary: "other", version: "0.10.0"}, legacy: true, fails: true, diagnosis: "(version 0.10.0)"},
		{name: "unverified 0.10 patch", status: runtimeReloadStatus{version: "0.10.1"}, legacy: true, fails: true, diagnosis: "(version 0.10.1)"},
		{name: "missing version", status: runtimeReloadStatus{}, legacy: true, fails: true, diagnosis: "(unrecognized version)"},
		{name: "control bytes in version", status: runtimeReloadStatus{version: "0.10.0\x1b[2J\nforged"}, legacy: true, fails: true, diagnosis: "(unrecognized version)"},
		{name: "malformed status", status: runtimeReloadStatus{raw: "not json \x1b[2J"}, legacy: true, fails: true, diagnosis: "unreadable runner status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			root := c.runtimeRoot()
			binary := filepath.Join(root, "dekit")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			runtimeReloadFake(t, root, tc.legacy, tc.status.json(t, binary), tc.reloadError)
			err := runtimeReloadConfig(&runtimeManifest{Root: root, Binary: binary})
			if (err != nil) != tc.fails {
				t.Fatalf("reload error = %v", err)
			}
			calls := runtimeReloadCalls(t, root)
			if strings.Contains(calls, "runner restart") != tc.restart {
				t.Fatalf("unexpected legacy restart: %s", calls)
			}
			if tc.diagnosis != "" {
				message := err.Error()
				if !strings.Contains(message, "not applied") && !strings.Contains(message, "runner status") {
					t.Fatalf("diagnosis is not actionable: %v", err)
				}
				if !strings.Contains(message, tc.diagnosis) || strings.Contains(message, root) || strings.ContainsAny(message, "\x1b\n") || strings.Contains(message, "forged") {
					t.Fatalf("diagnosis %q missing, or it echoes a path or control bytes: %q", tc.diagnosis, message)
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
		status        runtimeReloadStatus
		legacy, fails bool
	}{
		{name: "native reload", status: runtimeReloadNewerRunner},
		{name: "legacy client and verified legacy runner", status: runtimeReloadLegacyRunner, legacy: true},
		{name: "legacy client and newer runner binary", status: runtimeReloadNewerRunner, legacy: true, fails: true},
		{name: "pinned path reporting a newer runner", status: runtimeReloadStatus{version: "0.10.3"}, legacy: true, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t)
			root := c.runtimeRoot()
			binary := filepath.Join(root, "dekit")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			runtimeReloadFake(t, root, tc.legacy, tc.status.json(t, binary), "")
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
