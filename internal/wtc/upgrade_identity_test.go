package wtc

import (
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
		for _, mode := range []string{"clean", "dirty", "in-progress", "install-failed", "env-failed"} {
			t.Run(spelling+"/"+mode, func(t *testing.T) {
				c := newWorkspaceFixture(t)
				provider := newRenameProviderFixture(t, "github.com")
				registry, _ := os.ReadFile(filepath.Join(c.Harness, ".harness-repos.yml"))
				fixtureFile(t, filepath.Join(c.Harness, ".harness-repos.yml"), string(registry)+"  - name: reference\n    remote: https://github.com/example/new-name.git\n    default_ref: origin/main\n", 0644)
				fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.2.0\n", 0644)
				fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[compatibility]\nrequires = \">=0.2.0,<0.3\"\n", 0644)
				fixtureGit(t, "-C", c.Harness, "add", ".")
				fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "setup")
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
 exec) shift 3; exec "$@" ;;
esac
`, 0755)
				for _, name := range []string{"wtc", "taba"} {
					fixtureFile(t, filepath.Join(bin, name), "#!/bin/sh\n[ -f \"$REHEARSAL_STATE/installed\" ] || exit 99\nexec \"$REHEARSAL_CANDIDATE\" \"$@\"\n", 0755)
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
   else args='["wtc","status","--tui"]'; fi
   printf '{"result":{"process_info":{"foreground_process_group_id":42,"foreground_processes":[{"pid":42,"argv":%s}]}}}\n' "$args" ;;
 'pane send-keys') rm -f "$REHEARSAL_STATE/restarted"; touch "$REHEARSAL_STATE/interrupted" ;;
 'pane run')
   cd "$REHEARSAL_COLLECTION" || exit 1
   "$REHEARSAL_SPELLING" status --no-fetch --json > "$REHEARSAL_STATE/status.json" || exit 1
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
				if _, err := c.Upgrade(UpgradeOptions{}); err != nil {
					t.Fatal(err)
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
				source := filepath.Join(c.Workspace, "source-agent-harness")
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
				report, runErr := c.CatchUp(RolloutOptions(false))
				raw, _ := os.ReadFile(c.PRFile())
				if mode != "clean" {
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
				if _, err := c.CatchUp(RolloutOptions(false)); err != nil {
					t.Fatalf("repeat rollout: %v", err)
				}
				if after, _ := os.ReadFile(c.PRFile()); string(after) != string(raw) {
					t.Fatal("repeat rollout changed repaired metadata")
				}
			})
		}
	}
}
