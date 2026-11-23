package testenv

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// failingEnv refuses one change and records nothing else, so a test never
// alters the environment the rest of the package runs in.
type failingEnv struct {
	failSet, failUnset string
}

func (f failingEnv) Setenv(name, _ string) error {
	if name == f.failSet {
		return errors.New("refused")
	}
	return nil
}

func (f failingEnv) Unsetenv(name string) error {
	if name == f.failUnset {
		return errors.New("refused")
	}
	return nil
}

func TestIsolateFailsClosedWhenTheEnvironmentCannotBeSet(t *testing.T) {
	for _, name := range []string{"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", marker} {
		t.Run(name, func(t *testing.T) {
			tmp := privateTempDir(t)
			dir, err := isolate(failingEnv{failSet: name})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("isolate() error = %v, want an error naming %s", err, name)
			}
			if dir != "" {
				t.Errorf("isolate() dir = %q, want none", dir)
			}
			assertEmpty(t, tmp)
		})
	}
}

func TestIsolateFailsClosedWhenTheShellCannotBeScrubbed(t *testing.T) {
	t.Setenv("TABA_FROM_SHELL", "leak")
	tmp := privateTempDir(t)
	if _, err := isolate(failingEnv{failUnset: "TABA_FROM_SHELL"}); err == nil || !strings.Contains(err.Error(), "TABA_FROM_SHELL") {
		t.Fatalf("isolate() error = %v, want an error naming TABA_FROM_SHELL", err)
	}
	assertEmpty(t, tmp)
}

// privateTempDir gives isolate a temporary directory no other test binary
// writes to, so whatever it leaves behind is its own.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("isolate() left %s behind", entry.Name())
	}
}
