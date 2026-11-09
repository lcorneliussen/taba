package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatibilityRangeAndRewrite(t *testing.T) {
	for version, want := range map[string]string{"0.1.54": ">=0.1.54,<0.2", "v1.9.0": ">=1.9.0,<1.10", "2.0.3": ">=2.0.3,<2.1"} {
		if got, err := CompatibilityRange(version); err != nil || got != want {
			t.Fatalf("%s: %q %v", version, got, err)
		}
	}
	if _, err := CompatibilityRange("0.1"); err == nil {
		t.Fatal("partial version accepted")
	}
	config := "[other]\nrequires = \"keep\"\n\n[compatibility] # pin range\nrequires = \">=0.1.41,<0.2\" # moved by wtc upgrade\r\n\n[status]\nreturn_merged = false\n"
	got, err := replaceCompatibility(config, ">=0.1.54,<0.2")
	if err != nil {
		t.Fatal(err)
	}
	want := "[other]\nrequires = \"keep\"\n\n[compatibility] # pin range\nrequires = \">=0.1.54,<0.2\" # moved by wtc upgrade\r\n\n[status]\nreturn_merged = false\n"
	if got != want {
		t.Fatalf("rewrite changed more than the requires line:\n%q", got)
	}
	if _, err := replaceCompatibility("[other]\nrequires = \"x\"\n", ">=0.1.54,<0.2"); err == nil {
		t.Fatal("missing [compatibility] requires was not reported")
	}
}

// upgradeFixture commits a wtc.toml into the fixture harness and puts a fake
// gh on PATH that publishes 0.1.9 to 0.1.12, a prerelease 0.1.12-rc.1 and a
// draft 0.1.13.
func upgradeFixture(t *testing.T) (*Context, string) {
	t.Helper()
	c := newWorkspaceFixture(t)
	fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[compatibility]\nrequires = \">=0.1.9,<0.2\"\n", 0644)
	fixtureGit(t, "-C", c.Harness, "add", "wtc.toml")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "config")
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
case "$*" in
  *releases/latest*) echo v0.1.12 ;;
  *releases/tags/v0.1.1[0-2]*|*releases/tags/v0.1.12-rc.1*) echo ok ;;
  *releases/tags/*) exit 1 ;;
  *releases\?per_page=100*)
    for v in 9 10 11 12; do printf '{"tag_name":"v0.1.%s","body":"# wtc 0.1.%s\\n","draft":false,"prerelease":false}\n' $v $v; done
    printf '%s\n' '{"tag_name":"v0.1.12-rc.1","body":"# wtc 0.1.12-rc.1\n","draft":false,"prerelease":true}'
    printf '%s\n' '{"tag_name":"v0.1.13","body":"draft","draft":true,"prerelease":false}' ;;
  *) exit 2 ;;
esac
`, 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	return c, bin
}

func TestUpgradeBumpsPinAndRangeWithPublishedNotes(t *testing.T) {
	c, _ := upgradeFixture(t)
	pin := filepath.Join(c.Harness, ".wtc-cli-version")
	config := filepath.Join(c.Harness, "wtc.toml")

	dry, err := c.Upgrade(UpgradeOptions{DryRun: true})
	if err != nil || dry.To != "0.1.12" || dry.Requires != ">=0.1.12,<0.2" || len(dry.Notes) != 3 || dry.Notes[0].Version != "0.1.10" {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if data, _ := os.ReadFile(pin); string(data) != "0.1.9\n" {
		t.Fatal("dry run wrote the pin")
	}

	if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.99"}); err == nil || !strings.Contains(err.Error(), "not a published release") {
		t.Fatalf("unpublished version accepted: %v", err)
	}
	if data, _ := os.ReadFile(pin); string(data) != "0.1.9\n" {
		t.Fatal("unpublished version wrote the pin")
	}

	result, err := c.Upgrade(UpgradeOptions{Version: "v0.1.11"})
	if err != nil {
		t.Fatal(err)
	}
	if result.From != "0.1.9" || result.To != "0.1.11" || result.NotesSource != "GitHub releases" || !result.NotInstalled ||
		len(result.Notes) != 2 || result.Notes[1].Content != "# wtc 0.1.11\n" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if data, _ := os.ReadFile(pin); string(data) != "0.1.11\n" {
		t.Fatalf("pin not written: %q", data)
	}
	if data, _ := os.ReadFile(config); !strings.Contains(string(data), `requires = ">=0.1.11,<0.2"`) {
		t.Fatalf("range not written: %s", data)
	}
	if data, _ := os.ReadFile(filepath.Join(c.Collection, "mise.toml")); !strings.Contains(string(data), `"`+cliMiseTool+`" = "0.1.11"`) {
		t.Fatalf("collection mise config not refreshed: %s", data)
	}

	// The bump is left uncommitted for review, so a second run refuses it.
	if _, err := c.Upgrade(UpgradeOptions{}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty pin not refused: %v", err)
	}
	fixtureGit(t, "-C", c.Harness, "checkout", "--", ".wtc-cli-version", "wtc.toml")
	if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.9"}); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("same version accepted: %v", err)
	}
}

func TestUpgradeToPrereleaseReadsItsOwnNotes(t *testing.T) {
	c, _ := upgradeFixture(t)
	dry, err := c.Upgrade(UpgradeOptions{DryRun: true, Version: "0.1.12-rc.1"})
	if err != nil || dry.To != "0.1.12-rc.1" || len(dry.Notes) != 3 || dry.Notes[2].Version != "0.1.12-rc.1" || dry.Notes[1].Version != "0.1.11" {
		t.Fatalf("prerelease target: %+v %v", dry, err)
	}
	// A prerelease that is not the target stays out of the notes.
	dry, err = c.Upgrade(UpgradeOptions{DryRun: true})
	if err != nil || dry.To != "0.1.12" || len(dry.Notes) != 3 || dry.Notes[2].Version != "0.1.12" {
		t.Fatalf("stable target: %+v %v", dry, err)
	}
}

func TestUpgradeReadsTargetBinaryNotesThroughMise(t *testing.T) {
	c, bin := upgradeFixture(t)
	log := filepath.Join(c.Workspace, "mise.log")
	t.Setenv("MOCK_MISE_LOG", log)
	fixtureFile(t, filepath.Join(bin, "mise"), `#!/bin/sh
printf '%s|%s\n' "$*" "$WTC_DEV_BUILD" >> "$MOCK_MISE_LOG"
case "$1" in
  exec) printf '%s\n' '{"ok":true,"data":[{"version":"0.1.10","content":"corrected 0.1.10\n"},{"version":"0.1.12","content":"0.1.12\n"}]}' ;;
esac
`, 0755)
	result, err := c.Upgrade(UpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.NotesSource != "wtc 0.1.12 release-notes" || result.NotInstalled || len(result.Notes) != 2 || result.Notes[0].Content != "corrected 0.1.10\n" {
		t.Fatalf("target binary notes not used: %+v", result)
	}
	data, _ := os.ReadFile(log)
	for _, want := range []string{"install|", "exec " + cliMiseTool + "@0.1.12 -- wtc release-notes --since 0.1.9 --json|off"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("mise not called with %q:\n%s", want, data)
		}
	}
}

func TestUpgradeRefusesAdHocCollection(t *testing.T) {
	c := &Context{AdHoc: true}
	if _, err := c.Upgrade(UpgradeOptions{}); err == nil || !strings.Contains(err.Error(), "harness collection") {
		t.Fatalf("ad-hoc collection not refused: %v", err)
	}
}

func TestTabaUpgradePreservesSelectedPinAndConfigNames(t *testing.T) {
	for _, pinName := range []string{".wtc-cli-version", ".taba-version"} {
		for _, configName := range []string{"wtc.toml", "taba.toml"} {
			t.Run(pinName+"/"+configName, func(t *testing.T) {
				c, _ := upgradeFixture(t)
				for _, pair := range [][2]string{{".wtc-cli-version", pinName}, {"wtc.toml", configName}} {
					if pair[0] != pair[1] {
						fixtureGit(t, "-C", c.Harness, "mv", pair[0], pair[1])
					}
				}
				fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "input names")
				pin := filepath.Join(c.Harness, pinName)
				dry, err := c.Upgrade(UpgradeOptions{Version: "0.1.11", DryRun: true})
				if err != nil || strings.Join(dry.Files, ",") != pinName+","+configName {
					t.Fatalf("dry-run paths: %+v %v", dry, err)
				}
				if data, _ := os.ReadFile(pin); string(data) != "0.1.9\n" {
					t.Fatal("dry run modified the selected pin")
				}
				result, err := c.Upgrade(UpgradeOptions{Version: "0.1.11"})
				if err != nil || strings.Join(result.Files, ",") != pinName+","+configName {
					t.Fatalf("upgrade paths: %+v %v", result, err)
				}
				if data, _ := os.ReadFile(pin); string(data) != "0.1.11\n" {
					t.Fatalf("selected pin not updated: %q", data)
				}
				if data, _ := os.ReadFile(filepath.Join(c.Harness, configName)); !strings.Contains(string(data), `requires = ">=0.1.11,<0.2"`) {
					t.Fatalf("selected config not updated: %s", data)
				}
				for _, name := range []string{".wtc-cli-version", ".taba-version", "wtc.toml", "taba.toml"} {
					if name != pinName && name != configName {
						if _, err := os.Lstat(filepath.Join(c.Harness, name)); !os.IsNotExist(err) {
							t.Fatalf("upgrade created a shadow file %s", name)
						}
					}
				}
				if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.12"}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
					t.Fatalf("dirty selected files were not protected: %v", err)
				}
				fixtureGit(t, "-C", c.Harness, "add", "--", pinName, configName)
				fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "upgraded inputs")
				original := map[string]string{}
				for _, name := range []string{pinName, configName} {
					data, err := os.ReadFile(filepath.Join(c.Harness, name))
					if err != nil {
						t.Fatal(err)
					}
					original[name] = string(data)
				}
				for _, dirty := range []string{pinName, configName} {
					edited := original[dirty] + "\n# local edit\n"
					if dirty == pinName {
						edited = "0.1.10\n"
					}
					fixtureFile(t, filepath.Join(c.Harness, dirty), edited, 0644)
					if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.12"}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
						t.Fatalf("dirty %s alone was not protected: %v", dirty, err)
					}
					for _, name := range []string{pinName, configName} {
						want := original[name]
						if name == dirty {
							want = edited
						}
						if data, err := os.ReadFile(filepath.Join(c.Harness, name)); err != nil || string(data) != want {
							t.Fatalf("refused upgrade modified %s: %q %v", name, data, err)
						}
					}
					fixtureFile(t, filepath.Join(c.Harness, dirty), original[dirty], 0644)
				}
			})
		}
	}
}

func TestCatchUpIdleOnlyLeavesBusyAgentsAndInstallsTools(t *testing.T) {
	c := newWorkspaceFixture(t)
	for _, slug := range []string{"busy", "stuck", "quiet"} {
		if _, err := c.NewCollection(NewOptions{Slug: slug}); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(c.Workspace, "bin")
	log := filepath.Join(c.Workspace, "tools.log")
	fixtureFile(t, filepath.Join(bin, "herdr"), `#!/bin/sh
shift 2
case "$1 $2" in
  'workspace list')
    if [ -n "$MOCK_HERDR_DOWN" ]; then printf '%s\n' '{"error":{"code":"server_not_running"}}' >&2; exit 1; fi
    if [ -n "$MOCK_HERDR_BROKEN" ]; then printf '%s\n' '{"error":{"code":"internal"}}' >&2; exit 1; fi
    printf '%s\n' '{"result":{"workspaces":[{"label":"main","workspace_id":"w1"},{"label":"busy","workspace_id":"w2"},{"label":"quiet","workspace_id":"w3"},{"label":"stuck","workspace_id":"w4"}]}}' ;;
  'pane list')
    case "$4" in
      w1|w2) printf '%s\n' '{"result":{"panes":[{"label":"agent","pane_id":"x:p1","agent":"claude","agent_status":"working"}]}}' ;;
      w4) printf '%s\n' '{"result":{"panes":[{"label":"agent","pane_id":"w4:p1","agent":"codex","agent_status":"blocked"}]}}' ;;
      *) printf '%s\n' '{"result":{"panes":[{"label":"agent","pane_id":"x:p1","agent":"claude","agent_status":"idle"}]}}' ;;
    esac ;;
esac
`, 0755)
	fixtureFile(t, filepath.Join(bin, "mise"), "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$PWD\" >> \""+log+"\"\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HERDR_SESSION", "fixture")
	// Unlike an agent-initiated sweep, an interactive status action checks
	// the initiating collection's owner too.
	if busy, err := c.AgentBusy(); err != nil || busy != "working" {
		t.Fatalf("initiating owner was not checked: %q, %v", busy, err)
	}
	stuck, err := OpenCollection(filepath.Join(c.Workspace, "stuck"))
	if err != nil {
		t.Fatal(err)
	}
	if busy, err := stuck.AgentBusy(); err != nil || busy != "blocked" {
		t.Fatalf("blocked owner was not checked: %q, %v", busy, err)
	}
	opt := CatchUpOptions{All: true, Repos: []string{"harness"}, CleanOnly: true, IdleOnly: true, InstallTools: true, NoSkills: true, NoMCP: true, NoSecrets: true}
	report, _ := c.CatchUp(opt)
	outcomes := map[string]string{}
	for _, row := range report.Outcomes {
		if row.Kind == "repo" {
			outcomes[row.Collection] = row.Outcome + ": " + row.Reason
		}
		if row.Kind == "hook" && row.Repo == "tools" && row.Outcome != "ok" {
			t.Fatalf("tools hook failed: %+v", row)
		}
	}
	if !strings.HasPrefix(outcomes["busy"], "needs-owner: agent working") || !strings.HasPrefix(outcomes["stuck"], "needs-owner: agent blocked") {
		t.Fatalf("busy agents' collections were not left alone: %v", outcomes)
	}
	// main is the initiating collection: its working agent is the one sweeping.
	for _, name := range []string{"main", "quiet"} {
		if strings.HasPrefix(outcomes[name], "needs-owner") || outcomes[name] == "" {
			t.Fatalf("%s should have been caught up: %v", name, outcomes)
		}
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "install "+filepath.Join(c.Workspace, "quiet")) || strings.Contains(string(data), "install "+filepath.Join(c.Workspace, "busy")) {
		t.Fatalf("mise install ran in the wrong collections:\n%s", data)
	}

	// Without a running herdr session no agent can be busy.
	quick := CatchUpOptions{Collections: []string{"busy"}, Repos: []string{"harness"}, IdleOnly: true, DryRun: true, NoSkills: true, NoMCP: true, NoSecrets: true, NoEnv: true}
	t.Setenv("MOCK_HERDR_DOWN", "1")
	report, _ = c.CatchUp(quick)
	for _, row := range report.Outcomes {
		if row.Kind == "repo" && row.Outcome == "needs-owner" {
			t.Fatalf("stopped herdr session blocked the sweep: %+v", row)
		}
	}
	// Any other herdr failure leaves the agent's state unknown, so its owner decides.
	t.Setenv("MOCK_HERDR_DOWN", "")
	t.Setenv("MOCK_HERDR_BROKEN", "1")
	report, _ = c.CatchUp(quick)
	unknown := false
	for _, row := range report.Outcomes {
		unknown = unknown || row.Kind == "repo" && row.Outcome == "needs-owner" && strings.HasPrefix(row.Reason, "agent unknown")
	}
	if !unknown {
		t.Fatalf("unknown agent state was not left to the owner: %+v", report.Outcomes)
	}
}

func TestCatchUpInstallToolsReportsMissingAndFailedMise(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.NewCollection(NewOptions{Slug: "other"}); err != nil {
		t.Fatal(err)
	}
	opt := CatchUpOptions{Collections: []string{"other"}, Repos: []string{"harness"}, InstallTools: true, NoSkills: true, NoMCP: true, NoSecrets: true}
	tools := func() CatchUpRow {
		report, _ := c.CatchUp(opt)
		for _, row := range report.Outcomes {
			if row.Repo == "tools" {
				return row
			}
		}
		t.Fatalf("no tools row: %+v", report.Outcomes)
		return CatchUpRow{}
	}
	if row := tools(); row.Outcome != "skipped" || row.Reason != "mise not installed" {
		t.Fatalf("missing mise not reported: %+v", row)
	}
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "mise"), `#!/bin/sh
[ "$1" = install ] && { echo 'no such release' >&2; exit 1; }
exit 0
`, 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if row := tools(); row.Outcome != "failed" || !strings.Contains(row.Reason, "no such release") {
		t.Fatalf("failed install not reported: %+v", row)
	}
}

func TestRolloutOptionsSweepHarnessesOfIdleCleanCollections(t *testing.T) {
	got := RolloutOptions(true)
	if !got.All || len(got.Repos) != 1 || got.Repos[0] != "harness" || !got.CleanOnly || !got.IdleOnly || !got.InstallTools || !got.DryRun ||
		got.NoEnv || got.NoSkills || got.NoMCP || got.NoSecrets || len(got.Collections) != 0 {
		t.Fatalf("unexpected rollout sweep: %+v", got)
	}
}

func TestUpgradeKeepsPinWhenNotesAreUnavailable(t *testing.T) {
	c, bin := upgradeFixture(t)
	// Releases resolve, but the listing the notes come from fails.
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
case "$*" in *releases/latest*) echo v0.1.12 ;; *) exit 1 ;; esac
`, 0755)
	result, err := c.Upgrade(UpgradeOptions{})
	if err != nil || result.To != "0.1.12" || result.NotesError == "" {
		t.Fatalf("notes failure undid the upgrade: %+v %v", result, err)
	}
	if data, _ := os.ReadFile(filepath.Join(c.Harness, ".wtc-cli-version")); string(data) != "0.1.12\n" {
		t.Fatalf("pin not kept: %q", data)
	}
	fixtureGit(t, "-C", c.Harness, "checkout", "--", ".wtc-cli-version", "wtc.toml")
	if _, err := c.Upgrade(UpgradeOptions{DryRun: true}); err == nil {
		t.Fatal("a dry run without notes should fail: it has nothing else to show")
	}
}
