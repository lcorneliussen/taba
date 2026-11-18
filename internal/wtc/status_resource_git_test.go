package wtc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Frozen pre-optimization collector oracle; actual Git fixtures compare complete
// local rows, including development-base and operation authority.
func statusResourceLocalReference(ctx context.Context, t catchUpTarget) (StatusRepo, error) {
	dir := filepath.Base(t.path)
	row := StatusRepo{Collection: t.collection, Dir: dir, Repo: t.repo,
		Worktree: t.path, Tree: "clean", baseRef: t.ref, ComparisonRef: t.ref, DevelopmentBase: t.developmentBase, RenamedTo: t.renamed}
	remote, err := statusGitContext(ctx, t.path, "remote", "get-url", "origin")
	if err == nil {
		row.Slug, row.Forge = catchUpForge(remote)
	}
	branch, err := statusGitContext(ctx, t.path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		row.BranchKind = "branch"
		row.Branch = branch
		row.BranchDisplay = branch
	} else {
		row.BranchKind = "detached"
		row.Branch = catchUpBranchName(t.ref)
		row.BranchDisplay = "⌂ " + row.Branch
	}
	if reviewOnlyWorktree(t.path) {
		row.ReviewOnly = true
		row.BranchDisplay += " (review-only)"
	}
	upstream := t.ref
	row.AheadBasis, row.UpstreamState = "development-tip", "none"
	if row.BranchKind == "branch" {
		if found, err := statusGitContext(ctx, t.path, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
			upstream = found
			row.AheadBasis, row.UpstreamState = "upstream", "tracking"
		} else if tracking, err := statusGitContext(ctx, t.path, "for-each-ref", "--format=%(upstream:track)", "refs/heads/"+row.Branch); err == nil && tracking == "[gone]" {
			row.UpstreamState = "gone"
			row.BranchDisplay += " [gone]"
		}
	}
	row.AheadRef = upstream
	row.Ahead, err = statusCountContext(ctx, t.path, upstream+"..HEAD")
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s ahead count: %w", t.path, err)
	}
	row.Behind, err = statusCountContext(ctx, t.path, "HEAD.."+t.ref)
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s development-tip count: %w", t.path, err)
	}
	porcelain, err := statusGitContext(ctx, t.path, "status", "--porcelain")
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s working tree: %w", t.path, err)
	}
	if porcelain != "" {
		row.Changed = len(strings.Split(porcelain, "\n"))
		row.Tree = fmt.Sprintf("±%d", row.Changed)
	}
	row.Operation = catchUpOperation(t)
	if unmerged, err := statusGitContext(ctx, t.path, "diff", "--name-only", "--diff-filter=U"); err == nil && unmerged != "" {
		row.Conflict = true
	}
	return row, nil
}

func TestStatusResourceLocalActualGitComparison(t *testing.T) {
	c := newWorkspaceFixture(t)
	_, targets, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var target catchUpTarget
	for _, v := range targets {
		if v.path == c.Harness {
			target = v
		}
	}
	if target.path == "" {
		t.Fatal("no fixture target")
	}
	check := func(label string) {
		t.Helper()
		got, ge := statusLocalRepoContext(context.Background(), target)
		want, we := statusResourceLocalReference(context.Background(), target)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s got=%+v err=%v want=%+v err=%v", label, got, ge, want, we)
		}
	}
	check("detached")
	markers := []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"}
	args := []string{"-C", target.path, "rev-parse", "--path-format=absolute"}
	var expected []string
	for _, marker := range markers {
		args = append(args, "--git-path", marker)
		expected = append(expected, fixtureGit(t, "-C", target.path, "rev-parse", "--path-format=absolute", "--git-path", marker))
	}
	if got := fixtureGit(t, args...); got != strings.Join(expected, "\n") {
		t.Fatalf("batched actual Git paths differ: %q vs%q", got, expected)
	}
	fixtureGit(t, "-C", target.path, "switch", "-qc", "topic")
	check("branch-no-upstream")
	fixtureGit(t, "-C", target.path, "branch", "--set-upstream-to=origin/main")
	check("tracking")
	fixtureFile(t, filepath.Join(target.path, "untracked"), "new", 0600)
	check("untracked")
	fixtureGit(t, "-C", target.path, "add", "untracked")
	check("staged")
	fixtureGit(t, "-C", target.path, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic local")
	check("ahead")
	fixtureFile(t, filepath.Join(target.path, "untracked"), "changed", 0600)
	check("dirty")
	fixtureGit(t, "-C", target.path, "update-ref", "-d", "refs/remotes/origin/main")
	check("missing-base")
	fixtureGit(t, "-C", target.path, "update-ref", "refs/remotes/origin/main", "HEAD")
	check("changed-base")
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		path := fixtureGit(t, "-C", target.path, "rev-parse", "--path-format=absolute", "--git-path", marker)
		fixtureFile(t, path, "fixture", 0600)
		check(marker)
		os.Remove(path)
	}
	fixtureGit(t, "-C", target.path, "reset", "--hard", "HEAD")
	fixtureGit(t, "-C", target.path, "switch", "--orphan", "unborn")
	check("unborn")
}
