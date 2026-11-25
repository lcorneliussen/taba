package wtc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/gitref"
)

func TestReviewModeCollectionCannotPushAndFollowsPRHead(t *testing.T) {
	c := newWorkspaceFixture(t)
	source := filepath.Join(c.Workspace, "source-widget")
	commit := func(dir, file, message string) string {
		t.Helper()
		fixtureFile(t, filepath.Join(dir, file), message+"\n", 0644)
		fixtureGit(t, "-C", dir, "add", file)
		fixtureGit(t, "-C", dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", message)
		return fixtureGit(t, "-C", dir, "rev-parse", "HEAD")
	}
	headFile := filepath.Join(c.Workspace, "pr-head")
	publish := func(head string) {
		t.Helper()
		fixtureGit(t, "-C", source, "update-ref", "refs/pull/21/head", head)
		fixtureFile(t, headFile, head, 0644)
	}
	fixtureGit(t, "-C", source, "checkout", "-qb", "feature")
	publish(commit(source, "feature.txt", "first round"))
	fixtureGit(t, "-C", source, "checkout", "-q", "main")
	tipMoved := commit(source, "main.txt", "tip moved")
	c.Registry.Repos[1].Remote = "https://github.com/example/widget.git"
	// A longer pushInsteadOf in any scope beats the worktree's empty prefix;
	// the per-remote push URL must hold regardless.
	global := filepath.Join(c.Workspace, "global.gitconfig")
	fixtureFile(t, global, "[url \""+source+"\"]\n\tpushInsteadOf = "+source+"\n", 0644)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '{\"headRefName\":\"feature\",\"headRefOid\":\"%s\",\"baseRefName\":\"main\",\"title\":\"Review fixture\"}\\n' \"$(cat "+headFile+")\"\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	r, err := c.NewCollection(NewOptions{PR: "widget#21", Review: true})
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(r.Collection, "widget")
	if !r.Review || !reviewOnlyWorktree(worktree) || reviewOnlyWorktree(filepath.Join(r.Collection, "harness")) {
		t.Fatalf("review mode not recorded for the PR worktree only: %+v", r)
	}
	note, err := os.ReadFile(filepath.Join(r.Collection, "HANDOFF.md"))
	if err != nil || !strings.Contains(string(note), "Review-only collection") || strings.Contains(string(note), "git -C") {
		t.Fatalf("launch note does not say review-only: %s (%v)", note, err)
	}

	// No push leaves the worktree: not to its remote, a raw URL or a remote
	// added later. Another worktree of the same owner still pushes.
	fixtureGit(t, "-C", worktree, "remote", "add", "later", source)
	for _, target := range []string{"origin", source, "later"} {
		if out, err := exec.Command("git", "-C", worktree, "push", target, "HEAD:refs/heads/leak").CombinedOutput(); err == nil || !strings.Contains(string(out), "push-disabled-in-wtc-review-mode") {
			t.Fatalf("push to %s was not disabled: %s %v", target, out, err)
		}
	}
	if out, err := exec.Command("git", "-C", worktree, "push", "--no-verify", "origin", "HEAD:refs/heads/leak").CombinedOutput(); err == nil || !strings.Contains(string(out), "push-disabled-in-wtc-review-mode") {
		t.Fatalf("push skipping hooks reached origin: %s %v", out, err)
	}
	if _, err := gitOutput("-C", source, "rev-parse", "--verify", "refs/heads/leak"); err == nil {
		t.Fatal("a review-only push reached the source")
	}
	fixtureGit(t, "-C", worktree, "remote", "remove", "later")
	other := filepath.Join(c.Workspace, "other-widget")
	fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "widget.git"), "worktree", "add", "-q", "--detach", other, "origin/main")
	fixtureGit(t, "-C", other, "push", "-q", "origin", "HEAD:refs/heads/allowed")
	if got := fixtureGit(t, "-C", other, "rev-parse", "--is-bare-repository"); got != "false" {
		t.Fatalf("worktree of the locked owner reads as bare: %s", got)
	}

	target, err := OpenCollection(r.Collection)
	if err != nil {
		t.Fatal(err)
	}
	target.Registry.Repos[1].Remote = c.Registry.Repos[1].Remote
	if _, err := target.ResolveReviewBundle(filepath.Join(r.Collection, "any-bundle"), ReviewResolveOptions{}); err == nil || !strings.Contains(err.Error(), "belongs to the PR author") {
		t.Fatalf("resolve ran in review mode: %v", err)
	}
	row, err := statusLocalRepo(catchUpTarget{collection: filepath.Base(r.Collection), path: worktree, repo: "widget", ref: gitref.RemoteBranch("origin", "main")})
	if err != nil || !row.ReviewOnly || !strings.HasSuffix(row.BranchDisplay, "(review-only)") {
		t.Fatalf("status does not show review mode: %+v %v", row, err)
	}

	before := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD")
	tip := fixtureGit(t, "-C", worktree, "rev-parse", "origin/main")
	reconcile := catchUpTarget{collection: filepath.Base(r.Collection), path: worktree, repo: "widget", ref: gitref.RemoteBranch("origin", "main")}
	if outcome, reason := target.catchUpReconcile(reconcile, reconcile.ref, tip, catchUpPR{state: "OPEN"}, CatchUpOptions{}); outcome != "current" || !strings.Contains(reason, "review-only") {
		t.Fatalf("catch-up would update the review worktree: %s %s", outcome, reason)
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != before {
		t.Fatalf("catch-up moved the review worktree to %s", got)
	}

	if _, err := target.BuildReviewBundle(ReviewBundleOptions{Repo: "widget", PR: "99"}); err == nil || !strings.Contains(err.Error(), "not PR #99") {
		t.Fatalf("bundle accepted another PR in a review-only collection: %v", err)
	}

	bundle := func() (ReviewBundle, error) {
		return target.BuildReviewBundle(ReviewBundleOptions{Repo: "widget"})
	}
	// The author pushes again: the next bundle fast-forwards, without the
	// merge with the moved tip that catch-up would make.
	fixtureGit(t, "-C", source, "checkout", "-q", "feature")
	second := commit(source, "feature.txt", "second round")
	publish(second)
	b, err := bundle()
	if err != nil {
		t.Fatal(err)
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != second || b.Manifest.HeadSHA != second || b.Manifest.PR != "21" {
		t.Fatalf("bundle did not follow the new head: HEAD %s, manifest %+v", got, b.Manifest)
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "origin/main"); got != tipMoved {
		t.Fatalf("bundle diffed against a stale base %s, want %s", got, tipMoved)
	}

	// The author rewrites the branch: the reviewed head may be left behind.
	fixtureGit(t, "-C", source, "reset", "-q", "--hard", "main")
	rewritten := commit(source, "feature.txt", "rewritten")
	publish(rewritten)
	if _, err := bundle(); err != nil {
		t.Fatal(err)
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != rewritten {
		t.Fatalf("bundle did not follow the rewritten head: %s", got)
	}

	// Following the head moves only the review branch, never one checked
	// out to look around.
	fixtureGit(t, "-C", source, "checkout", "-q", "feature")
	blocked := commit(source, "blocker.txt", "adds a file")
	publish(blocked)
	fixtureGit(t, "-C", worktree, "switch", "-q", "-c", "inspect", "origin/main")
	inspect := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD")
	if _, err := bundle(); err == nil || !strings.Contains(err.Error(), "not its review branch") {
		t.Fatalf("bundle followed the head on another branch: %v", err)
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "inspect"); got != inspect {
		t.Fatalf("the inspected branch moved to %s", got)
	}
	fixtureGit(t, "-C", worktree, "switch", "-q", "feature")

	// A move git refuses stops the bundle.
	fixtureFile(t, filepath.Join(worktree, "blocker.txt"), "untracked\n", 0644)
	if _, err := bundle(); err == nil {
		t.Fatal("bundle went on after the fast-forward was refused")
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != rewritten {
		t.Fatalf("refused fast-forward moved HEAD to %s", got)
	}
	if err := os.Remove(filepath.Join(worktree, "blocker.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := bundle(); err != nil {
		t.Fatal(err)
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != blocked {
		t.Fatalf("bundle did not follow once unblocked: %s", got)
	}

	// A commit made here is never discarded.
	local := commit(worktree, "local.txt", "local review commit")
	fixtureGit(t, "-C", source, "reset", "-q", "--hard", "main")
	publish(commit(source, "feature.txt", "rewritten again"))
	if _, err := bundle(); err == nil || !strings.Contains(err.Error(), "not a PR head this collection recorded") {
		t.Fatalf("local commit was not protected: %v", err)
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != local {
		t.Fatalf("local commit was moved: %s", got)
	}
}

func TestReviewModeNeedsPR(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.NewCollection(NewOptions{Slug: "plain", Review: true}); err == nil || !strings.Contains(err.Error(), "needs a PR") {
		t.Fatalf("review mode without a PR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "plain")); !os.IsNotExist(err) {
		t.Fatalf("refused review collection left a folder: %v", err)
	}
}

func TestReviewModeRefusesSharedPushURLBeforeChangingOwner(t *testing.T) {
	c := newWorkspaceFixture(t)
	source := filepath.Join(c.Workspace, "source-widget")
	head := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureGit(t, "-C", source, "branch", "feature")
	fixtureGit(t, "-C", source, "update-ref", "refs/pull/22/head", head)
	c.Registry.Repos[1].Remote = "https://github.com/example/widget.git"
	bare := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+bare, "config", "remote.origin.pushurl", source)
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' '{\"headRefName\":\"feature\",\"headRefOid\":\""+head+"\",\"title\":\"Review fixture\"}'\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	r, err := c.NewCollection(NewOptions{PR: "widget#22", Review: true})
	if err == nil || !strings.Contains(err.Error(), "remote.origin.pushurl") {
		t.Fatalf("explicit pushurl was not refused: %v", err)
	}
	if _, err := os.Lstat(r.Collection); r.Collection == "" || !os.IsNotExist(err) {
		t.Fatalf("refusal left a collection with a pushable checkout: %v", err)
	}
	if got, _ := gitOutput("--git-dir="+bare, "config", "extensions.worktreeConfig"); got != "" {
		t.Fatalf("owner was changed before the refusal: extensions.worktreeConfig=%s", got)
	}
	if got := fixtureGit(t, "--git-dir="+bare, "config", "--bool", "core.bare"); got != "true" {
		t.Fatalf("owner core.bare moved before the refusal: %s", got)
	}
}

func TestReviewModeFailedLockLeavesCollectionReviewOnly(t *testing.T) {
	c := newWorkspaceFixture(t)
	source := filepath.Join(c.Workspace, "source-widget")
	head := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureGit(t, "-C", source, "branch", "feature")
	fixtureGit(t, "-C", source, "update-ref", "refs/pull/23/head", head)
	c.Registry.Repos[1].Remote = "https://github.com/example/widget.git"
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' '{\"headRefName\":\"feature\",\"headRefOid\":\""+head+"\",\"title\":\"Review fixture\"}'\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	// An insteadOf rule elsewhere rewrites even the dead push URL, so the
	// lock's own verification fails after the checkout exists.
	global := filepath.Join(c.Workspace, "global.gitconfig")
	fixtureFile(t, global, "[url \""+source+"/\"]\n\tinsteadOf = "+reviewPushPrefix+"\n", 0644)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	r, err := c.NewCollection(NewOptions{PR: "widget#23", Review: true})
	if err == nil || !strings.Contains(err.Error(), "after the review-only lock") {
		t.Fatalf("lock failure was not reported: %v", err)
	}
	if !reviewOnlyWorktree(filepath.Join(r.Collection, "widget")) {
		t.Fatal("partial collection with a failed lock is not review-only")
	}
	target, err := OpenCollection(r.Collection)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.RefuseInReviewMode("resolving review threads"); err == nil {
		t.Fatal("author steps run in the partial collection")
	}
}
