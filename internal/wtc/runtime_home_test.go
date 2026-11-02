package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeHomeDefaultsToLegacyAndSelectsAlternateTogether(t *testing.T) {
	c := &Context{Collection: t.TempDir()}
	base := func() string { return filepath.Base(filepath.Dir(c.runtimeRoot())) }
	if base() != ".wtc" {
		t.Fatalf("fresh collection must use the legacy name: %s", c.runtimeRoot())
	}
	fixtureFile(t, filepath.Join(c.Collection, ".taba", "svc", "app.yaml"), "tasks: {}\n", 0644)
	if base() != ".taba" || filepath.Base(filepath.Dir(c.runtimeLocalFragment("app"))) != "svc" || !strings.Contains(c.runtimeLocalFragment("app"), ".taba") {
		t.Fatalf("alternate services did not select the runtime with them: %s %s", c.runtimeRoot(), c.runtimeLocalFragment("app"))
	}
	if err := c.runtimePathsSafe(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeHomeIgnoresEmptyDirectories(t *testing.T) {
	c := &Context{Collection: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(c.Collection, ".wtc", "svc"), 0755); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.Collection, ".taba", "runtime", "manifest.json"), "{}", 0644)
	if err := c.runtimeConflict(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.runtimeRoot(), ".taba") {
		t.Fatalf("an empty legacy directory hid the alternate runtime: %s", c.runtimeRoot())
	}
}

func TestRuntimeStateUnderBothNamesIsRefused(t *testing.T) {
	c := &Context{Collection: t.TempDir()}
	fixtureFile(t, filepath.Join(c.Collection, ".wtc", "svc", "app.yaml"), "tasks: {}\n", 0644)
	alternate := filepath.Join(c.Collection, ".taba", "runtime", "manifest.json")
	fixtureFile(t, alternate, "{}", 0644)
	for name, err := range map[string]error{"paths": c.runtimePathsSafe(), "retire": c.RetireRuntime()} {
		if err == nil || !strings.Contains(err.Error(), "both") {
			t.Fatalf("%s accepted two runtime homes: %v", name, err)
		}
	}
	if _, err := c.runtimeManifest(); err == nil {
		t.Fatal("manifest read hid the other runtime")
	}
	path := c.runtimeLocalFragment("app")
	before, _ := os.ReadFile(path)
	if err := c.runtimeChange(path, nil); err == nil {
		t.Fatal("service change accepted two runtime homes")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("refused change wrote the fragment")
	}
	if _, err := os.Stat(alternate); err != nil {
		t.Fatal("refusal touched the other side")
	}
}

func TestLinkedAlternateRuntimeIsRefused(t *testing.T) {
	c := &Context{Collection: t.TempDir()}
	outside := t.TempDir()
	fixtureFile(t, filepath.Join(outside, "svc", "app.yaml"), "tasks: {}\n", 0644)
	if err := os.Symlink(outside, filepath.Join(c.Collection, ".taba")); err != nil {
		t.Fatal(err)
	}
	if err := c.runtimePathsSafe(); err == nil {
		t.Fatal("followed a linked runtime namespace")
	}
}

func TestRuntimeRunsAndRetiresFromAlternateHome(t *testing.T) {
	c := runtimeFixture(t)
	if err := os.MkdirAll(filepath.Join(c.Collection, ".taba", "svc"), 0755); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.Collection, ".taba", "svc", "keep.yaml"), "tasks: {}\n", 0644)
	m, err := c.renderRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Root, string(filepath.Separator)+".taba"+string(filepath.Separator)) {
		t.Fatalf("runner left the selected home: %s", m.Root)
	}
	if read, err := c.runtimeManifest(); err != nil || read == nil || read.Root != m.Root {
		t.Fatalf("manifest did not validate under the alternate home: %+v %v", read, err)
	}
	if _, err := os.Stat(filepath.Join(c.Collection, ".wtc", "runtime")); !os.IsNotExist(err) {
		t.Fatal("rendering forked a legacy runtime")
	}
	lock := filepath.Join(c.Collection, ".wtc-runtime.lock")
	if err := c.withRuntimeLock(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("the shared lock moved")
	}
	if err := removeRetiredGeneratedFiles(c.Collection, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(c.Collection, ".taba")); !os.IsNotExist(err) {
		t.Fatalf("alternate runtime survived retirement: %v", err)
	}
}

func TestRetirementRefusesBothRuntimeHomesBeforeHooks(t *testing.T) {
	c, target := retirementFixture(t)
	fixtureFile(t, filepath.Join(target, ".wtc/svc/app.yaml"), "tasks: {}\n", 0644)
	fixtureFile(t, filepath.Join(target, ".taba/runtime/manifest.json"), "{}", 0644)
	if _, _, err := c.retirePreflight(RetireOptions{Name: filepath.Base(target), Force: true}); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("preflight accepted two runtime homes: %v", err)
	}
}

func TestRetirementAssessmentReportsAlternateRuntimeState(t *testing.T) {
	c, target := retirementFixture(t)
	fixtureFile(t, filepath.Join(target, ".taba/svc/app.yaml"), "tasks: {}\n", 0644)
	report, err := c.InspectRetirement(filepath.Base(target), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(report.Assessment.Unknowns, " "), ".taba/svc") {
		t.Fatalf("alternate services unreported: %+v", report.Assessment)
	}
}

func TestRetirementCleanupCoversBothRuntimeHomes(t *testing.T) {
	target := t.TempDir()
	for _, namespace := range runtimeNamespaces {
		fixtureFile(t, filepath.Join(target, namespace, "runtime", "manifest.json"), "{}", 0644)
		fixtureFile(t, filepath.Join(target, namespace, "svc", "app.yaml"), "tasks: {}\n", 0644)
	}
	if err := removeRetiredGeneratedFiles(target, false); err != nil {
		t.Fatal(err)
	}
	for _, namespace := range runtimeNamespaces {
		if _, err := os.Lstat(filepath.Join(target, namespace)); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", namespace, err)
		}
	}
}
