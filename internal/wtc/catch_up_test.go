package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taba.sh/taba/internal/gitref"
)

func TestCatchUpInventoryUsesTargetIdentityAndRejectsUnknownSelector(t *testing.T) {
	c := newWorkspaceFixture(t)
	result, err := c.NewCollection(NewOptions{Slug: "other", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Collection == "" {
		t.Fatal("collection was not created")
	}
	t.Setenv("WTC_HARNESS_REPO", "widget") // Inherited identity must not redirect harness.
	report, targets, err := c.CatchUpInventory(CatchUpOptions{Collections: []string{"missing", "other"}, Repos: []string{"harness"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].repo != "agent-harness" || !targets[0].harness || targets[0].collection != "other" {
		t.Fatalf("wrong selected target: %+v", targets)
	}
	if report.ExitStatus != 1 || len(report.Outcomes) != 1 || report.Outcomes[0].Kind != "collection" {
		t.Fatalf("missing collection was not reported independently: %+v", report)
	}
	before := fixtureGit(t, "-C", targets[0].path, "rev-parse", "HEAD")
	report, targets, err = c.CatchUpInventory(CatchUpOptions{Collections: []string{"other"}, Repos: []string{"typo"}})
	if err == nil || len(targets) != 0 || len(report.Outcomes) != 1 || report.Outcomes[0].Kind != "selection" {
		t.Fatalf("selector typo did not stop the sweep: %+v %+v %v", report, targets, err)
	}
	if after := fixtureGit(t, "-C", filepath.Join(result.Collection, "harness"), "rev-parse", "HEAD"); after != before {
		t.Fatal("preflight changed a worktree")
	}
	reportPath := filepath.Join(c.Collection, "preflight.json")
	report, err = c.CatchUp(CatchUpOptions{Collections: []string{"other"}, Repos: []string{"typo"}, DryRun: true, Report: reportPath})
	if err == nil || report.ExitStatus != 1 {
		t.Fatalf("preflight failure was hidden: %+v %v", report, err)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("preflight report was not saved: %v", err)
	}
}

func TestCatchUpUsesOpenPRMergeTarget(t *testing.T) {
	c := newWorkspaceFixture(t)
	for _, tc := range []struct {
		pr   catchUpPR
		want string
		err  bool
	}{
		{catchUpPR{state: "NONE"}, "refs/remotes/origin/main", false},
		{catchUpPR{state: "MERGED"}, "refs/remotes/origin/main", false},
		{catchUpPR{state: "OPEN", base: "release/next"}, "refs/remotes/origin/release/next", false},
		{catchUpPR{state: "DRAFT", base: "develop"}, "refs/remotes/origin/develop", false},
		{catchUpPR{state: "OPEN"}, "", true},
		{catchUpPR{state: "OPEN", base: "../../bad"}, "", true},
	} {
		got, err := catchUpRefForPR(c.Harness, gitref.RemoteBranch("origin", "main"), tc.pr)
		if got.String() != tc.want || (err != nil) != tc.err {
			t.Fatalf("PR %+v: ref %q, error %v", tc.pr, got, err)
		}
	}
}

func TestCatchUpHandsOffMissingOpenPRTarget(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	before := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@github.com:example/agent-harness.git")
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"), "agent-harness 7 topic - fixture\n", 0644)
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' '{\"state\":\"OPEN\",\"baseRefName\":\"release/missing\"}'\n", 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	report, _ := c.CatchUp(CatchUpOptions{Repos: []string{"harness"}, DryRun: true, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
	for _, row := range report.Outcomes {
		if row.Kind == "repo" {
			if row.Outcome != "needs-owner" || row.TargetRef != "origin/release/missing" || row.NextAction == "" ||
				!strings.Contains(row.Reason, "merge target") {
				t.Fatalf("missing PR target not handed off: %+v", row)
			}
			if after := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD"); after != before {
				t.Fatal("missing PR target moved the branch")
			}
			return
		}
	}
	t.Fatalf("missing repo outcome: %+v", report)
}

func TestCatchUpInventorySelectsSymlinkedWorktreeDirectory(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+owner, "worktree", "add", "--detach", filepath.Join(c.Collection, "widget-real"), "origin/main")
	if err := os.Symlink("widget-real", filepath.Join(c.Collection, "widget-link")); err != nil {
		t.Fatal(err)
	}
	report, targets, err := c.CatchUpInventory(CatchUpOptions{Repos: []string{"widget-link"}, DryRun: true})
	if err != nil || report.ExitStatus != 0 || len(targets) != 1 || filepath.Base(targets[0].path) != "widget-link" {
		t.Fatalf("symlinked worktree was not selected: %+v %+v %v", report, targets, err)
	}
}

func TestCatchUpReportsUnopenableTargetActions(t *testing.T) {
	c := newWorkspaceFixture(t)
	target := catchUpTarget{collection: "missing", path: filepath.Join(c.Workspace, "missing", "harness"), repo: "agent-harness", harness: true, managed: true}
	report := CatchUpReport{Initiator: c.Collection}
	c.catchUpHooks(&report, target, CatchUpOptions{}, false)
	if report.ExitStatus == 0 {
		t.Fatalf("unopenable target did not fail catch-up: %+v", report.Outcomes)
	}
	for _, name := range []string{"secrets:agent-harness", "env", "skills"} {
		found := false
		for _, row := range report.Outcomes {
			if row.Kind == "hook" && row.Repo == name && row.Outcome == "failed" && strings.Contains(row.Reason, "cannot open target collection") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing failed %s action: %+v", name, report.Outcomes)
		}
	}
}

func TestCatchUpReportKeepsRowsWhenPersistenceFails(t *testing.T) {
	c := newWorkspaceFixture(t)
	report := CatchUpReport{SchemaVersion: 1, Initiator: c.Collection, Outcomes: []CatchUpRow{}}
	report.add("repo", "main", "widget", "needs-owner", "café | 日本", "old", "new", "old")
	report.add("repo", "main", "widget", "needs-owner", "merge conflict; aborted to original tree; paths: README.md", "old", "new", "old")
	if report.ExitStatus != 1 || !strings.Contains(report.Markdown(), "café \\| 日本") {
		t.Fatalf("incomplete readable report: %+v", report)
	}
	if !strings.Contains(report.Markdown(), "resolve conflicts") || !strings.Contains(report.Outcomes[1].NextAction, "push only if its PR is open or draft") {
		t.Fatalf("conflict handoff missing: %+v", report)
	}
	path := filepath.Join(c.Collection, "report.json")
	if err := SaveCatchUpReport(&report, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted CatchUpReport
	if err := json.Unmarshal(data, &persisted); err != nil || len(persisted.Outcomes) != 2 {
		t.Fatalf("unreadable persisted report: %v", err)
	}
	blockedPath := filepath.Join(c.Collection, "blocked.json")
	if err := os.Mkdir(blockedPath+".md", 0755); err != nil {
		t.Fatal(err)
	}
	if err := SaveCatchUpReport(&report, blockedPath); err == nil {
		t.Fatal("failed Markdown write was hidden")
	}
	data, err = os.ReadFile(blockedPath)
	if err != nil || json.Unmarshal(data, &persisted) != nil || persisted.ReportError == "" || persisted.ExitStatus != 1 {
		t.Fatalf("saved JSON did not record Markdown failure: %q %v", data, err)
	}
	if err := SaveCatchUpReport(&report, filepath.Join(c.Collection, "missing", "report.json")); err == nil || report.ReportError == "" {
		t.Fatal("report persistence failure was hidden")
	}
}

func TestCatchUpUpdatesDirtyLocalTreeAndReportsPartialFailure(t *testing.T) {
	c := newWorkspaceFixture(t)
	source := filepath.Join(c.Workspace, "source-agent-harness")
	fixtureFile(t, filepath.Join(source, "README.md"), "new upstream content\n", 0644)
	fixtureGit(t, "-C", source, "add", "README.md")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "upstream")
	want := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureFile(t, filepath.Join(c.Harness, "local-note"), "keep me\n", 0644)
	report, err := c.CatchUp(CatchUpOptions{Repos: []string{"harness"}, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
	if err != nil || report.ExitStatus != 0 {
		t.Fatalf("local catch-up failed: %+v %v", report, err)
	}
	if got := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD"); got != want {
		t.Fatalf("harness stayed at %s, want %s", got, want)
	}
	if got, err := os.ReadFile(filepath.Join(c.Harness, "local-note")); err != nil || string(got) != "keep me\n" {
		t.Fatalf("dirty change was lost: %q %v", got, err)
	}
	if got := fixtureGit(t, "-C", c.Harness, "stash", "list"); got != "" {
		t.Fatalf("stash was not restored: %s", got)
	}
	if _, err := os.Stat(filepath.Join(c.Collection, ".wtc-catch-up.json.md")); err != nil {
		t.Fatalf("missing readable report: %v", err)
	}

	// A failed owner fetch must not consume stale refs, and the next selected
	// repository still receives a result row.
	widgetOwner := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+widgetOwner, "remote", "set-url", "origin", filepath.Join(c.Workspace, "missing"))
	fixtureGit(t, "--git-dir="+widgetOwner, "worktree", "add", "--detach", filepath.Join(c.Collection, "widget"), "origin/main")
	report, err = c.CatchUp(CatchUpOptions{Repos: []string{"harness", "widget"}, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
	if err == nil || report.ExitStatus != 1 {
		t.Fatalf("partial failure was hidden: %+v %v", report, err)
	}
	seenHarness, seenWidget := false, false
	for _, row := range report.Outcomes {
		if row.Kind == "repo" && row.Repo == "agent-harness" && row.Outcome == "current" {
			seenHarness = true
		}
		if row.Kind == "repo" && row.Repo == "widget" && row.Outcome == "failed" && strings.Contains(row.Reason, "fetch failed") {
			seenWidget = true
		}
	}
	if !seenHarness || !seenWidget {
		t.Fatalf("missing independent outcomes: %+v", report.Outcomes)
	}
}

func TestCatchUpStatusReloadOnlyTouchesVerifiedStatusPane(t *testing.T) {
	c := newWorkspaceFixture(t)
	bin := filepath.Join(c.Workspace, "bin")
	log := filepath.Join(c.Workspace, "herdr.log")
	state := filepath.Join(c.Workspace, "pane-state")
	fixtureFile(t, filepath.Join(bin, "herdr"), `#!/bin/sh
printf '%s\n' "$*" >> "$MOCK_HERDR_LOG"
shift 2
case "$1 $2" in
  'workspace list') printf '%s\n' '{"result":{"workspaces":[{"label":"main","workspace_id":"w1"}]}}' ;;
  'pane list') printf '%s\n' '{"result":{"panes":[{"label":"status","pane_id":"w1:p3"},{"label":"agent","pane_id":"w1:p1","agent":"codex"}]}}' ;;
  'pane process-info')
    if [ -f "$MOCK_HERDR_STATE/restarted" ]; then args='["wtc","status","--tui"]'
    elif [ -f "$MOCK_HERDR_STATE/interrupted" ]; then args='["zsh"]'
    elif [ -f "$MOCK_HERDR_STATE/unrelated" ]; then args='["nvim"]'
    else args='["wtc","status","--tui"]'; fi
    printf '{"result":{"process_info":{"foreground_process_group_id":42,"foreground_processes":[{"pid":43,"argv":["renderer"]},{"pid":42,"argv":%s}]}}}\n' "$args" ;;
  'pane send-keys') touch "$MOCK_HERDR_STATE/interrupted" ;;
  'pane run') touch "$MOCK_HERDR_STATE/restarted" ;;
esac
`, 0755)
	if err := os.Mkdir(state, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HERDR_SESSION", "fixture")
	t.Setenv("MOCK_HERDR_LOG", log)
	t.Setenv("MOCK_HERDR_STATE", state)
	target := catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}
	report := CatchUpReport{Initiator: c.Collection}
	fixtureFile(t, filepath.Join(state, "unrelated"), "", 0644)
	c.catchUpReloadStatus(&report, target, CatchUpOptions{ReloadStatus: true})
	if len(report.Outcomes) != 1 || report.Outcomes[0].Outcome != "needs-owner" {
		t.Fatalf("unrelated process was accepted: %+v", report.Outcomes)
	}
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), "pane send-keys") || strings.Contains(string(data), "pane run") {
		t.Fatal("unrelated process received pane keys")
	}
	if err := os.Remove(filepath.Join(state, "unrelated")); err != nil {
		t.Fatal(err)
	}
	report.Outcomes = nil
	c.catchUpReloadStatus(&report, target, CatchUpOptions{ReloadStatus: true})
	if len(report.Outcomes) != 1 || report.Outcomes[0].Outcome != "restarted" {
		t.Fatalf("verified status pane was not restarted: %+v", report.Outcomes)
	}
	data, _ = os.ReadFile(log)
	if strings.Contains(string(data), "w1:p1") || !strings.Contains(string(data), "pane send-keys w1:p3 ctrl+c") || !strings.Contains(string(data), "pane run w1:p3") {
		t.Fatalf("wrong pane activity: %s", data)
	}
	if !strings.Contains(string(data), "cd '"+c.Collection+"' && wtc status --tui") {
		t.Fatalf("native status command was not restarted: %s", data)
	}
}

func TestCatchUpReadsEnlistedBitbucketStateWithoutGuessing(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@bitbucket.org:example/agent-harness.git")
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"), "agent-harness 7 topic - fixture\n", 0644)
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\nprintf '%s\\n' '{\"state\":\"OPEN\",\"draft\":true,\"source\":{\"commit\":{\"hash\":\"abc\"}},\"destination\":{\"branch\":{\"name\":\"release/next\"}}}'\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	target := catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}
	if got := c.catchUpPRState(target, "topic"); got.state != "DRAFT" || got.base != "release/next" {
		t.Fatalf("draft was not protected: %+v", got)
	}
	if got := c.catchUpPRState(target, "other"); got.state != "UNKNOWN" {
		t.Fatalf("unenlisted branch was guessed: %+v", got)
	}
}

func TestCatchUpRejectsUnrecognizedForgeState(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@github.com:example/agent-harness.git")
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"), "agent-harness 7 topic - fixture\n", 0644)
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' '{\"state\":\"FUTURE_STATE\",\"headRefOid\":\"abc\"}'\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	target := catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}
	if got := c.catchUpPRState(target, "topic"); got.state != "UNKNOWN" {
		t.Fatalf("unrecognized PR state was trusted: %+v", got)
	}
}

func TestCatchUpReadsAllEnlistedPRsBeforeMovingBranch(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@github.com:example/agent-harness.git")
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"), "agent-harness 7 topic - first\nagent-harness 8 topic - second\n", 0644)
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
case " $* " in
  *' view 7 '*) printf '%s\n' '{"state":"OPEN"}' ;;
  *' view 8 '*) exit 1 ;;
esac
`, 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	target := catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}
	if got := c.catchUpPRState(target, "topic"); got.state != "UNKNOWN" {
		t.Fatalf("later failed lookup was ignored: %+v", got)
	}
}

func TestCatchUpReloadsRegistryAfterHarnessSelfUpdate(t *testing.T) {
	c := newWorkspaceFixture(t)
	created, err := c.NewCollection(NewOptions{Slug: "other", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	harnessSource := filepath.Join(c.Workspace, "source-agent-harness")
	registryPath := filepath.Join(harnessSource, ".harness-repos.yml")
	registry, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	oldWidget := "name: widget\n    remote: " + filepath.Join(c.Workspace, "source-widget") + "\n    default_ref: origin/main"
	newWidget := strings.Replace(oldWidget, "origin/main", "origin/develop", 1)
	if !strings.Contains(string(registry), oldWidget) {
		t.Fatal("fixture registry shape changed")
	}
	fixtureFile(t, registryPath, strings.Replace(string(registry), oldWidget, newWidget, 1), 0644)
	fixtureGit(t, "-C", harnessSource, "add", ".harness-repos.yml")
	fixtureGit(t, "-C", harnessSource, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "change development ref")
	widgetSource := filepath.Join(c.Workspace, "source-widget")
	fixtureGit(t, "-C", widgetSource, "switch", "-qc", "develop")
	fixtureFile(t, filepath.Join(widgetSource, "README.md"), "develop content\n", 0644)
	fixtureGit(t, "-C", widgetSource, "add", "README.md")
	fixtureGit(t, "-C", widgetSource, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "develop")
	want := fixtureGit(t, "-C", widgetSource, "rev-parse", "HEAD")
	report, err := c.CatchUp(CatchUpOptions{Collections: []string{"other"}, Repos: []string{"harness", "widget"}, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
	if err != nil || report.ExitStatus != 0 {
		t.Fatalf("self-update failed: %+v %v", report, err)
	}
	if got := fixtureGit(t, "-C", filepath.Join(created.Collection, "widget"), "rev-parse", "HEAD"); got != want {
		t.Fatalf("widget did not follow new registry ref: %s, want %s", got, want)
	}
}

func TestCatchUpLegacyDefaultBranchOnlyFastForwards(t *testing.T) {
	c := newWorkspaceFixture(t)
	source := filepath.Join(c.Workspace, "source-agent-harness")
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "main")
	fixtureFile(t, filepath.Join(source, "README.md"), "upstream\n", 0644)
	fixtureGit(t, "-C", source, "add", "README.md")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "upstream")
	want := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	opt := CatchUpOptions{Repos: []string{"harness"}, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true}
	if report, err := c.CatchUp(opt); err != nil || report.ExitStatus != 0 {
		t.Fatalf("fast-forward failed: %+v %v", report, err)
	}
	if got := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD"); got != want {
		t.Fatalf("default branch did not fast-forward: %s", got)
	}
	fixtureFile(t, filepath.Join(c.Harness, "local-commit"), "local\n", 0644)
	fixtureGit(t, "-C", c.Harness, "add", "local-commit")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "local")
	local := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
	fixtureFile(t, filepath.Join(source, "README.md"), "more upstream\n", 0644)
	fixtureGit(t, "-C", source, "add", "README.md")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "more upstream")
	report, err := c.CatchUp(opt)
	if err == nil || report.ExitStatus != 1 {
		t.Fatalf("divergent default branch was merged: %+v %v", report, err)
	}
	if got := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD"); got != local {
		t.Fatalf("divergent local commit was moved: %s", got)
	}
	if catchUpGitOK(c.Harness, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
		t.Fatal("divergent default branch left a merge in progress")
	}
}

func TestCatchUpPinsStashWhenRestoreConflicts(t *testing.T) {
	c := newWorkspaceFixture(t)
	source := filepath.Join(c.Workspace, "source-agent-harness")
	fixtureFile(t, filepath.Join(source, "README.md"), "upstream edit\n", 0644)
	fixtureGit(t, "-C", source, "add", "README.md")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "upstream")
	want := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureFile(t, filepath.Join(c.Harness, "README.md"), "overlapping local edit\n", 0644)
	report, err := c.CatchUp(CatchUpOptions{Repos: []string{"harness"}, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
	if err == nil || report.ExitStatus != 1 {
		t.Fatalf("stash conflict was hidden: %+v %v", report, err)
	}
	if got := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD"); got != want {
		t.Fatalf("updated head was lost: %s", got)
	}
	stash := fixtureGit(t, "-C", c.Harness, "rev-parse", "refs/stash")
	pin := fixtureGit(t, "-C", c.Harness, "rev-parse", "refs/wtc-catch-up/main/agent-harness")
	if stash != pin {
		t.Fatalf("recovery stash was not pinned: %s != %s", stash, pin)
	}
	found := false
	for _, row := range report.Outcomes {
		if row.Kind == "repo" && row.Outcome == "needs-owner" && strings.Contains(row.Reason, "stash restore conflicted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing stash recovery row: %+v", report.Outcomes)
	}
}

func TestCatchUpSkipsSecretsHookForUnmanagedSibling(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+owner, "worktree", "add", "--detach", filepath.Join(c.Collection, "widget"), "origin/main")
	fixtureGit(t, "--git-dir="+owner, "worktree", "add", "--detach", filepath.Join(c.Collection, "ext.thing"), "origin/main")
	calls := filepath.Join(c.Workspace, "secrets-calls")
	t.Setenv("MOCK_SECRETS_LOG", calls)
	fixtureFile(t, filepath.Join(c.Harness, "tools", "link-secrets.sh"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MOCK_SECRETS_LOG\"\n", 0755)
	report, err := c.CatchUp(CatchUpOptions{Repos: []string{"widget", "ext.thing"}, NoSkills: true, NoMCP: true, NoEnv: true})
	if err != nil || report.ExitStatus != 0 {
		t.Fatalf("catch-up with an unmanaged sibling failed: %+v %v", report, err)
	}
	if logged, _ := os.ReadFile(calls); len(logged) != 0 {
		t.Fatalf("legacy secrets script was invoked: %q", logged)
	}
	seen := false
	managed := false
	for _, row := range report.Outcomes {
		if row.Kind == "hook" && row.Repo == "secrets:widget" && row.Outcome == "ok" {
			managed = true
		}
		if row.Kind == "hook" && row.Repo == "secrets:ext.thing" {
			if row.Outcome != "skipped" || !strings.Contains(row.Reason, "unmanaged") {
				t.Fatalf("unmanaged sibling secrets row: %+v", row)
			}
			seen = true
		}
	}
	if !seen || !managed {
		t.Fatalf("missing skipped secrets row: %+v", report.Outcomes)
	}
}

func TestCatchUpRefreshesCollectionWithoutShellEntryPoints(t *testing.T) {
	c := newWorkspaceFixture(t)
	for _, name := range []string{".env.collection", "mise.toml", "AGENTS.md"} {
		if err := os.Remove(filepath.Join(c.Collection, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	target := catchUpTarget{collection: filepath.Base(c.Collection), path: c.Harness, repo: "agent-harness", harness: true, managed: true}
	report := CatchUpReport{Initiator: c.Collection}
	c.catchUpHooks(&report, target, CatchUpOptions{NoSecrets: true}, false)
	for _, name := range []string{".env.collection", "mise.toml", "AGENTS.md"} {
		if _, err := os.Lstat(filepath.Join(c.Collection, name)); err != nil {
			t.Fatalf("native catch-up did not regenerate %s: %v; %+v", name, err, report.Outcomes)
		}
	}
	mcpRows := 0
	for _, row := range report.Outcomes {
		if row.Kind == "hook" && (row.Repo == "env" || row.Repo == "skills") && row.Outcome != "ok" {
			t.Fatalf("native catch-up action failed: %+v", row)
		}
		if row.Kind == "hook" && row.Repo == "mcp" && (row.Outcome != "skipped" || !strings.Contains(row.Reason, "no MCP registry")) {
			t.Fatalf("missing MCP registry was not reported: %+v", row)
		}
		if row.Kind == "hook" && row.Repo == "mcp" {
			mcpRows++
		}
	}
	if mcpRows != 1 {
		t.Fatalf("expected one skipped MCP action: %+v", report.Outcomes)
	}
}

func TestCatchUpRendersMCPWithoutShellEntryPoint(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureFile(t, filepath.Join(c.Harness, ".mcp-servers.yml"), "schema_version: 1\nservers: []\n", 0644)
	legacy := filepath.Join(c.Workspace, "legacy-mcp-called")
	fixtureFile(t, filepath.Join(c.Harness, "tools", "link-mcp.sh"), "#!/bin/sh\ntouch '"+legacy+"'\n", 0755)
	target := catchUpTarget{collection: filepath.Base(c.Collection), path: c.Harness, repo: "agent-harness", harness: true, managed: true}
	report := CatchUpReport{Initiator: c.Collection}
	c.catchUpHooks(&report, target, CatchUpOptions{NoSecrets: true, NoEnv: true, NoSkills: true}, false)
	if _, err := os.Stat(filepath.Join(c.Collection, ".mcp.json")); err != nil {
		t.Fatalf("native MCP output missing: %v; %+v", err, report.Outcomes)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy MCP script was invoked")
	}
	for _, row := range report.Outcomes {
		if row.Kind == "hook" && row.Repo == "mcp" {
			if row.Outcome != "ok" {
				t.Fatalf("native MCP action failed: %+v", row)
			}
			return
		}
	}
	t.Fatal("MCP catch-up outcome missing")
}

func TestCatchUpRejectsInvalidPinBeforeWritingEnv(t *testing.T) {
	c := newWorkspaceFixture(t)
	path := filepath.Join(c.Collection, ".env.collection")
	fixtureFile(t, path, "existing environment\n", 0644)
	fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "invalid pin\n", 0644)
	target := catchUpTarget{collection: filepath.Base(c.Collection), path: c.Harness, repo: "agent-harness", harness: true, managed: true}
	report := CatchUpReport{Initiator: c.Collection}
	c.catchUpHooks(&report, target, CatchUpOptions{NoSecrets: true, NoSkills: true, NoMCP: true}, false)
	if data, err := os.ReadFile(path); err != nil || string(data) != "existing environment\n" {
		t.Fatalf("failed environment validation replaced the old file: %q, %v", data, err)
	}
	for _, row := range report.Outcomes {
		if row.Kind == "hook" && row.Repo == "env" && row.Outcome == "failed" {
			return
		}
	}
	t.Fatalf("missing failed env outcome: %+v", report.Outcomes)
}

func TestCatchUpRunsSecretsHookForSiblingRegisteredByHarnessUpdate(t *testing.T) {
	c := newWorkspaceFixture(t)
	created, err := c.NewCollection(NewOptions{Slug: "other", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	// gadget is a sibling the registry does not know yet; the harness update
	// that catch-up applies first registers it and ships the secrets hook.
	widgetOwner := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+widgetOwner, "worktree", "add", "--detach", filepath.Join(created.Collection, "gadget"), "origin/main")
	harnessSource := filepath.Join(c.Workspace, "source-agent-harness")
	registryPath := filepath.Join(harnessSource, ".harness-repos.yml")
	registry, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, registryPath, string(registry)+"  - name: gadget\n    remote: "+filepath.Join(c.Workspace, "source-widget")+"\n    default_ref: origin/main\n    port_offset: 2\n", 0644)
	fixtureFile(t, filepath.Join(harnessSource, "tools", "link-secrets.sh"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MOCK_SECRETS_LOG\"\n", 0755)
	fixtureGit(t, "-C", harnessSource, "add", "-A")
	fixtureGit(t, "-C", harnessSource, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "register gadget")
	calls := filepath.Join(c.Workspace, "secrets-calls")
	t.Setenv("MOCK_SECRETS_LOG", calls)
	report, err := c.CatchUp(CatchUpOptions{Collections: []string{"other"}, Repos: []string{"harness", "gadget"}, NoSkills: true, NoMCP: true, NoEnv: true})
	if err != nil || report.ExitStatus != 0 {
		t.Fatalf("catch-up across a registering harness update failed: %+v %v", report, err)
	}
	if logged, _ := os.ReadFile(calls); len(logged) != 0 {
		t.Fatalf("legacy secrets script was invoked: %q", logged)
	}
	for _, row := range report.Outcomes {
		if row.Kind == "hook" && row.Repo == "secrets:gadget" && row.Outcome != "ok" {
			t.Fatalf("stale membership decided the hook: %+v", row)
		}
	}
}

func TestCatchUpReconcilesRemotelessOwnerAgainstLocalBranches(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, "local-only")
	fixtureGit(t, "init", "-q", "-b", "main", owner)
	commit := func(name string) string {
		fixtureFile(t, filepath.Join(owner, name), name+"\n", 0644)
		fixtureGit(t, "-C", owner, "add", name)
		fixtureGit(t, "-C", owner, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", name)
		return fixtureGit(t, "-C", owner, "rev-parse", "HEAD")
	}
	commit("one")
	detached := filepath.Join(c.Collection, "ext.local-only")
	spike := filepath.Join(c.Collection, "ext.spike")
	fixtureGit(t, "-C", owner, "worktree", "add", "-q", "--detach", detached, "main")
	fixtureGit(t, "-C", owner, "worktree", "add", "-q", "-b", "spike", spike, "main")
	tip := commit("two")
	// A same-named tag on another commit must not stand in for the branch.
	fixtureGit(t, "-C", owner, "tag", "main", "HEAD~1")
	// A remote under another name is not a local-only owner.
	other := filepath.Join(c.Collection, "ext.renamed")
	renamed := filepath.Join(c.Workspace, "renamed")
	fixtureGit(t, "clone", "-q", owner, renamed)
	fixtureGit(t, "-C", renamed, "remote", "rename", "origin", "upstream")
	fixtureGit(t, "-C", renamed, "worktree", "add", "-q", "--detach", other, "upstream/main")
	if _, targets, _ := c.CatchUpInventory(CatchUpOptions{Repos: []string{"ext.renamed"}, DryRun: true}); len(targets) != 1 || targets[0].noRemote {
		t.Fatalf("owner with a renamed remote counted as remote-less: %+v", targets)
	}
	fixtureGit(t, "-C", renamed, "worktree", "remove", other)
	report, err := c.CatchUp(CatchUpOptions{Repos: []string{"ext.local-only", "ext.spike"}, NoSkills: true, NoMCP: true, NoEnv: true})
	if err != nil || report.ExitStatus != 0 {
		t.Fatalf("remote-less owner failed the sweep: %+v %v", report, err)
	}
	fetches := 0
	for _, row := range report.Outcomes {
		switch {
		case row.Kind == "fetch":
			fetches++
			if row.Outcome != "no-remote" || !strings.Contains(row.Reason, "no remote;") {
				t.Fatalf("remote-less fetch row: %+v", row)
			}
		case row.Kind == "repo":
			if row.Outcome != "updated" || row.TargetRef != "main" {
				t.Fatalf("remote-less repo row: %+v", row)
			}
		}
	}
	if fetches != 1 {
		t.Fatalf("want one fetch row for the shared owner: %+v", report.Outcomes)
	}
	if got := fixtureGit(t, "-C", detached, "rev-parse", "HEAD"); got != tip {
		t.Fatalf("detached worktree at %s, want local main %s", got, tip)
	}
	if fixtureGit(t, "-C", spike, "symbolic-ref", "--short", "HEAD") != "spike" || !catchUpGitOK(spike, "merge-base", "--is-ancestor", tip, "HEAD") {
		t.Fatal("live branch did not receive local main")
	}
	snapshot, err := c.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatalf("status cannot read a remote-less sibling: %v", err)
	}
	seen := false
	for _, row := range snapshot.Repos {
		if row.Dir == "ext.local-only" {
			seen = true
			if row.Behind != 0 || row.Branch != "main" {
				t.Fatalf("status measured the wrong tip: %+v", row)
			}
		}
	}
	if !seen {
		t.Fatalf("status omitted the remote-less sibling: %+v", snapshot.Repos)
	}
	fetchReport, err := c.StatusRefreshRefsWithAge(false, 0)
	if err != nil || fetchReport.Failed != 0 {
		t.Fatalf("status fetched a remote-less owner: %+v %v", fetchReport, err)
	}
}

func TestCatchUpNamesUnreachableRemoteAndStillLinksSecrets(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+owner, "worktree", "add", "--detach", filepath.Join(c.Collection, "widget"), "origin/main")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", filepath.Join(c.Workspace, "gone"))
	report, err := c.CatchUp(CatchUpOptions{Repos: []string{"widget"}, NoSkills: true, NoMCP: true, NoEnv: true})
	if err == nil || report.ExitStatus != 1 {
		t.Fatalf("unreachable remote was not loud: %+v %v", report, err)
	}
	fetch, repo, secrets := false, false, false
	for _, row := range report.Outcomes {
		switch {
		case row.Kind == "fetch":
			fetch = row.Outcome == "failed" && strings.HasPrefix(row.Reason, "fatal:") && row.Reason != owner
		case row.Kind == "repo":
			repo = row.Outcome == "failed" && strings.Contains(row.Reason, "stale refs not used")
		case row.Kind == "hook" && row.Repo == "secrets:widget":
			secrets = row.Outcome == "ok"
		}
	}
	if !fetch || !repo || !secrets {
		t.Fatalf("unreachable remote rows: %+v", report.Outcomes)
	}
}

func TestCatchUpRepoOnlyHooksSkipCollectionRenders(t *testing.T) {
	c := newWorkspaceFixture(t)
	target := catchUpTarget{collection: filepath.Base(c.Collection), path: c.Harness, repo: "agent-harness", harness: true, managed: true}
	report := CatchUpReport{Initiator: c.Collection}
	c.catchUpHooks(&report, target, CatchUpOptions{DryRun: true}, true)
	if len(report.Outcomes) != 1 || report.Outcomes[0].Repo != "secrets:agent-harness" {
		t.Fatalf("repo-only hooks ran collection renders: %+v", report.Outcomes)
	}
}

func TestCatchUpLinksSecretsWhenFetchFailsBeforePRPreflight(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	worktree := filepath.Join(c.Collection, "widget")
	fixtureGit(t, "--git-dir="+owner, "worktree", "add", "-q", "-b", "topic", worktree, "origin/main")
	// A forge remote whose fetch fails offline, with an open PR whose merge
	// target is missing: the preflight that used to exit before the hooks.
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@github.com:example/widget.git")
	t.Setenv("GIT_SSH_COMMAND", "false")
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' '[{\"state\":\"OPEN\",\"isDraft\":false,\"baseRefName\":\"\"}]'\n", 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	report, err := c.CatchUp(CatchUpOptions{Repos: []string{"widget"}, NoSkills: true, NoMCP: true, NoEnv: true})
	if err == nil || report.ExitStatus != 1 {
		t.Fatalf("failed fetch was not loud: %+v %v", report, err)
	}
	repo, secrets := 0, 0
	for _, row := range report.Outcomes {
		if row.Kind == "repo" {
			repo++
			if row.Outcome != "failed" || !strings.Contains(row.Reason, "stale refs not used") {
				t.Fatalf("repo row: %+v", row)
			}
		}
		if row.Kind == "hook" && row.Repo == "secrets:widget" && row.Outcome == "ok" {
			secrets++
		}
	}
	if repo != 1 || secrets != 1 {
		t.Fatalf("want one repo row and one secrets run: %+v", report.Outcomes)
	}
}

func TestCatchUpLocalRefMapsEveryDevelopmentRefForm(t *testing.T) {
	local := catchUpTarget{noRemote: true}
	for _, in := range []string{"origin/main", "refs/heads/main", "refs/remotes/origin/main"} {
		ref, err := gitref.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := catchUpLocalRef(local, ref); got.String() != "refs/heads/main" {
			t.Errorf("%s resolved to %s", in, got)
		}
	}
	if got := catchUpLocalRef(catchUpTarget{}, gitref.RemoteBranch("origin", "main")); got.String() != "refs/remotes/origin/main" {
		t.Errorf("owner with a remote rewrote its ref: %s", got)
	}
}

// squashFixture puts a widget worktree on a two-commit branch and lands the
// same change on the source's main as one squash commit. With unrelated set,
// main moves first, so the trees differ and only the change itself matches.
func squashFixture(t *testing.T, unrelated bool) (*Context, catchUpTarget, string) {
	t.Helper()
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	worktree := filepath.Join(c.Collection, "widget")
	fixtureGit(t, "--git-dir="+owner, "worktree", "add", "-q", "-b", "topic", worktree, "origin/main")
	commit := func(dir, name, body string) {
		fixtureFile(t, filepath.Join(dir, name), body, 0644)
		fixtureGit(t, "-C", dir, "add", name)
		fixtureGit(t, "-C", dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", name)
	}
	commit(worktree, "feature.txt", "one\n")
	commit(worktree, "feature.txt", "one\ntwo\n")
	source := filepath.Join(c.Workspace, "source-widget")
	if unrelated {
		commit(source, "other.txt", "elsewhere\n")
	}
	commit(source, "feature.txt", "one\ntwo\n")
	squash := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureGit(t, "--git-dir="+owner, "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
	_, targets, err := c.CatchUpInventory(CatchUpOptions{Repos: []string{"widget"}, DryRun: true})
	if err != nil || len(targets) != 1 {
		t.Fatalf("inventory: %+v %v", targets, err)
	}
	return c, targets[0], squash
}

func TestCatchUpLandedAcceptsSquashMergeByContent(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		_, target, squash := squashFixture(t, unrelated)
		tip := fixtureGit(t, "-C", target.path, "rev-parse", "origin/main")
		if !catchUpLanded(target, tip, catchUpPR{state: "MERGED", mergeCommit: squash}) {
			t.Fatalf("squash merge (unrelated main commit: %v) not established as landed", unrelated)
		}
		if catchUpLanded(target, tip, catchUpPR{state: "MERGED"}) {
			t.Fatal("landing established without a merge commit")
		}
		parent := fixtureGit(t, "-C", target.path, "rev-parse", squash+"^")
		if catchUpLanded(target, tip, catchUpPR{state: "MERGED", mergeCommit: parent}) {
			t.Fatal("a merge commit without the branch's content established landing")
		}
	}
}

func TestStatusReturnsCleanMergedWorktreeToTip(t *testing.T) {
	c, target, squash := squashFixture(t, true)
	records := []PRRecord{{Repo: "widget", Number: "7", Branch: "topic"}}
	details := []statusPRDetail{{Number: "7", State: "MERGED", MergeCommit: squash}}
	tip := fixtureGit(t, "-C", target.path, "rev-parse", "origin/main")

	onTopic := func(why string) {
		t.Helper()
		snapshot, err := c.StatusLocalSnapshot(false)
		if err != nil {
			t.Fatal(err)
		}
		c.statusReturnMerged(&snapshot, records, details)
		if fixtureGit(t, "-C", target.path, "symbolic-ref", "--short", "HEAD") != "topic" {
			t.Fatalf("%s: worktree left its branch", why)
		}
	}
	for _, value := range []string{"off", "0", "false", "no"} {
		t.Setenv("WTC_STATUS_RETURN_MERGED", value)
		onTopic("env opt-out " + value)
	}
	t.Setenv("WTC_STATUS_RETURN_MERGED", "")
	off := false
	c.Config.Status.ReturnMerged = &off
	onTopic("wtc.toml opt-out")
	c.Config.Status.ReturnMerged = nil
	parent := fixtureGit(t, "-C", target.path, "rev-parse", squash+"^")
	details[0].MergeCommit = parent
	onTopic("unlanded branch")
	details[0].MergeCommit = squash
	records = append(records, PRRecord{Repo: "widget", Number: "8", Branch: "topic"})
	details = append(details, statusPRDetail{Number: "8", State: "OPEN"})
	onTopic("reused branch with an open PR")
	records, details = records[:1], details[:1]

	fixtureFile(t, filepath.Join(target.path, "scratch.txt"), "local\n", 0644)
	onTopic("dirty worktree")
	if err := os.Remove(filepath.Join(target.path, "scratch.txt")); err != nil {
		t.Fatal(err)
	}

	// Reconciliation refuses a worktree that moved off the branch it was read for.
	moved := catchUpPR{state: "MERGED", mergeCommit: squash, branch: "elsewhere"}
	if outcome, reason := c.catchUpReconcile(target, target.ref, tip, moved, CatchUpOptions{CleanOnly: true}); outcome != "needs-owner" || !strings.Contains(reason, "left elsewhere") {
		t.Fatalf("moved branch: %s %s", outcome, reason)
	}

	var progress []string
	c.StatusProgress = func(message string) { progress = append(progress, message) }
	snapshot, _ := c.StatusLocalSnapshot(false)
	for j := range snapshot.Repos {
		if snapshot.Repos[j].Dir == "widget" {
			snapshot.Repos[j].Runtime = "1 service up"
		}
	}
	c.statusReturnMerged(&snapshot, records, details)
	if got := fixtureGit(t, "-C", target.path, "rev-parse", "HEAD"); got != tip || catchUpGitOK(target.path, "symbolic-ref", "-q", "HEAD") {
		t.Fatalf("merged worktree not detached at the tip: %s", got)
	}
	// Safe pruning refuses a squash-merged branch, so the local ref survives.
	if log := strings.Join(progress, "\n"); !strings.Contains(log, "Returned widget to origin/main after its PR merged") || !strings.Contains(log, "local branch retained") {
		t.Fatalf("refresh log: %v", progress)
	}
	if !catchUpGitOK(target.path, "rev-parse", "--verify", "refs/heads/topic") {
		t.Fatal("unmerged local branch was force-deleted")
	}
	for _, row := range snapshot.Repos {
		if row.Dir == "widget" && (row.BranchKind != "detached" || row.Behind != 0 || row.Runtime != "1 service up") {
			t.Fatalf("snapshot row not refreshed: %+v", row)
		}
	}
}

func TestStatusReturnMergedTreatsHarnessAliasesAsOneWorktree(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic") // Landed: no commits beyond the tip.
	records := []PRRecord{{Repo: "harness", Number: "7", Branch: "topic"}, {Repo: "agent-harness", Number: "8", Branch: "topic"}}
	details := []statusPRDetail{{Number: "7", State: "MERGED"}, {Number: "8", State: "OPEN"}}
	snapshot, err := c.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	c.statusReturnMerged(&snapshot, records, details)
	if fixtureGit(t, "-C", c.Harness, "symbolic-ref", "--short", "HEAD") != "topic" {
		t.Fatal("an open PR under the other harness alias did not keep the branch")
	}
	// An inherited identity naming another repo must not redirect a merged
	// widget PR onto the harness branch of the same name.
	t.Setenv("WTC_HARNESS_REPO", "widget")
	c.statusReturnMerged(&snapshot, []PRRecord{{Repo: "widget", Number: "9", Branch: "topic"}}, []statusPRDetail{{Number: "9", State: "MERGED"}})
	if fixtureGit(t, "-C", c.Harness, "symbolic-ref", "--short", "HEAD") != "topic" {
		t.Fatal("inherited WTC_HARNESS_REPO redirected a widget merge to the harness")
	}
	t.Setenv("WTC_HARNESS_REPO", "agent-harness")
	c.statusReturnMerged(&snapshot, records[:1], details[:1])
	if catchUpGitOK(c.Harness, "symbolic-ref", "-q", "HEAD") {
		t.Fatal("a landed merged harness branch was not returned to the tip")
	}
}

func TestCatchUpForgeLookupIsBounded(t *testing.T) {
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nexec sleep 10\n", 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := catchUpForgeTimeout
	catchUpForgeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { catchUpForgeTimeout = old })
	start := time.Now()
	if _, err := catchUpJSON("gh", "pr", "view", "7"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("stalled forge lookup: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("lookup was not bounded: %s", elapsed)
	}
}
