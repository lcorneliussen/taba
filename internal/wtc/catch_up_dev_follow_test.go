package wtc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Pane control and installation are fixture scripts; eligible OFF/normal
// targets execute the real CLI to verify the installed version and bypasses.
func TestCatchUpInstalledReloadPreservesDevFollow(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the native CLI for isolated installed-target reload checks")
	}
	candidate := filepath.Join(t.TempDir(), "installed-candidate")
	build := exec.Command("go", "build", "-ldflags=-X main.version=0.2.2", "-o", candidate, "../../cmd/taba")
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("candidate build: %v %s", err, raw)
	}
	callers := []struct{ name, canonical, legacy string }{
		{"none", "", ""}, {"canonical-off", "off", ""}, {"legacy-off", "", "off"},
		{"both-off", "off", "off"}, {"canonical-conflict", "off", "on"}, {"legacy-conflict", "on", "off"},
	}
	for _, policy := range []string{"on", "off", "normal", "invalid", "auto-active"} {
		for _, caller := range callers {
			for _, dry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/dry-%t", policy, caller.name, dry), func(t *testing.T) {
					c := newWorkspaceFixture(t)
					fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.2.2\n", 0644)
					config := "[compatibility]\nrequires = \">=0.2.0,<0.3\"\n"
					if policy == "on" || policy == "auto-active" {
						source := filepath.Join(c.Collection, "cli")
						fixtureFile(t, filepath.Join(source, "go.mod"), "module taba.sh/taba\n\ngo 1.27.0\n", 0644)
						fixtureGit(t, "-C", source, "init", "-q", "-b", "main")
						fixtureGit(t, "-C", source, "remote", "add", "origin", "https://github.com/example/cli.git")
						fixtureGit(t, "-C", source, "add", "go.mod")
						fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic dev source")
						fixtureGit(t, "-C", source, "update-ref", "refs/remotes/origin/main", "HEAD")
						if policy == "on" {
							if err := SetDevMode(c.Collection, "on", source); err != nil {
								t.Fatal(err)
							}
						} else {
							config += "[dev]\nauto_source = \"cli\"\n"
							reg := filepath.Join(c.Harness, ".harness-repos.yml")
							old, _ := os.ReadFile(reg)
							fixtureFile(t, reg, string(old)+"  - name: cli\n    remote: https://github.com/example/cli.git\n    default_ref: origin/main\n", 0644)
						}
					} else if policy == "off" {
						if err := SetDevMode(c.Collection, "off", ""); err != nil {
							t.Fatal(err)
						}
					} else if policy == "invalid" {
						fixtureFile(t, filepath.Join(c.Collection, ".wtc-dev-mode.json"), "{", 0600)
					}
					fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), config, 0644)
					fixtureGit(t, "-C", c.Harness, "add", ".")
					fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic installed pin")
					before := catchUpDevPolicyBytes(t, c.Collection)
					state := t.TempDir()
					t.Setenv("RELOAD_TEST_STATE", state)
					t.Setenv("RELOAD_TEST_CANDIDATE", candidate)
					t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
					t.Setenv("TABA_CONFIG_ROOT", c.ConfigRoot)
					t.Setenv("HERDR_SESSION", "synthetic")
					t.Setenv("TABA_DEV_BUILD", caller.canonical)
					t.Setenv("WTC_DEV_BUILD", caller.legacy)
					bin := filepath.Join(c.Workspace, "reload-bin")
					fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nexit 1\n", 0755)
					fixtureFile(t, filepath.Join(bin, "wtc"), "#!/bin/sh\nprintf stale-PATH > \"$RELOAD_TEST_STATE/stale\"\nexit 99\n", 0755)
					fixtureFile(t, filepath.Join(bin, "mise"), `#!/bin/sh
printf '%s\n' "$*" >> "$RELOAD_TEST_STATE/mise.log"
case "$1" in
 trust) exit 0 ;;
 install) touch "$RELOAD_TEST_STATE/installed" ;;
 where) [ "$2" = github:lcorneliussen/taba@0.2.2 ] || exit 97; dirname "$RELOAD_TEST_CANDIDATE" ;;
 exec)
   [ "$2" = github:lcorneliussen/taba@0.2.2 ] || exit 97
   [ "$TABA_DEV_BUILD" = off ] && [ "$WTC_DEV_BUILD" = off ] || exit 98
   [ "$MISE_AUTO_INSTALL" = 0 ] && [ "$MISE_EXEC_AUTO_INSTALL" = 0 ] || exit 98
   printf '%s|%s\n' "$TABA_DEV_BUILD" "$WTC_DEV_BUILD" > "$RELOAD_TEST_STATE/child-env"
   "$RELOAD_TEST_CANDIDATE" --json dev status > "$RELOAD_TEST_STATE/dev-status.json" 2> "$RELOAD_TEST_STATE/dev-status.err" || :
   shift 4
   exec "$RELOAD_TEST_CANDIDATE" "$@" ;;
esac
`, 0755)
					fixtureFile(t, filepath.Join(bin, "herdr"), `#!/bin/sh
printf '%s\n' "$*" >> "$RELOAD_TEST_STATE/pane.log"
shift 2
case "$1 $2" in
 'workspace list') printf '%s\n' '{"result":{"workspaces":[{"label":"main","workspace_id":"w1"}]}}' ;;
 'pane list') printf '%s\n' '{"result":{"panes":[{"label":"status","pane_id":"w1:p3"}]}}' ;;
 'pane process-info')
   if [ -f "$RELOAD_TEST_STATE/restarted" ]; then args='["taba","status","--tui"]'
   elif [ -f "$RELOAD_TEST_STATE/interrupted" ]; then args='["sh"]'
   else args='["taba","status","--tui"]'; fi
   printf '{"result":{"process_info":{"foreground_process_group_id":42,"foreground_processes":[{"pid":42,"argv":%s}]}}}\n' "$args" ;;
 'pane send-keys') touch "$RELOAD_TEST_STATE/interrupted" ;;
 'pane run')
   /bin/sh -c "$4 --json --no-watch --no-fetch" > "$RELOAD_TEST_STATE/status.json" 2> "$RELOAD_TEST_STATE/status.err" || exit 1
   touch "$RELOAD_TEST_STATE/restarted" ;;
esac
`, 0755)
					t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
					selected, selectionErr := DevFollowConfiguration(c.Collection)
					if policy == "on" || policy == "auto-active" {
						if selectionErr != nil || !selected.Active {
							t.Fatalf("active selection prerequisite: %+v %v", selected, selectionErr)
						}
					}
					if policy == "invalid" && selectionErr == nil {
						t.Fatal("invalid policy prerequisite did not fail")
					}
					report := CatchUpReport{Initiator: c.Collection}
					report.add("repo", "main", "agent-harness", "current", "synthetic harness already current", "", "", "")
					target := catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true, managed: true}
					c.catchUpHooks(&report, target, CatchUpOptions{ReloadStatus: true, InstallTools: true, DryRun: dry, NoEnv: true, NoSecrets: true, NoSkills: true, NoMCP: true}, false)
					calls, _ := os.ReadFile(filepath.Join(state, "pane.log"))
					var pane CatchUpRow
					for _, row := range report.Outcomes {
						if row.Kind == "pane" {
							pane = row
						}
					}
					data, _ := os.ReadFile(filepath.Join(state, "status.json"))
					var snapshot StatusSnapshot
					_ = json.Unmarshal(data, &snapshot)
					devRaw, _ := os.ReadFile(filepath.Join(state, "dev-status.json"))
					var diagnostic struct {
						Data struct {
							HandoverBypassed bool           `json:"handover_bypassed"`
							Follow           DevFollowState `json:"follow"`
						}
					}
					_ = json.Unmarshal(devRaw, &diagnostic)
					blocked := policy == "on" || policy == "auto-active" || policy == "invalid"
					if blocked {
						if pane.Outcome != "needs-owner" || strings.Contains(string(calls), "pane send-keys") || strings.Contains(string(calls), "pane run") {
							t.Errorf("FOLLOW GUARD missing: policy=%s outcome=%s stable_version=%s child_bypassed=%t child_mode=%s active=%t calls=%s", policy, pane.Outcome, snapshot.CLIVersion, diagnostic.Data.HandoverBypassed, diagnostic.Data.Follow.Mode, diagnostic.Data.Follow.Active, calls)
						}
						if report.ExitStatus != 1 {
							t.Error("skip lacks needs-owner exit status")
						}
					} else if dry {
						if pane.Outcome != "planned" || strings.Contains(string(calls), "pane run") || strings.Contains(string(calls), "pane send-keys") {
							t.Errorf("normal dry run changed pane: %+v %s", pane, calls)
						}
					} else {
						if pane.Outcome != "restarted" || snapshot.CLIVersion != "0.2.2" || !diagnostic.Data.HandoverBypassed {
							t.Errorf("installed stable control: %+v version=%s dev=%s calls=%s", pane, snapshot.CLIVersion, devRaw, calls)
						}
						child, _ := os.ReadFile(filepath.Join(state, "child-env"))
						if string(child) != "off|off\n" {
							t.Errorf("child bypass lost: %s", child)
						}
					}
					if policy == "on" && caller.name == "none" && dry {
						manual := CatchUpReport{Initiator: c.Collection}
						manual.add("repo", "main", "agent-harness", "current", "synthetic current target", "", "", "")
						c.catchUpHooks(&manual, target, CatchUpOptions{ReloadStatus: true, DryRun: true, NoEnv: true, NoSecrets: true, NoSkills: true, NoMCP: true}, false)
						if manual.Outcomes[len(manual.Outcomes)-1].Outcome != "planned" {
							t.Errorf("unforced manual reload changed: %+v", manual.Outcomes)
						}
					}
					if !bytes.Equal(before, catchUpDevPolicyBytes(t, c.Collection)) {
						t.Error("mode/lock bytes or presence changed")
					}
					if os.Getenv("TABA_DEV_BUILD") != caller.canonical || os.Getenv("WTC_DEV_BUILD") != caller.legacy {
						t.Error("caller bypass was changed")
					}
					if _, err := os.Stat(filepath.Join(state, "stale")); !os.IsNotExist(err) {
						t.Error("selected stale PATH binary")
					}
				})
			}
		}
	}
}

func catchUpDevPolicyBytes(t *testing.T, collection string) []byte {
	t.Helper()
	var all []byte
	for _, name := range []string{".wtc-dev-mode.json", ".taba-dev-mode.json", ".wtc-dev-mode.lock", ".taba-dev-mode.lock"} {
		raw, err := os.ReadFile(filepath.Join(collection, name))
		all = append(all, []byte(fmt.Sprintf("%s:%t:", name, err == nil))...)
		all = append(all, raw...)
	}
	return all
}
