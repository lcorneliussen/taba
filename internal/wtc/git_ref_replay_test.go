package wtc

import (
	"path/filepath"
	"testing"
)

// A remote-less owner's development branch is refs/heads/<branch>. A tag of
// the same name on an older commit must not stand in for it, neither as the
// catch-up target nor as the ref status counts against.
func TestRemotelessOwnerBranchIsNotShadowedByTag(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, "tagged")
	fixtureGit(t, "init", "-q", "-b", "main", owner)
	commit := func(name string) string {
		fixtureFile(t, filepath.Join(owner, name), name+"\n", 0644)
		fixtureGit(t, "-C", owner, "add", name)
		fixtureGit(t, "-C", owner, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", name)
		return fixtureGit(t, "-C", owner, "rev-parse", "HEAD")
	}
	one := commit("one")
	two := commit("two")
	tip := commit("three")
	fixtureGit(t, "-C", owner, "tag", "main", two)
	worktree := filepath.Join(c.Collection, "ext.tagged")
	fixtureGit(t, "-C", owner, "worktree", "add", "-q", "--detach", worktree, one)

	snapshot, err := c.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range snapshot.Repos {
		if row.Dir == "ext.tagged" {
			found = true
			// two and three are on refs/heads/main and not in HEAD.
			if row.Behind != 2 || row.Ahead != 0 {
				t.Fatalf("status counted against the tag: ahead %d behind %d, want 0 and 2", row.Ahead, row.Behind)
			}
		}
	}
	if !found {
		t.Fatalf("status omitted the remote-less sibling: %+v", snapshot.Repos)
	}

	report, err := c.CatchUp(CatchUpOptions{Repos: []string{"ext.tagged"}, NoSkills: true, NoMCP: true, NoEnv: true})
	if err != nil || report.ExitStatus != 0 {
		t.Fatalf("catch-up failed: %+v %v", report, err)
	}
	for _, row := range report.Outcomes {
		if row.Kind == "repo" && (row.TargetSHA == nil || *row.TargetSHA != tip) {
			t.Fatalf("catch-up targeted the tag, want refs/heads/main at %s: %+v", tip, row)
		}
	}
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != tip {
		t.Fatalf("worktree at %s after catch-up, want refs/heads/main at %s", got, tip)
	}
}
