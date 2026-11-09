package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildVersioned(t *testing.T, out, version string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", out, ".")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, raw)
	}
}

// The pinned binary hands over to a newer dev build, runs itself when the dev
// build is older, and the dev build never hands over again.
func TestPinnedBinaryHandsOverToDevBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two binaries")
	}
	collection := t.TempDir()
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte("schema_version: 1\nrepositories: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	devDir := filepath.Join(collection, ".wtc", "dev")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		t.Fatal(err)
	}
	pinned := filepath.Join(t.TempDir(), "wtc")
	buildVersioned(t, pinned, "0.1.39")
	devVersion := "0.1.40-dev.2+dev.gabc1234"
	buildVersioned(t, filepath.Join(devDir, "wtc"), devVersion)
	writeVersion := func(v string) {
		if err := os.WriteFile(filepath.Join(devDir, "version"), []byte(v+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeVersion(devVersion)
	// Handover executes the legacy-named dev artifact but must retain the
	// invocation used by its caller for help, version and command hints.
	taba := filepath.Join(t.TempDir(), "taba")
	if err := os.Symlink(pinned, taba); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--version"}, {"svc", "add", "--help"}} {
		cmd := exec.Command(taba, args...)
		cmd.Dir = filepath.Join(collection, "harness")
		cmd.Env = append(os.Environ(), "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=")
		raw, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(raw), "taba ") || strings.Contains(string(raw), "wtc version") || strings.Contains(string(raw), "wtc svc") {
			t.Fatalf("dev handover lost invocation identity: %v %s", err, raw)
		}
	}
	run := func(env ...string) string {
		cmd := exec.Command(pinned, "--version")
		cmd.Dir = filepath.Join(collection, "harness")
		cmd.Env = append(os.Environ(), env...)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wtc --version: %v\n%s", err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	if got := run("WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD="); !strings.Contains(got, devVersion+" (dev build, in place of 0.1.39)") {
		t.Fatalf("pinned binary did not hand over: %q", got)
	}
	if got := run("WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=off"); !strings.Contains(got, "version 0.1.39") {
		t.Fatalf("WTC_DEV_BUILD=off must keep the pinned binary: %q", got)
	}
	// An ad-hoc collection (no harness repository) hands over the same way.
	adHoc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(adHoc, ".wtc", "config"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adHoc, ".wtc", "config", "repos.yml"), []byte("schema_version: 1\nrepositories: []\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(adHoc, ".wtc", "dev"), 0755); err != nil {
		t.Fatal(err)
	}
	buildVersioned(t, filepath.Join(adHoc, ".wtc", "dev", "wtc"), devVersion)
	if err := os.WriteFile(filepath.Join(adHoc, ".wtc", "dev", "version"), []byte(devVersion+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	adHocRun := exec.Command(pinned, "--version")
	adHocRun.Dir = adHoc
	adHocRun.Env = append(os.Environ(), "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=")
	if raw, err := adHocRun.CombinedOutput(); err != nil || !strings.Contains(string(raw), devVersion+" (dev build, in place of 0.1.39)") {
		t.Fatalf("ad-hoc collection did not hand over: %q %v", raw, err)
	}
	// Both namespaces can move while .wtc remains for generated files.
	if err := os.MkdirAll(filepath.Join(adHoc, ".taba"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"config", "dev"} {
		if err := os.Rename(filepath.Join(adHoc, ".wtc", namespace), filepath.Join(adHoc, ".taba", namespace)); err != nil {
			t.Fatal(err)
		}
	}
	adHocRun = exec.Command(taba, "--version")
	adHocRun.Dir = adHoc
	adHocRun.Env = append(os.Environ(), "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=")
	if raw, err := adHocRun.CombinedOutput(); err != nil || !strings.HasPrefix(string(raw), "taba version "+devVersion+" (dev build, in place of 0.1.39)") {
		t.Fatalf("alternate namespaces lost dev handover or invocation: %q %v", raw, err)
	}
	// The dev build run directly, through a symlinked collection path, is
	// itself and must not hand over to itself.
	link := filepath.Join(t.TempDir(), "collection")
	if err := os.Symlink(collection, link); err != nil {
		t.Fatal(err)
	}
	self := exec.Command(filepath.Join(link, ".wtc", "dev", "wtc"), "--version")
	self.Dir = filepath.Join(link, "harness")
	self.Env = append(os.Environ(), "WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD=")
	if raw, err := self.CombinedOutput(); err != nil || strings.TrimSpace(string(raw)) != "wtc version "+devVersion {
		t.Fatalf("the dev build re-executed itself: %q %v", raw, err)
	}
	// The pinned release moved past the build: it keeps running itself.
	writeVersion("0.1.38-dev.1+dev.gabc1234")
	if got := run("WTC_DEV_DELEGATED_FROM=", "WTC_DEV_BUILD="); !strings.Contains(got, "version 0.1.39") || strings.Contains(got, "dev build,") {
		t.Fatalf("an older dev build must not take over: %q", got)
	}
}

// The handover marker belongs to one process: children such as hooks, agents
// and herdr panes must not inherit it, or they would never hand over.
func TestDelegatedMarkerIsNotInherited(t *testing.T) {
	t.Setenv("WTC_DEV_DELEGATED_FROM", "0.1.39")
	takeDelegatedFrom()
	defer func() { delegatedFrom = "" }()
	if delegatedFrom != "0.1.39" || !strings.Contains(versionLabel(), "in place of 0.1.39") {
		t.Fatalf("marker not taken: %q", delegatedFrom)
	}
	if _, set := os.LookupEnv("WTC_DEV_DELEGATED_FROM"); set {
		t.Fatal("the marker is still in the environment for child processes")
	}
	child, err := exec.Command("/usr/bin/env").Output()
	if err != nil || strings.Contains(string(child), "WTC_DEV_DELEGATED_FROM") {
		t.Fatalf("a child process inherited the marker: %v", err)
	}
}
