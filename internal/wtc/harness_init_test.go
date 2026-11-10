package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessInitPreflightDryRunAndNoOverwrite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project-harness")
	opt := HarnessInitOptions{Dir: dir, Name: "agent-harness", Remote: "https://example.invalid/agent-harness.git", CLIVersion: "0.2.0", DryRun: true}
	result, err := InitHarness(opt)
	if err != nil || len(result.Files) != 6 {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("dry run wrote target")
	}
	opt.DryRun = false
	fixtureFile(t, filepath.Join(dir, "README.md"), "existing project instructions", 0644)
	if _, err := InitHarness(opt); err == nil {
		t.Fatal("overwrote README")
	}
	if _, err := os.Stat(filepath.Join(dir, ".harness-repos.yml")); !os.IsNotExist(err) {
		t.Fatal("wrote part of scaffold before collision check")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if string(data) != "existing project instructions" {
		t.Fatal("lost existing instructions")
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	opt.Dir = link
	if _, err := InitHarness(opt); err == nil {
		t.Fatal("followed symlink destination")
	}
}

func TestMinimalHarnessRendersCompleteAgentGuidance(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "task/harness")
	result, err := InitHarness(HarnessInitOptions{Dir: dir, Name: "agent-harness", Remote: "https://example.invalid/agent-harness.git", CLIVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 6 || !strings.Contains(strings.Join(result.Files, ","), ".taba-version,taba.toml") {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatal("initialized Git unexpectedly")
	}
	c, err := OpenCollection(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenderSkills(SkillRenderOptions{SeedScope: true}); err != nil {
		t.Fatal(err)
	}
	// Resolve the relative canon link used by the embedded draft skill.
	canon := filepath.Join(c.Collection, ".wtc/skills/taba-draft-pr/../../instructions/publication-privacy.md")
	if data, err := os.ReadFile(canon); err != nil || !strings.Contains(string(data), "Publication privacy") {
		t.Fatal("default skill has missing canon", err)
	}
	// A harness created with the new file names gets the new scope file name.
	if _, err := os.Stat(filepath.Join(c.Collection, "SCOPE.md")); err != nil {
		t.Fatal("new harness collection was not seeded with SCOPE.md", err)
	}
	if _, err := os.Lstat(filepath.Join(c.Collection, "WTC-SCOPE.md")); !os.IsNotExist(err) {
		t.Fatal("new harness collection was seeded with WTC-SCOPE.md")
	}
	entry, err := os.ReadFile(filepath.Join(c.Collection, "AGENTS.md"))
	if err != nil || !strings.Contains(string(entry), "`.wtc/instructions/`") {
		t.Fatal("entry still assumes a full boilerplate", err)
	}
	override := filepath.Join(dir, "instructions/publication-privacy.md")
	fixtureFile(t, override, "# Project publication policy\n", 0644)
	if _, err := c.RenderSkills(SkillRenderOptions{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(canon)
	if err != nil || string(data) != "# Project publication policy\n" {
		t.Fatal("project override did not win", err)
	}
	if err := os.Remove(override); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenderSkills(SkillRenderOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(canon); err != nil || target == "" {
		t.Fatal("dry run mutated instruction link")
	}
	if _, err := c.RenderSkills(SkillRenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(canon); err != nil || !strings.Contains(string(data), "Publication privacy") {
		t.Fatal("default was not restored", err)
	}
}

func TestHarnessInitRefusesAnExistingHarnessUnderTheOldNames(t *testing.T) {
	dir := t.TempDir()
	fixtureFile(t, filepath.Join(dir, ".wtc-cli-version"), "0.1.64\n", 0644)
	if _, err := InitHarness(HarnessInitOptions{Dir: dir, Name: "agent-harness", Remote: "https://example.invalid/agent-harness.git", CLIVersion: "0.2.1"}); err == nil || !strings.Contains(err.Error(), "existing harness") {
		t.Fatalf("init over an old-name harness: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".taba-version")); !os.IsNotExist(err) {
		t.Fatal("refused init still wrote .taba-version")
	}
}

func TestHarnessInitRejectsPinsThatCannotReadTheScaffold(t *testing.T) {
	for _, pin := range []string{"0.1.59", "0.1.64"} {
		dir := filepath.Join(t.TempDir(), "harness")
		_, err := InitHarness(HarnessInitOptions{Dir: dir, Name: "agent-harness", Remote: "https://example.invalid/agent-harness.git", CLIVersion: pin})
		if err == nil || !strings.Contains(err.Error(), "0.2.0 or later") {
			t.Fatalf("pin %s: %v", pin, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("pin %s: refused init wrote the target", pin)
		}
	}
}
