package openclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mergedPRCommand(t *testing.T, head, merge string) {
	t.Helper()
	bin := t.TempDir()
	data, err := json.Marshal([]any{map[string]any{
		"number": 7, "url": "https://github.com/example-org/docs/pull/7", "state": "MERGED",
		"title": "First round", "headRefOid": head, "mergeCommit": map[string]string{"oid": merge},
	}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(bin, "pr.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ncase \"$*\" in *headRefOid*) ;; *) exit 2 ;; esac\ncat \"$WTC_TEST_PR_JSON\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_TEST_PR_JSON", file)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCatchUpReturnsMergedBranchesToTip(t *testing.T) {
	for _, mode := range []string{"merge", "squash", "already-at-tip", "old-catch-up", "base-advanced-after-old-catch-up"} {
		t.Run(mode, func(t *testing.T) {
			c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
			bare := git("remote", "get-url", "--push", "origin")
			git("remote", "set-url", "origin", bare)
			write(t, c, "feature", "first round\n")
			git("add", "feature")
			git("commit", "-qm", "first round")
			head := git("rev-parse", "HEAD")
			if mode == "squash" || mode == "old-catch-up" || mode == "base-advanced-after-old-catch-up" {
				advanceOrigin(t, bare, "feature", "first round\n")
			} else {
				merge := git("commit-tree", "-p", "origin/main", "-p", "HEAD", "-m", "Merge first round", "HEAD^{tree}")
				git("push", "-q", bare, merge+":refs/heads/main")
			}
			git("fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main")
			merge := git("rev-parse", "origin/main")
			advanceOrigin(t, bare, "later", "base moved\n")
			git("fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main")
			if mode == "already-at-tip" || mode == "old-catch-up" || mode == "base-advanced-after-old-catch-up" {
				git("merge", "-q", "--no-edit", "origin/main")
			}
			if mode == "base-advanced-after-old-catch-up" {
				advanceOrigin(t, bare, "newer", "base moved again\n")
			}
			old := git("rev-parse", "HEAD")
			mergedPRCommand(t, head, merge)
			r, err := c.CatchUp()
			if err != nil || !r.ReturnedToTip || r.Branch != "" || r.Head != git("rev-parse", "origin/main") {
				t.Fatalf("catch-up: %+v %v", r, err)
			}
			if git("branch", "--show-current") != "" || git("rev-parse", "refs/heads/"+c.Branch()) != old {
				t.Fatal("did not detach, or rewrote the completed branch")
			}
			s := checkoutStatus(c.Dir, true)
			if s.Error != "" || s.Branch != "" || s.PullRequest != nil || len(s.PullRequests) != 1 || s.PullRequests[0].Number != 7 || s.Next != nil {
				t.Fatalf("after catch-up: %+v", s)
			}
			if git("status", "--porcelain") != "" {
				t.Fatal("history leaked into the checkout")
			}
			// Reopening and catching up again keeps the history, once only.
			fresh, err := OpenCatchUpCheckout(c.Dir, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fresh.CatchUp(); err != nil || len(checkoutStatus(c.Dir, false).PullRequests) != 1 {
				t.Fatalf("repeat: %v", err)
			}
		})
	}
}

func TestMergedCatchUpPreservesFollowUpWork(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	bare := git("remote", "get-url", "--push", "origin")
	git("remote", "set-url", "origin", bare)
	write(t, c, "feature", "first round\n")
	git("add", "feature")
	git("commit", "-qm", "first round")
	head := git("rev-parse", "HEAD")
	advanceOrigin(t, bare, "feature", "first round\n")
	git("fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main")
	merge := git("rev-parse", "origin/main")
	mergedPRCommand(t, head, merge)
	write(t, c, "follow-up", "new work\n")
	git("add", "follow-up")
	git("commit", "-qm", "next round")
	before := git("rev-parse", "HEAD")
	git("remote", "set-url", "origin", "https://github.com/example-org/docs.git")
	s := checkoutStatus(c.Dir, true)
	if s.PullRequest == nil || s.PullRequest.Head != head || s.Next == nil || s.Next.Step != "new-branch" {
		t.Fatalf("decoded follow-up state: %+v", s)
	}
	git("remote", "set-url", "origin", bare)
	if _, err := c.CatchUp(); err == nil || !strings.Contains(err.Error(), "new branch") {
		t.Fatalf("follow-up work was accepted: %v", err)
	}
	if git("rev-parse", "HEAD") != before || git("branch", "--show-current") != c.Branch() {
		t.Fatal("follow-up work moved")
	}
}

func TestCompletedRoundKeepsNonDefaultBase(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	git("update-ref", "refs/remotes/origin/release", "HEAD")
	if err := RecordBase(c.Dir, c.Worktree, "release"); err != nil {
		t.Fatal(err)
	}
	c.Dest = "release"
	if _, err := c.returnToTip(CatchUp{}, &PullRequest{Number: 7, State: "merged"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenCatchUpCheckout(c.Dir, "")
	if err != nil || fresh.Dest != "release" || checkoutStatus(c.Dir, false).Base != "origin/release" {
		t.Fatalf("lost base: %+v %v", fresh, err)
	}
}

func TestResolutionWorkInLegacyMergeGetsANewBranch(t *testing.T) {
	c, git := managedWorktree(t, "https://github.com/example-org/docs.git")
	bare := git("remote", "get-url", "--push", "origin")
	git("remote", "set-url", "origin", bare)
	write(t, c, "feature", "first round\n")
	git("add", "feature")
	git("commit", "-qm", "first round")
	head := git("rev-parse", "HEAD")
	advanceOrigin(t, bare, "feature", "first round\n")
	git("fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main")
	merge := git("rev-parse", "origin/main")
	git("merge", "-q", "--no-edit", "origin/main")
	// Work recorded only in the merge's tree, as manual resolution can do.
	write(t, c, "resolution", "keep this follow-up work\n")
	git("add", "resolution")
	git("commit", "-q", "--amend", "--no-edit")
	before := git("rev-parse", "HEAD")
	mergedPRCommand(t, head, merge)
	git("remote", "set-url", "origin", "https://github.com/example-org/docs.git")
	s := checkoutStatus(c.Dir, true)
	if s.Next == nil || s.Next.Step != "new-branch" {
		t.Fatalf("resolution work: %+v", s.Next)
	}
	git("remote", "set-url", "origin", bare)
	if _, err := c.CatchUp(); err == nil || !strings.Contains(err.Error(), "follow-up work") {
		t.Fatalf("edited merge was accepted: %v", err)
	}
	if git("rev-parse", "HEAD") != before || git("branch", "--show-current") != c.Branch() {
		t.Fatal("resolution work was moved")
	}
}
