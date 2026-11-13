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

	"gopkg.in/yaml.v3"

	"github.com/Masterminds/semver/v3"
	"taba.sh/taba/internal/wtc"
)

func TestWriteOpenClawPluginStampsTheVersion(t *testing.T) {
	dir := t.TempDir()
	if err := writeOpenClawPlugin(dir, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"index.js", "package.json", "openclaw.plugin.json", "dist/control-ui/v1/index.js", "skills/taba/SKILL.md", "skills/wtc/SKILL.md",
		// The taba skills' OpenClaw variants, under their own names.
		"skills/taba-catch-up/SKILL.md", "skills/taba-pr/SKILL.md", "skills/taba-follow/SKILL.md"} {
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
	skill, _ := os.ReadFile(filepath.Join(dir, "skills/taba/SKILL.md"))
	if !strings.Contains(string(skill), "taba docs instructions/openclaw.md") {
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

// go install of a release tag records the module version; that is the
// version. Checkout builds, pseudo-versions and modified trees are not.
func TestModuleVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v0.2.0":                               "0.2.0",
		"v0.2.0-rc.1":                          "0.2.0-rc.1",
		"(devel)":                              "",
		"":                                     "",
		"v0.2.1-0.20261109083000-abcdef123456": "",
		"v0.2.0-rc.1.0.20261109083000-abcdef123456":  "",
		"v0.2.1-0.20261109083000-abcdef123456+dirty": "",
		"v0.2.0+dirty": "",
	} {
		got := moduleVersion(func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: in}}, true })
		if got != want {
			t.Errorf("moduleVersion(%q) = %q, want %q", in, got, want)
		}
	}
	// A clean checkout at a tag records the tag too, next to vcs.* settings.
	checkout := &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"},
		Settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "317ca2a9b1c2"}, {Key: "vcs.modified", Value: "false"}}}
	if got := moduleVersion(func() (*debug.BuildInfo, bool) { return checkout, true }); got != "" {
		t.Errorf("clean tagged checkout: %q", got)
	}
	if got := moduleVersion(func() (*debug.BuildInfo, bool) { return nil, false }); got != "" {
		t.Errorf("without build info: %q", got)
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
		"taba-catch-up": "wtc catch-up --continue",
		"taba-pr":       "they push it\n   with ↑",
		"taba-follow":   "wtc: pull request news",
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
	catchUp, err := os.ReadFile(filepath.Join(dir, "skills/taba-catch-up/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(catchUp), "wtc docs instructions/openclaw-recovery.md") || !strings.Contains(string(catchUp), "--finish-recovery") {
		t.Fatal("installed catch-up skill lacks bundled recovery routing")
	}
	reference, err := wtc.ReadDefault("instructions/openclaw-recovery.md")
	if err != nil || !strings.Contains(string(reference), "--expected-head") {
		t.Fatalf("recovery reference is not embedded: %v", err)
	}
	// Skills without an OpenClaw variant stay local.
	if _, err := os.Stat(filepath.Join(dir, "skills", "taba-open")); err == nil {
		t.Error("taba-open has no OpenClaw variant but was installed")
	}
}

func TestOpenClawRootSkillUpgradePreservesCanonicalAndLegacyDiscovery(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "skills", "wtc", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(old), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("legacy full procedure before upgrade"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenClawPlugin(dir, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	type skill struct {
		Name         string `yaml:"name"`
		DisableModel bool   `yaml:"disable-model-invocation"`
		Metadata     struct {
			OpenClaw struct {
				SkillKey string `yaml:"skillKey"`
				Requires struct {
					Bins []string `yaml:"bins"`
				} `yaml:"requires"`
			} `yaml:"openclaw"`
		} `yaml:"metadata"`
	}
	for _, name := range []string{"taba", "wtc"} {
		raw, err := os.ReadFile(filepath.Join(dir, "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(string(raw), "---", 3)
		if len(parts) != 3 {
			t.Fatalf("%s has no valid skill frontmatter", name)
		}
		var got skill
		if err := yaml.Unmarshal([]byte(parts[1]), &got); err != nil {
			t.Fatal(err)
		}
		if got.Name != name {
			t.Fatalf("%s installed as %q", name, got.Name)
		}
		if got.DisableModel {
			t.Fatalf("%s was hidden from existing name-based agent allowlists", name)
		}
		bins := got.Metadata.OpenClaw.Requires.Bins
		if len(bins) != 2 || bins[0] != name || bins[1] != "git" {
			t.Fatalf("%s eligibility requires %v", name, bins)
		}
		if name == "taba" && got.Metadata.OpenClaw.SkillKey != "wtc" {
			t.Fatal("canonical skill lost the legacy settings/session-toggle identity")
		}
		if strings.Contains(string(raw), "legacy full procedure before upgrade") {
			t.Fatal("upgrade retained the stale procedure")
		}
	}
}
