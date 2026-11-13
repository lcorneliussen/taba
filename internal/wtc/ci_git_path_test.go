package wtc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const fixtureSystemTools = "/usr/bin:/bin"

var fixtureDeveloperGit = sync.OnceValue(func() string {
	out, err := exec.Command("/usr/bin/xcrun", "--find", "git").Output()
	if err != nil {
		return ""
	}
	path, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil || !filepath.IsAbs(path) || path == "/usr/bin/git" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return ""
	}
	return path
})

// Keep fixture tools isolated from the caller's PATH. On macOS, /usr/bin/git
// launches the developer Git through a dispatcher; avoid paying that extra
// launch for every fixture command while retaining the same Git and filesystem.
func fixtureSystemPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return fixtureSystemTools
	}
	git := fixtureDeveloperGit()
	if git == "" {
		return fixtureSystemTools
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	return bin + ":" + fixtureSystemTools
}

func TestFixtureSystemPathKeepsToolIsolation(t *testing.T) {
	caller := t.TempDir()
	fixtureFile(t, filepath.Join(caller, "caller-only-fixture-tool"), "#!/bin/sh\nexit 0\n", 0755)
	t.Setenv("PATH", caller)
	path := fixtureSystemPath(t)
	if runtime.GOOS != "darwin" || fixtureDeveloperGit() == "" {
		if path != fixtureSystemTools {
			t.Fatalf("system PATH changed: %q", path)
		}
	} else {
		bin, suffix, ok := strings.Cut(path, ":")
		if !ok || suffix != fixtureSystemTools {
			t.Fatalf("system tools changed: %q", path)
		}
		entries, err := os.ReadDir(bin)
		if err != nil || len(entries) != 1 || entries[0].Name() != "git" {
			t.Fatalf("prefix must expose only Git: %v %v", entries, err)
		}
		if target, err := os.Readlink(filepath.Join(bin, "git")); err != nil || target != fixtureDeveloperGit() {
			t.Fatalf("wrong developer Git: %q %v", target, err)
		}
	}
	t.Setenv("PATH", path)
	if _, err := exec.LookPath("caller-only-fixture-tool"); err == nil {
		t.Fatal("fixture inherited a caller tool")
	}
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		t.Fatalf("fixture Git: %v %s", err, out)
	}
	if runtime.GOOS == "darwin" {
		original, err := exec.Command("/usr/bin/git", "--version").Output()
		if err != nil || string(out) != string(original) {
			t.Fatalf("Git version changed: direct=%q system=%q error=%v", out, original, err)
		}
	}
}
