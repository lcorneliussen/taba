package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"taba.sh/taba/internal/wtc"
)

func TestWriteOpenClawPluginStampsTheVersion(t *testing.T) {
	dir := t.TempDir()
	if err := writeOpenClawPlugin(dir, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"index.js", "package.json", "openclaw.plugin.json", "dist/control-ui/v1/index.js", "skills/wtc/SKILL.md",
		// The wtc skills' OpenClaw variants, under their own names.
		"skills/wtc-catch-up/SKILL.md", "skills/wtc-pr/SKILL.md", "skills/wtc-follow/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s was not written: %v", f, err)
		}
	}
	for _, f := range []string{"package.json", "openclaw.plugin.json"} {
		data, _ := os.ReadFile(filepath.Join(dir, f))
		var metadata map[string]any
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("installed %s is invalid JSON: %v", f, err)
		}
		key, want := "id", "wtc"
		if f == "package.json" {
			key, want = "name", "openclaw-plugin-wtc"
		}
		if metadata[key] != want {
			t.Errorf("installed %s changes the existing %s: %v", f, key, metadata[key])
		}
		if !strings.Contains(string(data), `"version": "1.2.3"`) {
			t.Errorf("%s is not stamped with the version:\n%s", f, data)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "embed.go")); err == nil {
		t.Error("the Go file that embeds the plugin is not part of it")
	}
	// A dev build stamps a version the plugin compares with nothing.
	devDir := t.TempDir()
	if err := writeOpenClawPlugin(devDir, "dev"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(devDir, "package.json")); !regexp.MustCompile(`"version": "\d+\.\d+\.\d+-dev[+"]`).Match(data) {
		t.Errorf("a dev build is not stamped with a -dev version:\n%s", data)
	}
	skill, _ := os.ReadFile(filepath.Join(dir, "skills/wtc/SKILL.md"))
	if !strings.Contains(string(skill), "wtc docs instructions/openclaw.md") {
		t.Error("the skill does not point the agent at the embedded instructions")
	}
}

// install runs openclaw from the PATH: config first, then the plugin, then
// the reload that makes the running gateway use it.
func TestOpenClawInstallRunsOpenClaw(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "wtc")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	bin := filepath.Join(root, "bin")
	log := filepath.Join(root, "calls")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// The fake records its arguments and that the plugin it is given exists.
	fake := "#!/bin/sh\necho \"$*\" >> " + log + "\nif [ \"$2\" = install ]; then test -f \"$3/openclaw.plugin.json\" && echo manifest >> " + log + "; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "openclaw"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "openclaw", "install")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install: %s: %v", out, err)
	}
	if !strings.Contains(string(out), "installed and reloaded") {
		t.Fatalf("the reload did not succeed: %s", out)
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 4 || lines[0] != "config set "+customPluginsKey+" true" || !strings.HasPrefix(lines[1], "plugins install ") || !strings.HasSuffix(lines[1], " --force --accept-capabilities") || lines[2] != "manifest" || lines[3] != "plugins reload wtc" {
		t.Fatalf("openclaw was called with:\n%s", calls)
	}
}

func TestOpenClawInstallFlagsAndErrors(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "wtc")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	bin := filepath.Join(root, "bin")
	log := filepath.Join(root, "calls")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(path string, args ...string) (string, error) {
		cmd := exec.Command(binary, append([]string{"openclaw", "install"}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+path)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// --dir writes the plugin and calls nothing.
	dir := filepath.Join(root, "plugin")
	if out, err := run(bin, "--dir", dir); err != nil || !strings.Contains(out, "openclaw plugins install "+dir+" --force --accept-capabilities") {
		t.Fatalf("--dir: %s %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "openclaw.plugin.json")); err != nil {
		t.Fatal("--dir wrote no plugin")
	}

	// Without openclaw on the PATH it says where to run it.
	if out, err := run(bin); err == nil || !strings.Contains(out, "openclaw is not on the PATH") {
		t.Fatalf("no openclaw: %s %v", out, err)
	}

	// --no-config installs without touching the setting; a failing
	// openclaw is an error, except a failing reload, which only asks for a
	// restart.
	fake := "#!/bin/sh\necho \"$*\" >> " + log + "\n[ -n \"$FAIL\" ] && [ \"$2\" = \"$FAIL\" ] && exit 1\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "openclaw"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := run(bin, "--no-config"); err != nil {
		t.Fatalf("--no-config: %s %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if lines := strings.Split(strings.TrimSpace(string(calls)), "\n"); len(lines) != 2 || !strings.HasPrefix(lines[0], "plugins install ") || lines[1] != "plugins reload wtc" {
		t.Fatalf("--no-config called openclaw with:\n%s", calls)
	}
	fail := func(sub string) (string, error) {
		cmd := exec.Command(binary, "openclaw", "install")
		cmd.Env = append(os.Environ(), "PATH="+bin, "FAIL="+sub)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := fail("set"); err == nil || !strings.Contains(out, "openclaw config set") {
		t.Fatalf("a failing openclaw was not reported: %s %v", out, err)
	}
	if out, err := fail("reload"); err != nil || !strings.Contains(out, "restart the gateway") {
		t.Fatalf("a failing reload should install and ask for a restart: %s %v", out, err)
	}
}

// A build without a release version says which release it follows, its
// commit and when, and whether it had local changes.
func TestDescribeBuild(t *testing.T) {
	info := func(settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Settings: settings}, true }
	}
	got := describeBuild(info(debug.BuildSetting{Key: "vcs.revision", Value: "317ca2a9b1c2"}, debug.BuildSetting{Key: "vcs.time", Value: "2026-10-23T09:12:44Z"}, debug.BuildSetting{Key: "vcs.modified", Value: "true"}))
	if !regexp.MustCompile(`^\d+\.\d+\.\d+-dev\+g317ca2a\.20261023\.0912\.dirty$`).MatchString(got) {
		t.Errorf("with vcs info: %s", got)
	}
	if _, err := semver.NewVersion(got); err != nil {
		t.Errorf("%s is no semantic version: %v", got, err)
	}
	if got := describeBuild(func() (*debug.BuildInfo, bool) { return nil, false }); !regexp.MustCompile(`^\d+\.\d+\.\d+-dev$`).MatchString(got) {
		t.Errorf("without: %s", got)
	}
	// wtc --version of an unversioned build is that description; a release
	// is itself.
	if got := versionLabel(); !regexp.MustCompile(`^\d+\.\d+\.\d+-dev`).MatchString(got) {
		t.Errorf("wtc --version of a test build: %s", got)
	}
	was := version
	t.Cleanup(func() { version = was })
	version = "1.2.3"
	if got := versionLabel(); got != "1.2.3" {
		t.Errorf("a release: %s", got)
	}
}

func TestOpenClawSkillsAreTheOpenClawVariants(t *testing.T) {
	dir := t.TempDir()
	if err := writeOpenClawPlugin(dir, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	// Each is its OpenClaw variant, not the local skill of that name.
	for name, marker := range map[string]string{
		"wtc-catch-up": "wtc catch-up --continue",
		"wtc-pr":       "they push it\n   with ↑",
		"wtc-follow":   "wtc: pull request news",
	} {
		data, err := os.ReadFile(filepath.Join(dir, "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "---\nname: "+name+"\n") || !strings.Contains(string(data), marker) {
			t.Errorf("%s is not its OpenClaw variant:\n%.400s", name, data)
		}
		// The marker tells the variants apart only while the local skill lacks it.
		if local, _ := wtc.ReadDefault("skills/" + name + "/SKILL.md"); strings.Contains(string(local), marker) {
			t.Errorf("the local %s has %q too: pick a marker only its OpenClaw variant has", name, marker)
		}
	}
	// Skills without an OpenClaw variant stay local.
	if _, err := os.Stat(filepath.Join(dir, "skills", "wtc-open")); err == nil {
		t.Error("wtc-open has no OpenClaw variant but was installed")
	}
}
