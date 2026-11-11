package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/openclaw"
)

// doctorCheck is one line of wtc doctor. A required check that fails makes
// doctor fail; the others are advice.
type doctorCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
	Required bool   `json:"required"`
}

// openClawEnv is what the OpenClaw checks look at, so that tests can fake it.
type openClawEnv struct {
	lookPath func(string) (string, error)
	run      func(name string, args ...string) (string, error)
	readFile func(string) ([]byte, error)
	// project is the gateway's clone of a repository, or "".
	project func(remote string) string
	version string
}

func realOpenClawEnv() openClawEnv {
	return openClawEnv{
		lookPath: exec.LookPath,
		run: func(name string, args ...string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, name, args...).Output()
			return string(out), err
		},
		readFile: os.ReadFile,
		project: func(remote string) string {
			return openclaw.GatewayProjectDir(gatewayStateDir(), openclaw.ParseOrigin(remote))
		},
		version: version,
	}
}

var gitVersion = regexp.MustCompile(`git version (\d+\.\d+(?:\.\d+)?)`)

// mentionsWtc is how doctor decides that an AGENTS.md tells its agent about
// wtc: the word, in any case.
var mentionsWtc = regexp.MustCompile(`(?i)\bwtc\b`)

// openClawChecks checks what a gateway needs for wtc: Git and gh, the plugin
// of this wtc's version, custom Control UI plugins, and an AGENTS.md per
// agent that mentions wtc.
func openClawChecks(env openClawEnv) []doctorCheck {
	var checks []doctorCheck
	gitCheck := doctorCheck{Name: "git", Required: true, Detail: "not on the PATH"}
	if out, err := env.run("git", "--version"); err == nil {
		gitCheck.Detail = strings.TrimSpace(out)
		if m := gitVersion.FindStringSubmatch(out); m != nil {
			if v, err := semver.NewVersion(m[1]); err == nil && !v.LessThan(semver.MustParse("2.42.0")) {
				gitCheck.OK = true
			} else {
				gitCheck.Detail += ": the gateway needs 2.42 or newer to publish"
			}
		}
	}
	checks = append(checks, gitCheck)
	gh, err := env.lookPath("gh")
	checks = append(checks, doctorCheck{Name: "gh", OK: err == nil, Detail: orElse(gh, "not on the PATH: pull requests on GitHub need it")})

	if _, err := env.lookPath("openclaw"); err != nil {
		return append(checks, doctorCheck{Name: "openclaw", Required: true, Detail: branding.Text("not on the PATH: run {{command}} doctor on the gateway host, where the plugin and agents can be checked")})
	}

	plugin := doctorCheck{Name: "plugin", Required: true, Detail: branding.Text("the wtc plugin is not installed: {{command}} openclaw install")}
	if out, err := env.run("openclaw", "plugins", "list", "--json"); err != nil {
		plugin.Detail = fmt.Sprintf("openclaw plugins list failed: %v", err)
	} else {
		var list struct {
			Plugins []struct {
				ID      string `json:"id"`
				Version string `json:"version"`
				Enabled bool   `json:"enabled"`
				Status  string `json:"status"`
			} `json:"plugins"`
		}
		if err := json.Unmarshal([]byte(jsonPart(out)), &list); err != nil {
			plugin.Detail = "could not read openclaw plugins list --json"
		}
		want := strings.TrimPrefix(env.version, "v")
		for _, p := range list.Plugins {
			if p.ID != "wtc" {
				continue
			}
			switch {
			case !p.Enabled:
				plugin.Detail = fmt.Sprintf("%s is installed but disabled: openclaw plugins enable wtc", p.Version)
			case releaseVersion(want) && p.Version != want:
				plugin.Detail = fmt.Sprintf(branding.Text("%s is installed, but this wtc is %s: {{command}} openclaw install"), p.Version, want)
			default:
				plugin.OK = true
				plugin.Detail = fmt.Sprintf("%s, %s", p.Version, orElse(p.Status, "enabled"))
			}
		}
	}
	checks = append(checks, plugin)

	ui := doctorCheck{Name: "control-ui", Required: true, Detail: customPluginsKey + branding.Text(" is off, so the card and panel do not load: {{command}} openclaw install turns it on")}
	if out, err := env.run("openclaw", "config", "get", customPluginsKey); err == nil && strings.TrimSpace(lastLine(out)) == "true" {
		ui.OK, ui.Detail = true, customPluginsKey
	}
	checks = append(checks, ui)
	checks = append(checks, harnessChecks(env)...)

	out, err := env.run("openclaw", "agents", "list", "--json")
	var agents []struct {
		ID        string `json:"id"`
		Workspace string `json:"workspace"`
	}
	if err != nil || json.Unmarshal([]byte(jsonPart(out)), &agents) != nil {
		return append(checks, doctorCheck{Name: "agents", Detail: "could not list the gateway's agents with openclaw agents list --json"})
	}
	for _, a := range agents {
		c := doctorCheck{Name: "agents.md:" + a.ID}
		file := strings.TrimSuffix(a.Workspace, "/") + "/AGENTS.md"
		data, err := env.readFile(file)
		switch {
		case a.Workspace == "":
			c.Detail = "the agent has no workspace"
		case errors.Is(err, fs.ErrNotExist):
			c.Detail = fmt.Sprintf(branding.Text("%s does not exist; create it with: {{command}} docs openclaw-AGENTS.md >> %s"), file, file)
		case err != nil:
			c.Detail = fmt.Sprintf("%s could not be read: %v", file, err)
		case !mentionsWtc.Match(data):
			c.Detail = fmt.Sprintf(branding.Text("%s does not mention wtc, so its agent only finds it through the skill; add: {{command}} docs openclaw-AGENTS.md >> %s"), file, file)
		default:
			c.OK, c.Detail = true, file
		}
		checks = append(checks, c)
	}
	return checks
}

// A release, as opposed to a dev build, whose plugin version is comparable.
func releaseVersion(v string) bool {
	_, err := semver.StrictNewVersion(v)
	return err == nil && !strings.Contains(v, "-")
}

// openclaw prints a banner before its JSON on some versions: the JSON is
// from the first line that starts it.
func jsonPart(out string) string {
	lines := strings.SplitAfter(out, "\n")
	for i, line := range lines {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			return strings.Join(lines[i:], "")
		}
	}
	return out
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[len(lines)-1]
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// printDoctor prints checks as wtc doctor does and fails when a required
// one did.
func printDoctor(checks []doctorCheck, summary string, asJSON bool) error {
	okay := true
	for _, c := range checks {
		if c.Required && !c.OK {
			okay = false
		}
	}
	if asJSON {
		if err := emit(envelope{OK: okay, Data: checks, Summary: summary}, true); err != nil {
			return err
		}
	} else {
		for _, c := range checks {
			state := "ok"
			if !c.OK {
				state = "missing"
				if !c.Required {
					state = "advice"
				}
			}
			fmt.Printf("%-15s %-8s %s\n", c.Name, state, c.Detail)
		}
	}
	if !okay {
		return fmt.Errorf("required checks failed")
	}
	return nil
}

// harnessChecks checks the configured harnesses: the config parses, and the
// gateway has a clone of each, which the first add-repo of a collection
// needs.
func harnessChecks(env openClawEnv) []doctorCheck {
	out, err := env.run("openclaw", "config", "get", openclaw.PluginConfigKey, "--json")
	if err != nil && strings.Contains(out, "unset") {
		out, err = "", nil
	}
	if err != nil {
		return []doctorCheck{{Name: "harnesses", Detail: fmt.Sprintf("could not read %s: %v", openclaw.PluginConfigKey, err)}}
	}
	cfg, err := openclaw.ParseGatewayConfig(out)
	if err != nil {
		return []doctorCheck{{Name: "harnesses", Required: true, Detail: err.Error() + branding.Text(": {{command}} add-repo fails until it is fixed")}}
	}
	if len(cfg.Harnesses) == 0 {
		return []doctorCheck{{Name: "harnesses", OK: true, Detail: "none configured: collections are ad hoc"}}
	}
	var checks []doctorCheck
	for _, h := range cfg.Harnesses {
		c := doctorCheck{Name: "harness:" + h.Name, Detail: h.Remote + " is not a project on the gateway yet: add it with Add repository in the Collection panel"}
		if env.project != nil {
			if dir := env.project(h.Remote); dir != "" {
				c.OK, c.Detail = true, fmt.Sprintf("%s at %s", h.Remote, h.Ref)
				if h.Name == cfg.DefaultHarness {
					c.Detail += ", the default"
				}
			}
		}
		checks = append(checks, c)
	}
	return checks
}
