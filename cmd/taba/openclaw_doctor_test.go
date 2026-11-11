package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGateway answers the commands openClawChecks runs.
type fakeGateway struct {
	git, plugins, customPlugins, agents string
	// config is the wtc plugin's config; projects the gateway's clones.
	config     string
	projects   map[string]string
	files      map[string]string
	noOpenClaw bool
}

func (g fakeGateway) env(version string) openClawEnv {
	return openClawEnv{
		lookPath: func(name string) (string, error) {
			if name == "openclaw" && g.noOpenClaw {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
		run: func(name string, args ...string) (string, error) {
			switch strings.Join(append([]string{name}, args...), " ") {
			case "git --version":
				return g.git, nil
			case "openclaw plugins list --json":
				return g.plugins, nil
			case "openclaw config get " + customPluginsKey:
				return g.customPlugins, nil
			case "openclaw agents list --json":
				return g.agents, nil
			case "openclaw config get plugins.entries.wtc.config --json":
				if g.config == "" {
					return `{"ok": false, "error": {"message": "Config path is valid but unset"}}`, errors.New("exit status 1")
				}
				return g.config, nil
			}
			return "", errors.New("unexpected command")
		},
		readFile: func(path string) ([]byte, error) {
			if data, ok := g.files[path]; ok {
				if data == "EACCES" {
					return nil, fs.ErrPermission
				}
				return []byte(data), nil
			}
			return nil, os.ErrNotExist
		},
		project: func(remote string) string { return g.projects[remote] },
		version: version,
	}
}

func checksByName(checks []doctorCheck) map[string]doctorCheck {
	m := map[string]doctorCheck{}
	for _, c := range checks {
		m[c.Name] = c
	}
	return m
}

func healthyGateway() fakeGateway {
	return fakeGateway{
		git:           "git version 2.47.3\n",
		plugins:       `{"plugins":[{"id":"bitbucket","version":"0.3.0","enabled":true},{"id":"wtc","version":"0.1.43","enabled":true,"status":"loaded"}]}`,
		customPlugins: "true\n",
		agents:        `[{"id":"main","workspace":"/ws/main"},{"id":"ops","workspace":"/ws/ops"}]`,
		files: map[string]string{
			"/ws/main/AGENTS.md": "# Team\n\n## Worktree collections\n\nRead `wtc docs instructions/openclaw.md`.\n",
			"/ws/ops/AGENTS.md":  "# Ops\n\nNothing about collections.\n",
		},
	}
}

func TestOpenClawDoctorChecksTheGateway(t *testing.T) {
	checks := checksByName(openClawChecks(healthyGateway().env("v0.1.43")))
	for _, name := range []string{"git", "gh", "plugin", "control-ui", "agents.md:main"} {
		if !checks[name].OK {
			t.Errorf("%s: %+v", name, checks[name])
		}
	}
	ops := checks["agents.md:ops"]
	if ops.OK || ops.Required || !strings.Contains(ops.Detail, "wtc docs openclaw-AGENTS.md >> /ws/ops/AGENTS.md") {
		t.Errorf("an AGENTS.md without wtc is advice that says how to fix it: %+v", ops)
	}
}

func TestOpenClawDoctorFlagsWhatIsWrong(t *testing.T) {
	g := healthyGateway()
	g.git = "git version 2.39.5\n"
	g.customPlugins = "false\n"
	g.files = map[string]string{}
	checks := checksByName(openClawChecks(g.env("0.1.44")))
	if c := checks["git"]; c.OK || !c.Required || !strings.Contains(c.Detail, "2.42") {
		t.Errorf("old git: %+v", c)
	}
	if c := checks["plugin"]; c.OK || !strings.Contains(c.Detail, "0.1.43 is installed, but this wtc is 0.1.44") {
		t.Errorf("stale plugin: %+v", c)
	}
	if c := checks["control-ui"]; c.OK || !c.Required {
		t.Errorf("custom plugins off: %+v", c)
	}
	if c := checks["agents.md:main"]; c.OK || !strings.Contains(c.Detail, "does not exist") || !strings.Contains(c.Detail, ">> /ws/main/AGENTS.md") {
		t.Errorf("missing AGENTS.md: %+v", c)
	}

	// A dev build compares no versions; a missing plugin is required.
	if c := checksByName(openClawChecks(healthyGateway().env("0.1.44-dev.2")))["plugin"]; !c.OK {
		t.Errorf("a dev build should accept any plugin version: %+v", c)
	}
	g = healthyGateway()
	g.plugins = `{"plugins":[]}`
	if c := checksByName(openClawChecks(g.env("0.1.43")))["plugin"]; c.OK || !c.Required || !strings.Contains(c.Detail, "wtc openclaw install") {
		t.Errorf("no plugin: %+v", c)
	}

	// Without openclaw, only what can be checked is.
	g = healthyGateway()
	g.noOpenClaw = true
	checks = checksByName(openClawChecks(g.env("0.1.43")))
	if c := checks["openclaw"]; c.OK || !c.Required {
		t.Errorf("no openclaw: %+v", c)
	}
	if _, ok := checks["plugin"]; ok {
		t.Error("the plugin cannot be checked without openclaw")
	}
}

func TestOpenClawDoctorErrorBranches(t *testing.T) {
	g := healthyGateway()
	g.git = "git: unknown\n"
	g.plugins = "OpenClaw 2026.10.2 (b8952d4) [beta] — {banner}\n" + `{"plugins":[{"id":"wtc","version":"0.1.43","enabled":false}]}`
	g.agents = `[{"id":"main","workspace":""},{"id":"ops","workspace":"/ws/ops"}]`
	g.files = map[string]string{"/ws/ops/AGENTS.md": "EACCES"}
	checks := checksByName(openClawChecks(g.env("0.1.43")))
	if c := checks["git"]; c.OK {
		t.Errorf("an unreadable git version: %+v", c)
	}
	if c := checks["plugin"]; c.OK || !strings.Contains(c.Detail, "disabled") {
		t.Errorf("a disabled plugin behind a banner: %+v", c)
	}
	if c := checks["agents.md:main"]; c.OK || !strings.Contains(c.Detail, "no workspace") {
		t.Errorf("no workspace: %+v", c)
	}
	if c := checks["agents.md:ops"]; c.OK || strings.Contains(c.Detail, ">") || !strings.Contains(c.Detail, "could not be read") {
		t.Errorf("an unreadable AGENTS.md is never offered a redirect: %+v", c)
	}

	g = healthyGateway()
	g.plugins = "not json"
	g.agents = "not json"
	checks = checksByName(openClawChecks(g.env("0.1.43")))
	if c := checks["plugin"]; c.OK || !c.Required {
		t.Errorf("unparsable plugins list: %+v", c)
	}
	if c := checks["agents"]; c.OK || c.Required || !strings.Contains(c.Detail, "could not list") {
		t.Errorf("unparsable agents list: %+v", c)
	}
}

// wtc doctor --openclaw runs the gateway checks, prints them and fails on
// a required one; --json carries ok, required and the details.
func TestOpenClawDoctorCommand(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "wtc")
	if out, err := exec.Command("go", "build", "-ldflags", "-X main.version=0.1.43", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	bin := filepath.Join(root, "bin")
	ws := filepath.Join(root, "ws")
	for _, dir := range []string{bin, ws} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("We use wtc.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/sh
case "$1 $2" in
  "plugins list") echo '{"plugins":[{"id":"wtc","version":"0.1.43","enabled":true,"status":"loaded"}]}' ;;
  "config get") echo "$CUSTOM" ;;
  "agents list") echo '[{"id":"main","workspace":"` + ws + `"}]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "openclaw"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "gh"} {
		real, err := exec.LookPath(name)
		if err != nil {
			if name == "gh" {
				continue
			}
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	doctor := func(custom string, args ...string) (string, error) {
		cmd := exec.Command(binary, append([]string{"doctor", "--openclaw"}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+bin, "CUSTOM="+custom, "OPENCLAW_STATE_DIR=", "OPENCLAW_SHELL=")
		out, err := cmd.Output()
		return string(out), err
	}
	out, err := doctor("true")
	if err != nil || !strings.Contains(out, "plugin") || !strings.Contains(out, "agents.md:main") {
		t.Fatalf("a healthy gateway: %s %v", out, err)
	}
	out, err = doctor("false", "--json")
	if err == nil {
		t.Fatalf("custom plugins off must fail doctor: %s", out)
	}
	var result struct {
		OK   bool          `json:"ok"`
		Data []doctorCheck `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if result.OK {
		t.Error("ok is true with a required check failing")
	}
	for _, c := range result.Data {
		if c.Name == "control-ui" && (c.OK || !c.Required) {
			t.Errorf("control-ui: %+v", c)
		}
	}
}

// Configured harnesses must parse, and each must be a project on the
// gateway; none configured is fine.
func TestOpenClawDoctorChecksTheHarnesses(t *testing.T) {
	g := healthyGateway()
	if c := checksByName(openClawChecks(g.env("0.1.43")))["harnesses"]; !c.OK || !strings.Contains(c.Detail, "none configured") {
		t.Errorf("no harnesses: %+v", c)
	}
	g.config = `{"harnesses": [{"name": "platform", "remote": "https://github.com/example-org/platform-harness"}, {"name": "ops", "remote": "https://github.com/example-org/ops-harness", "ref": "develop"}], "defaultHarness": "ops"}`
	g.projects = map[string]string{"https://github.com/example-org/ops-harness": "/state/repos/ops-harness"}
	checks := checksByName(openClawChecks(g.env("0.1.43")))
	if c := checks["harness:ops"]; !c.OK || c.Detail != "https://github.com/example-org/ops-harness at develop, the default" {
		t.Errorf("ops: %+v", c)
	}
	if c := checks["harness:platform"]; c.OK || c.Required || !strings.Contains(c.Detail, "Add repository") {
		t.Errorf("platform, not on the gateway: %+v", c)
	}
	g.config = `{"harnesses": [{"name": "Platform", "remote": "https://github.com/example-org/platform-harness"}]}`
	if c := checksByName(openClawChecks(g.env("0.1.43")))["harnesses"]; c.OK || !c.Required {
		t.Errorf("a bad config: %+v", c)
	}
}
