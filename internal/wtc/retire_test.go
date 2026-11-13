package wtc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetirePreservesStandalonePrivateControl(t *testing.T) {
	root := adHocFixture(t)
	c := initAdHoc(t, filepath.Join(root, "task"))
	private := filepath.Join(c.Collection, ".config", "synthetic-token")
	fixtureFile(t, private, "synthetic credential", 0600)
	for _, force := range []bool{false, true} {
		if _, err := c.RetirePreflight(RetireOptions{Name: filepath.Base(c.Collection), Self: true, Force: force}); err == nil || !strings.Contains(err.Error(), "private control") {
			t.Fatalf("private data not preserved (force=%v): %v", force, err)
		}
	}
	if body, err := os.ReadFile(private); err != nil || string(body) != "synthetic credential" {
		t.Fatal("retirement changed private data")
	}
}

func TestRetireCollectionRunsTeardownAndKeepsRemoteRefs(t *testing.T) {
	c := newWorkspaceFixture(t)
	result, err := c.NewCollection(NewOptions{Slug: "finished", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target := result.Collection
	fixtureFile(t, filepath.Join(target, "SCOPE.md"), "Disposable task context.\n", 0644)
	fixtureFile(t, filepath.Join(target, ".wtc-prs.lock"), "", 0600)
	for _, name := range []string{".taba-prs", ".taba-status.json", ".taba-status.md", ".last-taba-status.yml", ".taba-catch-up.json", ".taba-catch-up.json.md"} {
		fixtureFile(t, filepath.Join(target, name), "", 0644)
	}
	// A dev build is generated and must not keep the folder alive.
	fixtureFile(t, filepath.Join(target, ".wtc", "dev", "wtc"), "#!/bin/sh\n", 0755)
	fixtureFile(t, filepath.Join(target, ".wtc", "dev", "version"), "0.1.40-dev.1+dev.gabc1234\n", 0644)
	// So are a review-only collection's mode file and pre-push hook.
	fixtureFile(t, filepath.Join(target, ReviewModeFile), "{}\n", 0644)
	fixtureFile(t, filepath.Join(target, ".wtc", "review-hooks", "pre-push"), "#!/bin/sh\nexit 1\n", 0755)
	widget := filepath.Join(target, "widget")
	if err := os.Remove(filepath.Join(widget, "init-ran")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(widget, ".harness", "teardown.sh"), "#!/bin/sh\nprintf '%s' \"$WTC_COLLECTION\" > \"$(dirname \"$(dirname \"$PWD\")\")/teardown-ran\"\n", 0755)
	// The untracked hook itself would make the worktree dirty. Commit it only
	// in the local fixture and publish that ref to the local bare remote.
	fixtureGit(t, "-C", widget, "add", ".harness/teardown.sh")
	fixtureGit(t, "-C", widget, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "teardown fixture")
	head := fixtureGit(t, "-C", widget, "rev-parse", "HEAD")
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+owner, "update-ref", "refs/remotes/origin/teardown-fixture", head)
	retired, err := c.RetireCollection(RetireOptions{Name: "finished"})
	if err != nil {
		t.Fatal(err)
	}
	if !retired.FolderRemoved || len(retired.Removed) != 2 {
		t.Fatalf("unexpected retirement: %+v", retired)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(c.Workspace, "teardown-ran")); err != nil || string(data) != "finished" {
		t.Fatalf("teardown hook did not run with collection env: %q (%v)", data, err)
	}
	if got := fixtureGit(t, "--git-dir="+owner, "rev-parse", "refs/remotes/origin/teardown-fixture"); got != head {
		t.Fatalf("remote ref changed: %s", got)
	}
}

func TestRetireCollectionPreflightAndLeftovers(t *testing.T) {
	c := newWorkspaceFixture(t)
	result, err := c.NewCollection(NewOptions{Slug: "pending", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target := result.Collection
	if _, err := c.RetireCollection(RetireOptions{Name: "pending"}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty tree was not blocked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "widget")); err != nil {
		t.Fatalf("preflight removed worktree: %v", err)
	}
	if err := os.Remove(filepath.Join(target, "widget", "init-ran")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(target, "keep.txt"), "unknown local state\n", 0644)
	fixtureFile(t, filepath.Join(target, ".claude", "skills", "personal", "SKILL.md"), "local override\n", 0644)
	fixtureFile(t, filepath.Join(target, ".codex", "config.toml"), "model = \"example\"\n", 0644)
	if err := os.Remove(filepath.Join(target, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(target, "AGENTS.md"), "local instructions\n", 0644)
	fixtureFile(t, filepath.Join(target, ".envrc"), "export EXAMPLE=1\n", 0644)
	retired, err := c.RetireCollection(RetireOptions{Name: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if retired.FolderRemoved || len(retired.Leftovers) != 5 {
		t.Fatalf("unknown file did not remain visible: %+v", retired)
	}
	for _, rel := range []string{"keep.txt", ".claude/skills/personal/SKILL.md", ".codex/config.toml", "AGENTS.md", ".envrc"} {
		if _, err := os.Stat(filepath.Join(target, rel)); err != nil {
			t.Fatalf("local file %s was removed: %v", rel, err)
		}
	}
}

func TestRemoveManagedCollectionEntryFallback(t *testing.T) {
	target := t.TempDir()
	defaultBody, err := ReadDefault("collection-AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(target, ".wtc", "collection-AGENTS.md"), string(defaultBody), 0644)
	if err := os.Symlink(".wtc/collection-AGENTS.md", filepath.Join(target, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedCollectionEntry(target); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("generated entry remains: %v %v", entries, err)
	}
}

func TestRemoveManagedCollectionEntryGeneratedEnvrcMarker(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		removed    bool
	}{
		{"generated", "# Generated by wtc skills render. Do not edit.\nlocal change\n", true},
		{"override", "export USER_CHOICE=1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			path := filepath.Join(target, ".envrc")
			fixtureFile(t, path, tc.body, 0644)
			if err := removeManagedCollectionEntry(target); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(path)
			if os.IsNotExist(err) != tc.removed {
				t.Fatalf("removed=%v, want %v; err=%v", os.IsNotExist(err), tc.removed, err)
			}
		})
	}
}

func TestRetireCollectionHonorsHerdrSessionOverride(t *testing.T) {
	c := newWorkspaceFixture(t)
	bin := filepath.Join(c.Workspace, "bin")
	log := filepath.Join(c.Workspace, "herdr.log")
	fixtureFile(t, filepath.Join(bin, "herdr"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\ncase \"$*\" in *'workspace list') printf '%s\\n' '{\"result\":{\"workspaces\":[{\"label\":\"finished\",\"workspace_id\":\"fixture-id\"}]}}';; esac\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HARNESS_HERDR_SESSION", "other-session")
	if closed, warning := c.closeRetiredWorkspace("finished"); !closed || warning != "" {
		t.Fatalf("unexpected herdr result: %v %q", closed, warning)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "--session other-session workspace list\n--session other-session workspace close fixture-id\n" {
		t.Fatalf("herdr used wrong session: %q (%v)", data, err)
	}
}

func TestRetireWorkspaceClosureUsesVerifiedID(t *testing.T) {
	c := newWorkspaceFixture(t)
	bin := filepath.Join(c.Workspace, "bin")
	log := filepath.Join(c.Workspace, "herdr-id.log")
	fixtureFile(t, filepath.Join(bin, "herdr"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+log+"\"\ncase \"$*\" in *'workspace list') printf '%s\\n' '{\"result\":{\"workspaces\":[{\"label\":\"finished\",\"workspace_id\":\"wrong-id\"},{\"label\":\"finished\",\"workspace_id\":\"source-id\"}]}}';; esac\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if closed, warning := c.closeRetiredWorkspaceID("finished", "source-id"); !closed || warning != "" {
		t.Fatalf("verified workspace was not closed: %v %q", closed, warning)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), "workspace close source-id") || strings.Contains(string(data), "workspace close wrong-id") {
		t.Fatalf("closed wrong workspace: %q (%v)", data, err)
	}
	if closed, warning := c.closeRetiredWorkspaceID("finished", "missing-id"); closed || !strings.Contains(warning, "identity changed") {
		t.Fatalf("missing pinned workspace was accepted: %v %q", closed, warning)
	}
	after, err := os.ReadFile(log)
	if err != nil || strings.Count(string(after), "workspace close ") != 1 {
		t.Fatalf("missing pinned workspace closed another workspace: %q (%v)", after, err)
	}
}

func TestRetireCollectionRejectsSelfAndEscapes(t *testing.T) {
	c := newWorkspaceFixture(t)
	for _, name := range []string{"main", "../source-widget", "missing"} {
		if _, err := c.RetireCollection(RetireOptions{Name: name, Force: true}); err == nil {
			t.Fatalf("accepted retirement of %q", name)
		}
	}
	if _, err := os.Stat(c.Harness); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RetireCollection(RetireOptions{Name: "main", Self: true}); err == nil || !strings.Contains(err.Error(), "workspace ID") {
		t.Fatalf("self-retirement without a verified workspace was accepted: %v", err)
	}
	sibling, err := c.NewCollection(NewOptions{Slug: "sibling"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RetireCollection(RetireOptions{Name: "sibling", Self: true, WorkspaceID: "source-id"}); err == nil || !strings.Contains(err.Error(), "current collection") {
		t.Fatalf("sibling retirement via self worker was accepted: %v", err)
	}
	if _, err := os.Stat(sibling.Collection); err != nil {
		t.Fatalf("sibling was removed by rejected self-retirement: %v", err)
	}
}

func TestRetireCollectionSelfWorkerKeepsRemoteRefs(t *testing.T) {
	c := newWorkspaceFixture(t)
	created, err := c.NewCollection(NewOptions{Slug: "self-retire", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := OpenCollection(created.Collection)
	if err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(created.Collection, "harness")
	widget := filepath.Join(created.Collection, "widget")
	if err := os.Remove(filepath.Join(widget, "init-ran")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(widget, ".harness", "teardown.sh"), "#!/bin/sh\nprintf 'product torn down\\n' > \"$(dirname \"$(dirname \"$PWD\")\")/product-teardown.log\"\n", 0755)
	fixtureGit(t, "-C", widget, "add", ".harness/teardown.sh")
	fixtureGit(t, "-C", widget, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "product teardown fixture")
	widgetHead := fixtureGit(t, "-C", widget, "rev-parse", "HEAD")
	widgetOwner := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+widgetOwner, "update-ref", "refs/remotes/origin/self-retire-teardown", widgetHead)
	fixtureFile(t, filepath.Join(harness, "hooks", "wtc", "retire.pre.sh"), "#!/bin/sh\n[ -f \"$(dirname \"$PWD\")/workspace-closed.log\" ] || exit 15\n", 0755)
	fixtureFile(t, filepath.Join(harness, "hooks", "wtc", "retire.post.sh"), "#!/bin/sh\n[ -d \"$PWD/harness\" ] || exit 13\n[ -f \"$(dirname \"$PWD\")/product-teardown.log\" ] || exit 14\nprintf 'post\\n' >> \"$(dirname \"$PWD\")/post-hook.log\"\n", 0755)
	fixtureGit(t, "-C", harness, "add", "hooks/wtc/retire.pre.sh", "hooks/wtc/retire.post.sh")
	fixtureGit(t, "-C", harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "self-retire post hook")
	head := fixtureGit(t, "-C", harness, "rev-parse", "HEAD")
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	fixtureGit(t, "--git-dir="+owner, "update-ref", "refs/remotes/origin/self-retire-hook", head)
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "herdr"), "#!/bin/sh\ncase \"$*\" in\n  *'workspace list') printf '%s\\n' '{\"result\":{\"workspaces\":[{\"label\":\"self-retire\",\"workspace_id\":\"source-id\"}]}}' ;;\n  *'workspace close source-id') printf 'closed\\n' > \"$RETIRE_TEST_CLOSED_MARKER\" ;;\n  *) exit 2 ;;\nesac\n", 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HARNESS_HERDR_SESSION", "fixture")
	t.Setenv("RETIRE_TEST_CLOSED_MARKER", filepath.Join(c.Workspace, "workspace-closed.log"))
	if _, err := target.RetirePreflight(RetireOptions{Name: "self-retire"}); err == nil {
		t.Fatal("ordinary caller bypassed the self-retire guard")
	}
	if _, err := target.RetirePreflight(RetireOptions{Name: "self-retire", Self: true}); err != nil {
		t.Fatal(err)
	}
	retired, err := target.RetireCollection(RetireOptions{Name: "self-retire", Self: true, WorkspaceID: "source-id"})
	if err != nil {
		t.Fatal(err)
	}
	if !retired.FolderRemoved || !retired.WorkspaceClosed || len(retired.Removed) != 2 {
		t.Fatalf("unexpected self-retirement: %+v", retired)
	}
	if _, err := os.Stat(created.Collection); !os.IsNotExist(err) {
		t.Fatalf("self-retired collection remains: %v", err)
	}
	if _, err := os.Stat(owner); err != nil {
		t.Fatalf("bare owner was removed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(c.Workspace, "post-hook.log")); err != nil || string(data) != "post\n" {
		t.Fatalf("post hook did not run exactly once while harness existed: %q (%v)", data, err)
	}
}

func TestRetirePreflightRejectsMainCheckout(t *testing.T) {
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "harness", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inspectRetireWorktrees(target); err == nil || !strings.Contains(err.Error(), "linked Git worktree") {
		t.Fatalf("main checkout was not rejected before deletion: %v", err)
	}
}

func TestRetireCollectionBlocksUnpushedCommitAndPreHookCanVeto(t *testing.T) {
	c := newWorkspaceFixture(t)
	result, err := c.NewCollection(NewOptions{Slug: "unpushed"})
	if err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(result.Collection, "harness")
	fixtureFile(t, filepath.Join(harness, "work.txt"), "local commit\n", 0644)
	fixtureGit(t, "-C", harness, "add", "work.txt")
	fixtureGit(t, "-C", harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "local fixture")
	if _, err := c.RetireCollection(RetireOptions{Name: "unpushed"}); err == nil || !strings.Contains(err.Error(), "commits absent") {
		t.Fatalf("unpushed commit was not blocked: %v", err)
	}
	fixtureFile(t, filepath.Join(c.Harness, "hooks", "wtc", "retire.pre.sh"), "#!/bin/sh\nexit 12\n", 0755)
	if _, err := c.RetireCollection(RetireOptions{Name: "unpushed", Force: true}); err == nil || !strings.Contains(err.Error(), "retire.pre") {
		t.Fatalf("pre-hook did not veto forced retirement: %v", err)
	}
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("pre-hook veto removed target: %v", err)
	}
}

func TestRetireMarksLeftoverFolderAndOtherCommandsRespectIt(t *testing.T) {
	c := newWorkspaceFixture(t)
	result, err := c.NewCollection(NewOptions{Slug: "leftover", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target := result.Collection
	if err := os.Remove(filepath.Join(target, "widget", "init-ran")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(target, "design", "sketch.txt"), "hand-made\n", 0644)
	CLIVersion = "9.9.9"
	t.Cleanup(func() { CLIVersion = "dev" })
	retired, err := c.RetireCollection(RetireOptions{Name: "leftover"})
	if err != nil {
		t.Fatal(err)
	}
	if retired.FolderRemoved || strings.Join(retired.Leftovers, ",") != "design" {
		t.Fatalf("unexpected leftovers: %+v", retired)
	}
	marker, err := ReadRetiredMarker(target)
	if err != nil || marker.SchemaVersion != 1 || marker.CLIVersion != "9.9.9" || marker.RetiredAt == "" || strings.Join(marker.BlockingPaths, ",") != "design" {
		t.Fatalf("marker %+v, error %v", marker, err)
	}

	// Sweeps and discovery treat the folder as leftovers, never as a target.
	// Even a stray harness directory does not revive it.
	if err := os.Mkdir(filepath.Join(target, "harness"), 0755); err != nil {
		t.Fatal(err)
	}
	dirs, err := WorkspaceCollections(c.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		if dir == target {
			t.Fatalf("sweep listed a retired folder: %v", dirs)
		}
	}
	if err := os.Remove(filepath.Join(target, "harness")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(filepath.Join(target, "design")); err == nil || !strings.Contains(err.Error(), "retired collection") {
		t.Fatalf("discovery inside leftovers: %v", err)
	}
	if _, err := OpenCollection(target); err == nil || !strings.Contains(err.Error(), "retired collection") {
		t.Fatalf("open of leftovers: %v", err)
	}
	stale := &Context{Collection: target}
	if err := stale.WriteStatusSnapshot(StatusSnapshot{Schema: 1, Collection: "leftover"}); err == nil {
		t.Fatal("a lingering status loop repopulated the retired folder")
	}
	report := CatchUpReport{}
	if err := os.Symlink(filepath.Join(target, "design"), filepath.Join(c.Collection, "reports")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(target, ".wtc-catch-up.json"), filepath.Join(target, "design", "report.json"), filepath.Join(c.Collection, "reports", "report.json")} {
		if err := SaveCatchUpReport(&report, path); err == nil {
			t.Fatalf("a catch-up report was written into the retired folder: %s", path)
		}
	}
	t.Chdir(filepath.Join(target, "design"))
	if err := SaveCatchUpReport(&report, "report.json"); err == nil {
		t.Fatal("a relative report path wrote into the retired folder")
	}
	snapshot, err := c.StatusLocalSnapshot(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Retired) != 1 || snapshot.Retired[0].Folder != "leftover" || !strings.Contains(snapshot.Markdown(), "## Retired leftovers") {
		t.Fatalf("status did not show the leftovers: %+v", snapshot.Retired)
	}

	// Retire again: still blocked, then finished once the file is gone, even
	// with a generated file that appeared in the meantime.
	again, err := c.RetireCollection(RetireOptions{Name: "leftover"})
	if err != nil || again.FolderRemoved || strings.Join(again.Leftovers, ",") != "design" {
		t.Fatalf("re-retire with blockers: %+v %v", again, err)
	}
	if err := os.RemoveAll(filepath.Join(target, "design")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(target, ".wtc-status.json"), "{}\n", 0644)
	done, err := c.RetireCollection(RetireOptions{Name: "leftover"})
	if err != nil || !done.FolderRemoved {
		t.Fatalf("re-retire did not finish: %+v %v", done, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("retired folder remains: %v", err)
	}
}

func TestRetiredLeftoversWithUnreadableMarkerStillListAndBlockWorktrees(t *testing.T) {
	c := newWorkspaceFixture(t)
	folder := filepath.Join(c.Workspace, "broken")
	fixtureFile(t, filepath.Join(folder, RetiredMarkerFile), "not json\n", 0644)
	fixtureFile(t, filepath.Join(folder, "notes.txt"), "kept\n", 0644)
	snapshot, err := c.StatusLocalSnapshot(true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(snapshot.Retired) != 1 || strings.Join(snapshot.Retired[0].BlockingPaths, ",") != "notes.txt" ||
		!strings.Contains(string(data), `"retired":[{"folder":"broken"`) {
		t.Fatalf("unreadable marker not listed with current contents: %s %v", data, err)
	}
	fixtureFile(t, filepath.Join(folder, ".git"), "gitdir: elsewhere\n", 0644)
	fixtureFile(t, filepath.Join(folder, "mise.toml"), "restored\n", 0644)
	if _, err := c.RetireCollection(RetireOptions{Name: "broken"}); err == nil || !strings.Contains(err.Error(), "Git checkout itself") {
		t.Fatalf("re-retire cleaned a checkout at the folder root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "mise.toml")); err != nil {
		t.Fatal("a restored checkout's file was removed")
	}
	for _, name := range []string{".git", "mise.toml"} {
		if err := os.Remove(filepath.Join(folder, name)); err != nil {
			t.Fatal(err)
		}
	}
	fixtureFile(t, filepath.Join(folder, "stray", ".git"), "gitdir: elsewhere\n", 0644)
	if _, err := c.RetireCollection(RetireOptions{Name: "broken"}); err == nil || !strings.Contains(err.Error(), "holds stray again") {
		t.Fatalf("re-retire removed files around a worktree: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(folder, "stray")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(folder, "harness", "README.md"), "back again\n", 0644)
	if _, err := c.RetireCollection(RetireOptions{Name: "broken"}); err == nil || !strings.Contains(err.Error(), "holds harness again") {
		t.Fatalf("re-retire removed files around a harness: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "notes.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestFinishRetiredFolderKeepsMarkerWhenRemovalFails(t *testing.T) {
	c := newWorkspaceFixture(t)
	parent := filepath.Join(c.Workspace, "locked")
	folder := filepath.Join(parent, "leftover")
	fixtureFile(t, filepath.Join(folder, RetiredMarkerFile), "{}\n", 0644)
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0755) })
	if _, err := c.finishRetiredFolder(folder); err == nil {
		t.Fatal("removal under a read-only parent succeeded")
	}
	if !RetiredFolder(folder) {
		t.Fatal("surviving folder lost its retire marker")
	}
}
