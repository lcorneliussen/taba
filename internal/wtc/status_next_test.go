package wtc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"taba.sh/taba/internal/next"
)

// A local row's facts become its next step: pushing is the agent's here,
// and the pull request's facts read as the forge reports them.
func TestStatusNextFromALocalRow(t *testing.T) {
	// No git in the worktree: the mapping is what is tested here, and git
	// must not read the checkout the test runs in. Predicting the merge is
	// tested in next and on the gateway.
	none := t.TempDir()
	branch := func(fields StatusRepo) StatusRepo {
		fields.Repo, fields.BranchKind, fields.Branch, fields.baseRef, fields.Worktree = "api", "branch", "12-paging", "origin/main", none
		return fields
	}
	for _, tc := range []struct {
		name string
		row  StatusRepo
		want string
		by   string
	}{
		{"commits to push: the agent's draft PR", branch(StatusRepo{Ahead: 2}), "push", next.ByAgent},
		{"failed checks", branch(StatusRepo{PR: &StatusPRFacts{Number: "7", Checks: "FAILURE", Merge: "CLEAN", Review: "waiting"}}), "fix-checks", next.ByAgent},
		{"conflicting with its base", branch(StatusRepo{PR: &StatusPRFacts{Number: "7", Checks: "SUCCESS", Merge: "CONFLICTING"}}), "merge", next.ByAgent},
		{"changes requested", branch(StatusRepo{PR: &StatusPRFacts{Number: "7", Checks: "SUCCESS", Merge: "CLEAN", Review: "changes"}}), "address-review", next.ByAgent},
		{"approved and green", branch(StatusRepo{PR: &StatusPRFacts{Number: "7", Checks: "SUCCESS", Merge: "CLEAN", Review: "approved"}}), "merge-pr", next.ByPerson},
		{"a merge in progress", branch(StatusRepo{Operation: "merge", Conflict: true}), "resolve", next.ByAgent},
		{"a pushed branch behind its base: the agent decides", branch(StatusRepo{Behind: 2}), "catch-up", next.ByWTC},
		{"uncommitted files: wait", branch(StatusRepo{Ahead: 1, Behind: 2, Changed: 3}), "", ""},
		{"detached and current: nothing", StatusRepo{Repo: "api", BranchKind: "detached", Branch: "main", Worktree: none}, "", ""},
	} {
		got := next.For(statusFacts(tc.row))
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("%s: %+v, want none", tc.name, got)
		case tc.want != "" && (got == nil || got.Step != tc.want || got.By != tc.by):
			t.Errorf("%s: %+v, want %s by %s", tc.name, got, tc.want, tc.by)
		case tc.want == "catch-up" && got.Approve != next.ApproveAgent:
			t.Errorf("%s: a catch-up onto a branch needs the agent's go: %+v", tc.name, got)
		}
	}
}

func TestStatusNextSkipsReviewOnly(t *testing.T) {
	snapshot := StatusSnapshot{Repos: []StatusRepo{{Repo: "api", ReviewOnly: true, BranchKind: "branch", Behind: 3,
		Worktree: t.TempDir(), PR: &StatusPRFacts{Number: "7", Checks: "FAILURE"}}}}
	statusNext(&snapshot)
	if snapshot.Repos[0].Next != nil {
		t.Fatalf("review checkout received authoring work: %+v", snapshot.Repos[0].Next)
	}
}

func TestStatusSnapshotNextAndRetainedMergedWork(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	c := newWorkspaceFixture(t)
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	fixtureFile(t, filepath.Join(c.Harness, "work"), "first round\n", 0644)
	fixtureGit(t, "-C", c.Harness, "add", "work")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-qm", "first round")
	merged := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
	fixtureGit(t, "-C", c.Harness, "update-ref", "refs/remotes/origin/main", merged)
	fixtureFile(t, filepath.Join(c.Harness, "follow-up"), "next round\n", 0644)
	fixtureGit(t, "-C", c.Harness, "add", "follow-up")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-qm", "next round")
	if _, err := c.EnlistPR(PRRecord{Repo: "agent-harness", Number: "7", Branch: "topic"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snapshot.Repos {
		if r.Dir == "harness" && (r.baseRef == "" || r.Next == nil || r.Next.Step != "push") {
			t.Fatalf("local snapshot dropped next: %+v", r)
		}
	}
	c.Registry.Repos[0].Remote = "https://github.com/example/harness.git"
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' '{\"number\":7,\"state\":\"MERGED\",\"title\":\"First round\",\"mergeCommit\":{\"oid\":\"" + merged + "\"}}'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	snapshot, err = c.statusForgePreview(false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snapshot.Repos {
		if r.Dir == "harness" && (r.Next == nil || r.Next.Step != "new-branch" || r.Next.By != next.ByAgent) {
			t.Fatalf("merged follow-up work: %+v", r.Next)
		}
	}
	// Each repository's command selects only that repository, even when a
	// sibling is live and would need a separate approval.
	snapshot = StatusSnapshot{Repos: []StatusRepo{
		{Repo: "api", Worktree: t.TempDir(), BranchKind: "detached", Behind: 1},
		{Repo: "web", Worktree: t.TempDir(), BranchKind: "branch", Branch: "live", Behind: 1},
	}}
	statusNext(&snapshot)
	if got := snapshot.Repos[0].Next; got == nil || !reflect.DeepEqual(got.Command, []string{"wtc", "catch-up", "--repos", "api"}) {
		t.Fatalf("unscoped step: %+v", got)
	}
}

func TestLocalNextCatchUpLeavesSiblingAlone(t *testing.T) {
	c := newWorkspaceFixture(t)
	widget := filepath.Join(c.Collection, "widget")
	fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "widget.git"), "worktree", "add", "-q", "--detach", widget, "origin/main")
	fixtureGit(t, "-C", widget, "switch", "-qc", "live")
	fixtureFile(t, filepath.Join(widget, "pending"), "keep this work\n", 0644)
	before := fixtureGit(t, "-C", widget, "rev-parse", "HEAD")
	f := statusFacts(StatusRepo{Repo: "agent-harness", Dir: "harness", BranchKind: "detached", Behind: 1, Worktree: c.Harness})
	step := next.For(f)
	if step == nil || len(step.Command) != 4 {
		t.Fatalf("command: %+v", step)
	}
	_, err := c.CatchUp(CatchUpOptions{Repos: []string{step.Command[3]}, NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if fixtureGit(t, "-C", widget, "rev-parse", "HEAD") != before || fixtureGit(t, "-C", widget, "branch", "--show-current") != "live" {
		t.Fatal("catch-up changed the unselected sibling")
	}
	if data, err := os.ReadFile(filepath.Join(widget, "pending")); err != nil || string(data) != "keep this work\n" {
		t.Fatalf("sibling's pending file: %s %v", data, err)
	}
}

func TestStatusNextUsesPullRequestBase(t *testing.T) {
	for _, mode := range []string{"enlisted", "discovered"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			c := newWorkspaceFixture(t)
			fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
			fixtureFile(t, filepath.Join(c.Harness, "README.md"), "ours\n", 0644)
			fixtureGit(t, "-C", c.Harness, "add", "README.md")
			fixtureGit(t, "-C", c.Harness, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-qm", "our change")
			source := filepath.Join(c.Workspace, "source-agent-harness")
			fixtureGit(t, "-C", source, "switch", "-qc", "release")
			fixtureFile(t, filepath.Join(source, "README.md"), "theirs\n", 0644)
			fixtureGit(t, "-C", source, "add", "README.md")
			fixtureGit(t, "-C", source, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-qm", "release change")
			fixtureGit(t, "-C", c.Harness, "fetch", "-q", "origin", "+refs/heads/release:refs/remotes/origin/release")
			c.Registry.Repos[0].Remote = "https://github.com/example/harness.git"
			if mode == "enlisted" {
				if _, err := c.EnlistPR(PRRecord{Repo: "agent-harness", Number: "7", Branch: "topic"}); err != nil {
					t.Fatal(err)
				}
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nif [ \"$2\" = list ]; then printf '[{\"number\":7,\"headRefName\":\"topic\"}]'; exit; fi\nprintf '%s' '{\"number\":7,\"state\":\"OPEN\",\"title\":\"Change\",\"baseRefName\":\"release\"}'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			snapshot, err := c.statusForgePreview(false, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range snapshot.Repos {
				if r.Dir == "harness" && (r.baseRef != "origin/release" || r.Next == nil || r.Next.Step != "merge") {
					t.Fatalf("did not predict against the PR's base: %+v", r)
				}
			}
		})
	}
}

func TestWorkspaceNextTargetsTheOtherCollection(t *testing.T) {
	c := newWorkspaceFixture(t)
	beta, err := c.NewCollection(NewOptions{Slug: "beta", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "live")
	fixtureFile(t, filepath.Join(c.Harness, "pending"), "keep current work\n", 0644)
	before := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
	source := filepath.Join(c.Workspace, "source-agent-harness")
	fixtureFile(t, filepath.Join(source, "README.md"), "new base\n", 0644)
	fixtureGit(t, "-C", source, "add", "README.md")
	fixtureGit(t, "-C", source, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-qm", "base advances")
	fixtureGit(t, "-C", c.Harness, "fetch", "-q", "origin", "+refs/heads/main:refs/remotes/origin/main")
	snapshot, err := c.StatusLocalSnapshot(true)
	if err != nil {
		t.Fatal(err)
	}
	var step *next.Step
	for _, r := range snapshot.Repos {
		if r.Collection == "beta" && r.Dir == "harness" {
			step = r.Next
		}
	}
	if step == nil || !reflect.DeepEqual(step.Command, []string{"wtc", "catch-up", "beta", "--repos", "agent-harness"}) {
		t.Fatalf("wrong collection command: %+v", step)
	}
	_, err = c.CatchUp(CatchUpOptions{Collections: []string{step.Command[2]}, Repos: []string{step.Command[4]},
		NoSkills: true, NoMCP: true, NoEnv: true, NoSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD") != before || fixtureGit(t, "-C", c.Harness, "branch", "--show-current") != "live" {
		t.Fatal("other collection's step moved the current checkout")
	}
	if data, err := os.ReadFile(filepath.Join(c.Harness, "pending")); err != nil || string(data) != "keep current work\n" {
		t.Fatalf("current collection's work: %s %v", data, err)
	}
	if fixtureGit(t, "-C", filepath.Join(beta.Collection, "harness"), "rev-parse", "HEAD") != fixtureGit(t, "-C", source, "rev-parse", "HEAD") {
		t.Fatal("target collection was not caught up")
	}
}
