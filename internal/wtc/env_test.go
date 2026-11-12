package wtc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) *Context {
	t.Helper()
	root := t.TempDir()
	collection := filepath.Join(root, "demo")
	harness := filepath.Join(collection, "harness")
	if err := os.MkdirAll(harness, 0755); err != nil {
		t.Fatal(err)
	}
	registry := "repos:\n  - name: harness-example\n    remote: https://example.invalid/harness-example.git\n    default_ref: origin/main\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n    port_offset: 2\n    x-deploy-workflow: release\n"
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte(registry), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := Discover(collection)
	if err != nil {
		t.Fatal(err)
	}
	c.ConfigRoot = filepath.Join(root, "config")
	return c
}
func TestRenderEnvPreservesPortBase(t *testing.T) {
	c := fixture(t)
	old := []byte("COLLECTION_PORT_BASE=43100\n")
	if err := os.WriteFile(filepath.Join(c.Collection, ".env.collection"), old, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := c.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"COLLECTION_PORT_BASE=43100", "WIDGET_PORT=43102", "WTC_COLLECTION='demo'"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if err := c.WriteEnv(got); err != nil {
		t.Fatal(err)
	}
	again, err := c.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(again) {
		t.Fatal("regeneration moved a port or changed output")
	}
}
func TestLocalEnvSymlinkStopsGeneration(t *testing.T) {
	c := fixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Symlink(outside, filepath.Join(c.Collection, ".env.collection.local")); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateEnvSupport(); err == nil {
		t.Fatal("accepted symlinked local env")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("followed symlink")
	}
}
func TestEjectRefusesOverwrite(t *testing.T) {
	c := fixture(t)
	paths, err := Eject(c.Harness, []string{"instructions/publication-privacy.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("got %d files", len(paths))
	}
	if _, err := Eject(c.Harness, []string{"instructions/publication-privacy.md"}); err == nil {
		t.Fatal("overwrote ejected default")
	}
}

func TestEjectSkillGlob(t *testing.T) {
	c := fixture(t)
	paths, err := Eject(c.Harness, []string{"skills/wtc-*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 10 {
		t.Fatalf("unexpected paths: %v", paths)
	}
}

func TestExistingLocalEnvStillRefreshesMise(t *testing.T) {
	c := fixture(t)
	local := filepath.Join(c.Collection, ".env.collection.local")
	if err := os.WriteFile(local, []byte("CUSTOM=keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "CUSTOM=keep\n" {
		t.Fatal("local overrides changed")
	}
	if _, err := os.Stat(filepath.Join(c.Collection, "mise.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestTrustGeneratedMiseConfig(t *testing.T) {
	c := fixture(t)
	if err := c.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	t.Setenv("MISE_TEST_LOG", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := filepath.Join(bin, "mise")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MISE_TEST_LOG\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := c.TrustMise(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(log)
	if err != nil || string(got) != "trust "+filepath.Join(c.Collection, "mise.toml")+"\n" {
		t.Fatalf("mise invocation = %q, %v", got, err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 7\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := c.TrustMise(); err == nil {
		t.Fatal("ignored mise trust failure")
	}
}

func TestMisePinSurvivesEnvRegeneration(t *testing.T) {
	c := fixture(t)
	if err := os.WriteFile(filepath.Join(c.Harness, ".wtc-cli-version"), []byte("0.1.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := c.EnsureEnvSupport(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(c.Collection, "mise.toml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"[tools]", `"github:lcorneliussen/taba" = "0.1.1"`, "[env]"} {
			if !strings.Contains(string(data), want) {
				t.Fatalf("mise.toml missing %q: %s", want, data)
			}
		}
	}
}

func TestMiseKeepsHarnessToolsBesideCLIPin(t *testing.T) {
	c := fixture(t)
	config := "[mise.tools]\nbun = \"1.2.3\"\n\"npm:example-cli\" = \"4.5.6\"\n"
	if err := os.WriteFile(filepath.Join(c.Harness, "wtc.toml"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Harness, ".wtc-cli-version"), []byte("0.1.2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.Collection, "mise.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"bun" = "1.2.3"`, `"npm:example-cli" = "4.5.6"`, `"github:lcorneliussen/taba" = "0.1.2"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in %s", want, data)
		}
	}
	if strings.Count(string(data), "[tools]") != 1 {
		t.Fatalf("expected one tools table: %s", data)
	}
}

func TestInvalidMisePinDoesNotRewrite(t *testing.T) {
	c := fixture(t)
	path := filepath.Join(c.Collection, "mise.toml")
	if err := os.WriteFile(path, []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Harness, ".wtc-cli-version"), []byte("latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureEnvSupport(); err == nil {
		t.Fatal("accepted non-exact pin")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep\n" {
		t.Fatalf("changed mise.toml on invalid pin: %q, %v", data, err)
	}
}

func TestRenderedEnvCanBeSourcedWithSpaces(t *testing.T) {
	c := fixture(t)
	c.ConfigRoot = filepath.Join(t.TempDir(), "control root")
	data, err := c.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteEnv(data); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", "-c", `set -a; . "$1"; printf '%s' "$WTC_CONFIG_ROOT"`, "bash", filepath.Join(c.Collection, ".env.collection"))
	command.Env = append(os.Environ(), "BASH_ENV=")
	got, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != c.ConfigRoot {
		t.Fatalf("got %q, want %q", got, c.ConfigRoot)
	}
}

func TestMachineToolIdentityOptIn(t *testing.T) {
	c := fixture(t)
	t.Setenv("WTC_TWG_SITE", "")
	if err := os.MkdirAll(c.ConfigRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.ConfigRoot, "wtc.env"), []byte("WTC_TWG_SITE=example\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.ConfigRoot, "gh"), []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := c.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "GH_CONFIG_DIR=") {
		t.Fatal("file activated gh identity")
	}
	if strings.Contains(string(data), "TWG_SITE='example'") {
		t.Fatal("environment override was not honored")
	}
	if err := os.Unsetenv("WTC_TWG_SITE"); err != nil {
		t.Fatal(err)
	}
	data, err = c.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "TWG_SITE='example'") {
		t.Fatal("machine default was not loaded")
	}
}

func TestRegistryRejectsPortNameCollision(t *testing.T) {
	c := fixture(t)
	p := filepath.Join(c.Harness, ".harness-repos.yml")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("  - name: widget-extra\n    remote: https://example.invalid/extra.git\n    port_offset: 3\n  - name: widget_extra\n    remote: https://example.invalid/extra2.git\n    port_offset: 4\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCollection(c.Collection); err == nil {
		t.Fatal("accepted duplicate port variable")
	}
}

func TestMiseToolsCannotOverrideEitherCLIPinKey(t *testing.T) {
	for _, key := range []string{cliMiseTool, legacyCLIMiseTool} {
		c := fixture(t)
		fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.2.0\n", 0644)
		c.Config.Mise.Tools = map[string]string{key: "0.1.0"}
		if _, err := c.RenderMise(); err == nil || !strings.Contains(err.Error(), "must not override") {
			t.Errorf("%s override accepted: %v", key, err)
		}
	}
}
