package next

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFor(t *testing.T) {
	no, yes := false, true
	for _, tc := range []struct {
		name string
		f    Facts
		step string // "" for none
		by   string
		ok   func(*Step) bool
	}{
		{"nothing to do", Facts{Repo: "api"}, "", "", nil},
		{"work in progress", Facts{Repo: "api", Branch: "12-x", Ahead: 1, Dirty: 2}, "", "", nil},
		{"a merge in progress is the agent's", Facts{Repo: "api", InProgress: "a merge", Conflicted: 2}, "resolve", ByAgent,
			func(s *Step) bool { return s.Skill == "taba-catch-up" }},
		{"a merged PR not in the checkout: back to the tip", Facts{Repo: "api", Host: HostOpenClaw, Base: "origin/main", Branch: "12-x", PR: &PR{Number: 7, State: "merged", MergedIn: &no}}, "return-to-tip", ByWTC,
			func(s *Step) bool {
				return reflect.DeepEqual(s.Command, []string{"wtc", "catch-up"}) && s.Approve == "" && s.Reason == "#7 was merged: back to main's tip; the next change branches anew"
			}},
		{"merged, and main moved on: back to the tip, never a merge into it", Facts{Repo: "api", Base: "origin/main", Branch: "12-x", Behind: 21, PR: &PR{Number: 7, State: "merged", MergedIn: &yes}}, "return-to-tip", ByWTC,
			func(s *Step) bool { return s.Approve == "" }},
		{"merged, and at the tip: leave the branch", Facts{Repo: "api", Branch: "12-x", PR: &PR{Number: 7, State: "merged", MergedIn: &yes}}, "return-to-tip", ByWTC, nil},
		{"a catch-up's merge after the merge is no work", Facts{Repo: "api", Branch: "12-x", ToPush: 1, PR: &PR{Number: 7, State: "merged", MergedIn: &yes}}, "return-to-tip", ByWTC, nil},
		{"commits after the merge go to a new branch", Facts{Repo: "api", Branch: "12-x", ToPush: 2, AfterMerge: 2, PR: &PR{Number: 7, State: "merged", MergedIn: &yes}}, "new-branch", ByAgent,
			func(s *Step) bool {
				return s.Reason == "#7 was merged, but 2 commits came after it: put them on a new branch"
			}},
		{"detached with commits of its own: a branch first", Facts{Repo: "api", Ahead: 1, Behind: 3}, "new-branch", ByAgent, nil},
		{"a conflicting PR is merged by the agent", Facts{Repo: "api", Base: "origin/main", PR: &PR{Number: 7, State: "open", Mergeable: "conflicting"}}, "merge", ByAgent,
			func(s *Step) bool { return s.Reason == "#7 conflicts with main" }},
		{"failed checks", Facts{Repo: "api", PR: &PR{Number: 7, State: "open", Checks: "failure"}}, "fix-checks", ByAgent,
			func(s *Step) bool { return s.Skill == "taba-follow" }},
		{"changes requested", Facts{Repo: "api", PR: &PR{Number: 7, State: "open", Checks: "success", Review: "changes_requested"}}, "address-review", ByAgent, nil},
		{"behind, merging conflicts", Facts{Repo: "api", Base: "origin/main", Behind: 3, Branch: "12-x", Ahead: 1, MergeConflicts: []string{"README.md"}}, "merge", ByAgent,
			func(s *Step) bool { return s.Reason == "3 new on main; merging conflicts in README.md" }},
		{"behind and detached: wtc moves it", Facts{Repo: "api", Base: "origin/main", Behind: 5}, "catch-up", ByWTC,
			func(s *Step) bool {
				return s.Approve == "" && reflect.DeepEqual(s.Command, []string{"wtc", "catch-up", "--repos", "api"})
			}},
		{"behind on a live branch: clean, but the agent's call", Facts{Repo: "api", Base: "origin/main", Behind: 2, Branch: "12-x", Ahead: 1}, "catch-up", ByWTC,
			func(s *Step) bool { return s.Approve == ApproveAgent && s.Reason == "2 new on main; merges cleanly" }},
		{"behind on a pushed branch, nothing ahead: still the agent's call", Facts{Repo: "api", Base: "origin/main", Behind: 2, Branch: "12-x"}, "catch-up", ByWTC,
			func(s *Step) bool { return s.Approve == ApproveAgent }},
		{"checks running don't hold back a push", Facts{Repo: "api", Host: HostOpenClaw, ToPush: 1, PR: &PR{Number: 7, State: "open", Checks: "pending"}}, "push", ByWTC, nil},
		{"checks running don't hold back a catch-up", Facts{Repo: "api", Behind: 1, PR: &PR{Number: 7, State: "open", Checks: "pending"}}, "catch-up", ByWTC, nil},
		{"behind, unknown whether it conflicts", Facts{Repo: "api", Behind: 2, MergeUnknown: true}, "catch-up", ByWTC,
			func(s *Step) bool { return s.Approve == ApproveAgent }},
		{"behind with uncommitted files: wait for the work", Facts{Repo: "api", Behind: 2, Dirty: 1}, "", "", nil},
		{"commits to push on the gateway are the person's", Facts{Repo: "api", Host: HostOpenClaw, ToPush: 2}, "push", ByWTC,
			func(s *Step) bool {
				return s.Approve == ApprovePerson && s.Gateway == "github_publish" && s.Reason == "2 commits to push, opening a draft pull request"
			}},
		{"commits to push locally are the agent's", Facts{Repo: "api", Host: HostLocal, ToPush: 2, PR: &PR{Number: 7, State: "open"}}, "push", ByAgent,
			func(s *Step) bool { return s.Skill == "taba-draft-pr" && s.Reason == "2 commits to push" }},
		{"approved and green: people merge", Facts{Repo: "api", PR: &PR{Number: 7, State: "open", Checks: "success", Review: "approved", Mergeable: "mergeable"}}, "merge-pr", ByPerson,
			func(s *Step) bool { return s.Approve == "" && reflect.DeepEqual(s.Then, []string{"catch-up"}) }},
		{"approved, but local commits not pushed: push first", Facts{Repo: "api", Host: HostOpenClaw, ToPush: 1, PR: &PR{Number: 7, State: "open", Checks: "success", Review: "approved", Mergeable: "mergeable"}}, "push", ByWTC, nil},
		{"approved, but work in progress: nothing", Facts{Repo: "api", Dirty: 1, PR: &PR{Number: 7, State: "open", Checks: "success", Review: "approved"}}, "", "", nil},
		{"failed checks while the agent works: nothing yet", Facts{Repo: "api", Dirty: 2, PR: &PR{Number: 7, State: "open", Checks: "failure"}}, "", "", nil},
		{"a conflicting PR during work: nothing yet", Facts{Repo: "api", Dirty: 1, PR: &PR{Number: 7, State: "open", Mergeable: "conflicting"}}, "", "", nil},
		{"conflicted files without an operation are resolved", Facts{Repo: "api", Conflicted: 1, Dirty: 1}, "resolve", ByAgent,
			func(s *Step) bool { return s.Reason == "a merge is not finished in api: 1 conflicted files" }},
		{"an approved draft is no merge yet", Facts{Repo: "api", PR: &PR{Number: 7, State: "open", Draft: true, Checks: "success", Review: "approved"}}, "", "", nil},
		{"merged, with work in progress: nothing yet", Facts{Repo: "api", Dirty: 1, PR: &PR{Number: 7, State: "merged"}}, "", "", nil},
		{"checks running: nothing yet", Facts{Repo: "api", PR: &PR{Number: 7, State: "open", Checks: "pending"}}, "", "", nil},
	} {
		got := For(tc.f)
		if tc.step == "" {
			if got != nil {
				t.Errorf("%s: %+v, want none", tc.name, got)
			}
			continue
		}
		if got == nil || got.Step != tc.step || got.By != tc.by || got.Repo != "api" || got.Key == "" || (tc.ok != nil && !tc.ok(got)) {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}

// The key changes with the facts it is about, and only with them.
func TestKey(t *testing.T) {
	a := For(Facts{Repo: "api", Behind: 2, Head: "h1", BaseHead: "b1"})
	again := For(Facts{Repo: "api", Behind: 2, Head: "h1", BaseHead: "b1"})
	moved := For(Facts{Repo: "api", Behind: 3, Head: "h1", BaseHead: "b2"})
	if a.Key != again.Key || a.Key == moved.Key || a.Key != "api:catch-up:h1:b1" {
		t.Errorf("keys: %q %q %q", a.Key, again.Key, moved.Key)
	}
	other := For(Facts{Collection: "other", Repo: "api", Behind: 2, Head: "h1", BaseHead: "b1"})
	if other.Key == a.Key || !reflect.DeepEqual(other.Command, []string{"wtc", "catch-up", "other", "--repos", "api"}) {
		t.Fatalf("collection target: %+v", other)
	}
}

func TestPredictMerge(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.md", "one\n")
	write("b.md", "one\n")
	git("add", ".")
	git("commit", "-qm", "base")
	git("branch", "other")
	write("a.md", "mine\n")
	git("commit", "-qam", "mine")
	git("switch", "-q", "other")
	write("b.md", "theirs\n")
	git("commit", "-qam", "clean change")
	git("switch", "-q", "main")
	if got, ok := PredictMerge(dir, "other"); !ok || len(got) != 0 {
		t.Fatalf("clean: %v %v", got, ok)
	}
	git("switch", "-q", "other")
	write("a.md", "theirs\n")
	git("commit", "-qam", "conflicting change")
	git("switch", "-q", "main")
	if got, ok := PredictMerge(dir, "other"); !ok || !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Fatalf("conflict: %v %v", got, ok)
	}
	// Nothing was touched.
	if out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); len(out) != 0 {
		t.Errorf("the worktree changed: %s", out)
	}
	if _, ok := PredictMerge(dir, "no-such-ref"); ok {
		t.Error("an unknown ref was predicted")
	}
}
