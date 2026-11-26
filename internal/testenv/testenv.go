// Package testenv runs every test package in the environment CI gives it,
// whatever shell runs `go test`. It only scrubs the caller's product,
// multiplexer and Git settings and then runs the package's fixtures, and
// leaves HOME, proxy and GO* variables as they are.
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
// setup holds a package's own fixtures, in order. Each returns a cleanup,
// run in reverse order; a cleanup error fails the run.
func Main(m *testing.M, setup ...func() (cleanup func() error)) {
	os.Exit(run(m, setup))
}

func run(m *testing.M, setup []func() func() error) (code int) {
	if os.Getenv(marker) == "" {
		dir, err := isolate(osEnv{})
		if err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			return 1
		}
		defer os.RemoveAll(dir)
	}
	for _, fixture := range setup {
		cleanup := fixture()
		defer func() {
			if err := cleanup(); err != nil {
				fmt.Fprintln(os.Stderr, "testenv:", err)
				code = 1
			}
		}()
	}
	return m.Run()
}

// env is the process environment isolate changes.
type env interface {
	Setenv(name, value string) error
	Unsetenv(name string) error
}

type osEnv struct{}

func (osEnv) Setenv(name, value string) error { return os.Setenv(name, value) }
func (osEnv) Unsetenv(name string) error      { return os.Unsetenv(name) }

// isolate fails closed: if any change to the environment fails, it removes
// its directory and returns the error, and no test runs.
func isolate(e env) (string, error) {
	for _, setting := range os.Environ() {
		if name, _, _ := strings.Cut(setting, "="); fromShell(name) {
			if err := e.Unsetenv(name); err != nil {
				return "", fmt.Errorf("unset %s: %w", name, err)
			}
		}
	}
	dir, err := os.MkdirTemp("", "taba-testenv-")
	if err != nil {
		return "", err
	}
	if err := configure(e, dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func configure(e env, dir string) error {
	config := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(config, []byte(gitConfig), 0o600); err != nil {
		return err
	}
	for _, setting := range [][2]string{
		{"GIT_CONFIG_NOSYSTEM", "1"},
		{"GIT_CONFIG_GLOBAL", config},
		{marker, dir},
	} {
		if err := e.Setenv(setting[0], setting[1]); err != nil {
			return fmt.Errorf("set %s: %w", setting[0], err)
		}
	}
	return nil
}

// knobs are the WTC_ and TABA_ names a test reads on purpose. New knobs use
// the WTC_TEST_ or TABA_TEST_ prefix; the rest predate this package.
var knobs = []string{"WTC_TEST_", "TABA_TEST_", "WTC_OPENCLAW_", "WTC_RECORDING_",
	"WTC_HOSTED435_FRAME_DIR", "WTC_TS_AUTHKEY", "WTC_PUBLISH_GH_TOKEN"}

// fromShell reports whether a variable is a product, multiplexer or Git
// setting from the caller's shell. A collection shell carries WTC_ and TABA_
// settings, a herdr or Rex pane names its server, and agents and editors set
// Git variables such as GIT_EDITOR; CI sets none of them. Git tracing passes
// through for tools/ci/test-timings.py --git-trace.
func fromShell(name string) bool {
	if strings.HasPrefix(name, "GIT_") {
		return !strings.HasPrefix(name, "GIT_TRACE")
	}
	if strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "REX_") || name == "HARNESS_HERDR_SESSION" {
		return true
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
