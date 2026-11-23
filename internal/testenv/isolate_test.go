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
			before := tempDirs(t)
			dir, err := isolate(failingEnv{failSet: name})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("isolate() error = %v, want an error naming %s", err, name)
			}
			if dir != "" {
				t.Errorf("isolate() dir = %q, want none", dir)
			}
			if leaked := newEntries(before, tempDirs(t)); len(leaked) > 0 {
				t.Errorf("isolate() left %v behind", leaked)
			}
		})
	}
}

func TestIsolateFailsClosedWhenTheShellCannotBeScrubbed(t *testing.T) {
	t.Setenv("TABA_FROM_SHELL", "leak")
	before := tempDirs(t)
	if _, err := isolate(failingEnv{failUnset: "TABA_FROM_SHELL"}); err == nil || !strings.Contains(err.Error(), "TABA_FROM_SHELL") {
		t.Fatalf("isolate() error = %v, want an error naming TABA_FROM_SHELL", err)
	}
	if leaked := newEntries(before, tempDirs(t)); len(leaked) > 0 {
		t.Errorf("isolate() left %v behind", leaked)
	}
}

func tempDirs(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "taba-testenv-") {
			names[entry.Name()] = true
		}
	}
	return names
}

func newEntries(before, after map[string]bool) []string {
	var added []string
	for name := range after {
		if !before[name] {
			added = append(added, name)
		}
	}
	return added
}
