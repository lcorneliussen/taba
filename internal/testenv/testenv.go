// Package testenv runs every test package in the environment CI gives it,
// whatever shell runs `go test`.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// marker names the directory of an isolated test process. A test binary that
// a test starts as a child inherits it and keeps the environment it was given.
const marker = "TABA_TEST_ENV"

// gitConfig is the only Git configuration tests read: no system file, and a
// global file with a fixture identity, so commits work on any machine.
const gitConfig = "[user]\n\tname = fixture\n\temail = fixture@example.invalid\n"

// Main is the TestMain of every test package. It removes what a developer's
// shell adds to CI's environment, runs setup, then the tests, and exits.
// setup holds a package's own fixtures and returns their cleanup.
func Main(m *testing.M, setup ...func() (cleanup func())) {
	os.Exit(run(m, setup))
}

func run(m *testing.M, setup []func() func()) int {
	if os.Getenv(marker) == "" {
		dir, err := isolate()
		if err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			return 1
		}
		defer os.RemoveAll(dir)
	}
	for _, fixture := range setup {
		cleanup := fixture()
		defer cleanup()
	}
	return m.Run()
}

func isolate() (string, error) {
	for _, setting := range os.Environ() {
		if name, _, _ := strings.Cut(setting, "="); fromShell(name) {
			os.Unsetenv(name)
		}
	}
	dir, err := os.MkdirTemp("", "taba-testenv-")
	if err != nil {
		return "", err
	}
	config := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(config, []byte(gitConfig), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_CONFIG_GLOBAL", config)
	os.Setenv(marker, dir)
	return dir, nil
}

// knobs are the WTC_ and TABA_ names a test reads on purpose. New knobs use
// the WTC_TEST_ or TABA_TEST_ prefix; the rest predate this package.
var knobs = []string{"WTC_TEST_", "TABA_TEST_", "WTC_OPENCLAW_", "WTC_RECORDING_",
	"WTC_HOSTED435_FRAME_DIR", "WTC_TS_AUTHKEY", "WTC_PUBLISH_GH_TOKEN"}

// fromShell reports whether a variable is a product or Git setting from the
// caller's shell. A collection shell carries WTC_ and TABA_ settings, and
// agents and editors set Git variables such as GIT_EDITOR; CI sets neither.
// Git tracing passes through for tools/ci/test-timings.py --git-trace.
func fromShell(name string) bool {
	if strings.HasPrefix(name, "GIT_") {
		return !strings.HasPrefix(name, "GIT_TRACE")
	}
	if !strings.HasPrefix(name, "WTC_") && !strings.HasPrefix(name, "TABA_") {
		return false
	}
	for _, knob := range knobs {
		if strings.HasPrefix(name, knob) {
			return false
		}
	}
	return true
}
