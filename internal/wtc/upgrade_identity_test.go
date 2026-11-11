package wtc

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Build the real command for the pane fixture: returning argv from a mock alone
// would not demonstrate that installed code produced the recovered snapshot.
func TestUpgradeRolloutHandsStatusToInstalledCandidate(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a native CLI for the isolated installer/pane rehearsal")
	}
	candidate := filepath.Join(t.TempDir(), "candidate")
	build := exec.Command("go", "build", "-ldflags=-X main.version=0.2.2", "-o", candidate, "../../cmd/taba")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v\n%s", err, out)
	}
	for _, spelling := range []string{"wtc", "taba"} {
		for _, mode := range []string{"clean", "dirty", "in-progress", "install-failed", "env-failed", "installed-unavailable"} {
			t.Run(spelling+"/"+mode, func(t *testing.T) {
				c := newWorkspaceFixture(t)
				provider := newRenameProviderFixture(t, "github.com")
				registry, _ := os.ReadFile(filepath.Join(c.Harness, ".harness-repos.yml"))
				fixtureFile(t, filepath.Join(c.Harness, ".harness-repos.yml"), string(registry)+"  - name: reference\n    remote: https://github.com/example/new-name.git\n    default_ref: origin/main\n", 0644)
				fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.2.0\n", 0644)
				fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[compatibility]\nrequires = \">=0.2.0,<0.3\"\n", 0644)
				fixtureGit(t, "-C", c.Harness, "add", ".")
				fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "setup")
				// The starting pin/config is already published. Keep this history
				// identical for baseline and candidate, including the dry-run.
				source := filepath.Join(c.Workspace, "source-agent-harness")
				fixtureGit(t, "-C", source, "fetch", "-q", c.Harness, "HEAD")
				fixtureGit(t, "-C", source, "merge", "--ff-only", "FETCH_HEAD")
				fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "agent-harness.git"), "fetch", "-q", "origin")
				reopened, err := OpenCollection(c.Collection)
				if err != nil {
					t.Fatal(err)
				}
				c.Registry = reopened.Registry
				before := "reference 42 topic https://github.com/example/old-name/pull/42 Synthetic upgrade\n"
				fixtureFile(t, c.PRFile(), before, 0600)
				bin := filepath.Join(c.Workspace, "bin")
				state := t.TempDir()
				t.Setenv("REHEARSAL_STATE", state)
				t.Setenv("REHEARSAL_CANDIDATE", candidate)
				t.Setenv("REHEARSAL_COLLECTION", c.Collection)
				t.Setenv("REHEARSAL_SPELLING", spelling)
				t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
				t.Setenv("WTC_DEV_BUILD", "off")
				t.Setenv("HERDR_SESSION", "fixture")
				fixtureFile(t, filepath.Join(bin, "mise"), `#!/bin/sh
printf '%s\n' "$*" >> "$REHEARSAL_STATE/mise.log"
case "$1" in
 trust) [ ! -f "$REHEARSAL_STATE/env-failed" ] ;;
 install) [ ! -f "$REHEARSAL_STATE/install-failed" ] || exit 1; touch "$REHEARSAL_STATE/installed" ;;
 where)
   [ "$2" = github:lcorneliussen/taba@0.2.2 ] || exit 1
   [ -f "$REHEARSAL_STATE/installed" ] && [ ! -f "$REHEARSAL_STATE/installed-unavailable" ] || exit 1
   dirname "$REHEARSAL_CANDIDATE" ;;
 exec)
   [ "$2" = github:lcorneliussen/taba@0.2.2 ] || exit 1
   shift 4
   if [ "$1" = status ]; then
     [ "$MISE_AUTO_INSTALL" = 0 ] && [ "$MISE_EXEC_AUTO_INSTALL" = 0 ] || exit 98
     [ "$TABA_DEV_BUILD" = off ] && [ "$WTC_DEV_BUILD" = off ] || exit 98
   fi
   exec "$REHEARSAL_CANDIDATE" "$@" ;;
esac
`, 0755)
				for _, name := range []string{"wtc", "taba"} {
					fixtureFile(t, filepath.Join(bin, name), "#!/bin/sh\nprintf 'stale PATH selected\\n' >> \"$REHEARSAL_STATE/stale-path\"\nexit 99\n", 0755)
				}
				fixtureFile(t, filepath.Join(bin, "herdr"), `#!/bin/sh
printf '%s\n' "$*" >> "$REHEARSAL_STATE/pane.log"
shift 2
case "$1 $2" in
 'workspace list') printf '%s\n' '{"result":{"workspaces":[{"label":"main","workspace_id":"w1"}]}}' ;;
 'pane list') printf '%s\n' '{"result":{"panes":[{"label":"status","pane_id":"w1:p3"},{"label":"agent","pane_id":"w1:p1","agent":"codex"}]}}' ;;
 'pane process-info')
   if [ -f "$REHEARSAL_STATE/restarted" ]; then args='["taba","status","--tui"]'
   elif [ -f "$REHEARSAL_STATE/interrupted" ]; then args='["sh"]'
   else args="[\"$REHEARSAL_SPELLING\",\"status\",\"--tui\"]"; fi
   printf '{"result":{"process_info":{"foreground_process_group_id":42,"foreground_processes":[{"pid":42,"argv":%s}]}}}\n' "$args" ;;
 'pane send-keys') rm -f "$REHEARSAL_STATE/restarted"; touch "$REHEARSAL_STATE/interrupted" ;;
 'pane run')
   /bin/sh -c "$4" > "$REHEARSAL_STATE/status.stdout" || exit 1
   cp "$REHEARSAL_COLLECTION/.wtc-status.json" "$REHEARSAL_STATE/status.json" || exit 1
   touch "$REHEARSAL_STATE/restarted" ;;
esac
`, 0755)
				// Keep provider fixtures on PATH; only release lookups are handled here.
				providerGH := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0] + "/gh"
				t.Setenv("REHEARSAL_PROVIDER_GH", providerGH)
				fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
case "$*" in
 *releases/latest*|*releases/tags/v0.2.2*) printf 'v0.2.2\n' ;;
 *) exec "$REHEARSAL_PROVIDER_GH" "$@" ;;
esac
`, 0755)
				t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
				if mode == "clean" {
					dry := exec.Command(candidate, "upgrade", "--rollout", "--dry-run", "--json")
					dry.Dir = c.Collection
					if out, err := dry.CombinedOutput(); err != nil {
						t.Fatalf("rollout dry-run must not require an already installed target: %v\n%s", err, out)
					}
					if _, err := os.Stat(filepath.Join(state, "installed")); !os.IsNotExist(err) {
						t.Fatal("rollout dry-run installed tools")
					}
					calls, _ := os.ReadFile(filepath.Join(state, "pane.log"))
					if strings.Contains(string(calls), "pane send-keys") || strings.Contains(string(calls), "pane run") {
						t.Fatal("rollout dry-run changed a pane")
					}
					os.Remove(filepath.Join(state, "pane.log"))
				}
				// An optional released binary exercises the real bootstrap rather than
				// assuming the authoring version was already installed.
				from := os.Getenv("TABA_TEST_UPGRADE_FROM")
				if from == "" {
					from = candidate
				}
				if mode == "dirty" {
					configPath := filepath.Join(c.Harness, "wtc.toml")
					clean, _ := os.ReadFile(configPath)
					ownerConfig := string(clean) + "# existing owner edits\n"
					fixtureFile(t, configPath, ownerConfig, 0644)
					blocked := exec.Command(from, "upgrade", "--json")
					blocked.Dir = c.Collection
					if out, err := blocked.CombinedOutput(); err == nil || !strings.Contains(string(out), "uncommitted") {
						t.Fatalf("dirty released bootstrap should refuse: %v\n%s", err, out)
					}
					if data, _ := os.ReadFile(configPath); string(data) != ownerConfig {
						t.Fatal("dirty released bootstrap overwrote owner config")
					}
					if data, _ := os.ReadFile(c.PRFile()); string(data) != before {
						t.Fatal("refused upgrade repaired metadata")
					}
					if _, err := os.Stat(filepath.Join(state, "installed")); !os.IsNotExist(err) {
						t.Fatal("refused upgrade reached installer")
					}
					// Only the disposable fixture is reset to exercise the clean path.
					fixtureFile(t, configPath, string(clean), 0644)
				}
				proposal := exec.Command(from, "upgrade", "--json")
				proposal.Dir = c.Collection
				if out, err := proposal.CombinedOutput(); err != nil {
					t.Fatalf("upgrade proposal: %v\n%s", err, out)
				}
				if raw, _ := os.ReadFile(c.PRFile()); string(raw) != before {
					t.Fatal("release notes acquired unrelated repair side effects")
				}
				if _, err := os.Stat(filepath.Join(state, "pane.log")); !os.IsNotExist(err) {
					t.Fatal("pin proposal restarted a status pane before rollout")
				}
				// Simulate the reviewed harness pin reaching its local upstream.
				fixtureGit(t, "-C", c.Harness, "add", ".wtc-cli-version", "wtc.toml")
				fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "upgrade")
				fixtureGit(t, "-C", source, "fetch", "-q", c.Harness, "HEAD")
				fixtureGit(t, "-C", source, "merge", "--ff-only", "FETCH_HEAD")
				if mode == "dirty" {
					fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "# user edits\n", 0644)
				} else if mode == "in-progress" {
					gitdir := fixtureGit(t, "-C", c.Harness, "rev-parse", "--absolute-git-dir")
					fixtureFile(t, filepath.Join(gitdir, "MERGE_HEAD"), fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")+"\n", 0600)
				} else if mode != "clean" {
					fixtureFile(t, filepath.Join(state, mode), "", 0600)
				}
				head := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
				config, _ := os.ReadFile(filepath.Join(c.Harness, "wtc.toml"))
				runRollout := func() (CatchUpReport, error) {
					cmd := exec.Command(candidate, "upgrade", "--rollout", "--json")
					cmd.Dir = c.Collection
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					err := cmd.Run()
					var report CatchUpReport
					if parseErr := json.Unmarshal(stdout.Bytes(), &report); parseErr != nil {
						t.Fatalf("native rollout report unavailable: %v\n%s\n%s", parseErr, &stdout, &stderr)
					}
					return report, err
				}
				// Only the installed target invocation can have the new rollout
				// behavior. The old upgrade process never executes this code.
				report, runErr := runRollout()
				raw, _ := os.ReadFile(c.PRFile())
				if mode != "clean" {
					if mode == "dirty" || mode == "in-progress" {
						needsOwner := false
						for _, row := range report.Outcomes {
							needsOwner = needsOwner || (row.Kind == "repo" && row.Outcome == "needs-owner")
						}
						if !needsOwner {
							t.Fatalf("owned target lacks truthful needs-owner outcome: %+v", report.Outcomes)
						}
					}
					calls, _ := os.ReadFile(filepath.Join(state, "pane.log"))
					if string(raw) != before || strings.Contains(string(calls), "pane send-keys") || strings.Contains(string(calls), "pane run") {
						t.Fatalf("unusable/owned target was restarted or repaired: %+v %v", report.Outcomes, runErr)
					}
					configAfter, _ := os.ReadFile(filepath.Join(c.Harness, "wtc.toml"))
					if string(configAfter) != string(config) || fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD") != head {
						t.Fatal("user config or in-progress HEAD changed")
					}
					return
				}
				if runErr != nil || string(raw) != strings.Replace(before, "old-name", "new-name", 1) {
					t.Fatalf("rollout failed to recover metadata: %+v %v", report.Outcomes, runErr)
				}
				if _, err := os.Stat(filepath.Join(state, "stale-path")); !os.IsNotExist(err) {
					t.Fatal("rollout selected a stale PATH binary instead of the installed pin")
				}
				var snapshot StatusSnapshot
				data, _ := os.ReadFile(filepath.Join(state, "status.json"))
				if json.Unmarshal(data, &snapshot) != nil || snapshot.CLIVersion != "0.2.2" || len(snapshot.PRs) != 1 || snapshot.PRs[0].State != "OPEN" {
					t.Fatalf("candidate did not produce recovered status: %s", data)
				}
				calls, _ := os.ReadFile(filepath.Join(state, "pane.log"))
				if strings.Contains(string(calls), "pane run w1:p1") || !strings.Contains(string(calls), "pane run w1:p3") {
					t.Fatalf("wrong pane handover: %s", calls)
				}
				provider.put(t, "fail", "")
				if _, err := runRollout(); err != nil {
					t.Fatalf("repeat rollout: %v", err)
				}
				if after, _ := os.ReadFile(c.PRFile()); string(after) != string(raw) {
					t.Fatal("repeat rollout changed repaired metadata")
				}
			})
		}
	}
}

func TestUpgradeDirtySelectedInputsPreservesBothShapes(t *testing.T) {
	for _, shape := range []string{"legacy", "current", "both"} {
		t.Run(shape, func(t *testing.T) {
			c, bin := upgradeFixture(t)
			config, pin := "wtc.toml", ".wtc-cli-version"
			if shape != "legacy" {
				fixtureFile(t, filepath.Join(c.Harness, "taba.toml"), "[compatibility]\nrequires = \">=0.1.9,<0.2\"\n", 0644)
				fixtureFile(t, filepath.Join(c.Harness, ".taba-version"), "0.1.9\n", 0644)
			}
			if shape == "current" {
				if err := os.Remove(filepath.Join(c.Harness, config)); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(c.Harness, pin)); err != nil {
					t.Fatal(err)
				}
				config, pin = "taba.toml", ".taba-version"
			}
			fixtureGit(t, "-C", c.Harness, "add", "-A")
			fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "selected inputs")
			// Match an existing tracked configuration with owner changes.
			selected, _ := os.ReadFile(filepath.Join(c.Harness, config))
			fixtureFile(t, filepath.Join(c.Harness, config), string(selected)+"# owner edits\n", 0644)
			fixtureFile(t, filepath.Join(bin, "mise"), "#!/bin/sh\ntouch \"$REHEARSAL_INSTALL_CALLED\"\n", 0755)
			called := filepath.Join(t.TempDir(), "called")
			t.Setenv("REHEARSAL_INSTALL_CALLED", called)
			before := map[string]string{}
			for _, name := range []string{"wtc.toml", "taba.toml", ".wtc-cli-version", ".taba-version"} {
				data, err := os.ReadFile(filepath.Join(c.Harness, name))
				if err == nil {
					before[name] = string(data)
				}
			}
			for _, dry := range []bool{true, false} {
				if _, err := c.Upgrade(UpgradeOptions{DryRun: dry}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
					t.Fatalf("dirty selected inputs must refuse: %v", err)
				}
			}
			for name, want := range before {
				if got, _ := os.ReadFile(filepath.Join(c.Harness, name)); string(got) != want {
					t.Fatalf("dirty upgrade changed %s", name)
				}
			}
			if _, err := os.Stat(called); !os.IsNotExist(err) {
				t.Fatal("dirty upgrade reached installer")
			}
		})
	}
}

func TestAdHocConfigPrecedenceAllowsStatusRepairButRefusesHarnessUpgrade(t *testing.T) {
	for _, namespace := range []string{".wtc", ".taba"} {
		t.Run(namespace, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			newRenameProviderFixture(t, "github.com")
			// A harness sibling alone does not decide whether this is managed.
			fixtureFile(t, filepath.Join(c.Collection, namespace, "config", "repos.yml"), "repos:\n  - name: reference\n    remote: https://github.com/example/new-name.git\n    default_ref: origin/main\n", 0644)
			fixtureFile(t, filepath.Join(c.Collection, ".wtc", "skills", "placeholder"), "generated legacy namespace\n", 0644)
			selected, err := OpenCollection(c.Collection)
			if err != nil || !selected.AdHoc || selected.Harness != filepath.Join(c.Collection, namespace, "config") {
				t.Fatalf("wrong config geometry: %+v %v", selected, err)
			}
			old := "reference 42 topic https://github.com/example/old-name/pull/42 Ad-hoc\n"
			fixtureFile(t, filepath.Join(c.Collection, ".taba-prs"), old, 0600)
			if _, err := selected.Upgrade(UpgradeOptions{}); err == nil || !strings.Contains(err.Error(), "ad-hoc") {
				t.Fatalf("ad-hoc upgrade bootstrap limitation hidden: %v", err)
			}
			if raw, _ := os.ReadFile(selected.PRFile()); string(raw) != old {
				t.Fatal("refused ad-hoc upgrade repaired metadata")
			}
			snapshot, err := selected.statusForgePreview(false, true)
			if err != nil || len(snapshot.PRs) != 1 || snapshot.PRs[0].State != "OPEN" || snapshot.PRs[0].Slug != "example/new-name" {
				t.Fatalf("status must recover independently of harness upgrade: %+v %v", snapshot.PRs, err)
			}
		})
	}
}
