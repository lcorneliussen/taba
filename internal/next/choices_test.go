package next_test

import (
	"reflect"
	"taba.sh/taba/internal/next"
	"taba.sh/taba/internal/next/nexttest"
	"testing"
)

func TestChoicesScenarios(t *testing.T) {
	for _, tc := range []struct {
		name    string
		f       next.Facts
		primary string
	}{
		{"green unreviewed", next.Facts{Repo: "api", PR: &next.PR{Number: 7, State: "open", Checks: "success", Mergeable: "mergeable"}}, "follow-pr"},
		{"unpublished fix supersedes old failed check", next.Facts{Repo: "api", Branch: "topic", ToPush: 1, PR: &next.PR{Number: 7, State: "open", Checks: "failure"}}, "push"},
		{"unknown mergeability", next.Facts{Repo: "api", PR: &next.PR{Number: 7, State: "open", Checks: "success", Review: "approved"}}, "follow-pr"},
		{"draft review", next.Facts{Repo: "api", PR: &next.PR{Number: 7, State: "open", Draft: true, Checks: "success"}}, "follow-pr"},
		{"commit dirty", next.Facts{Repo: "api", Branch: "topic", Dirty: 2}, "commit"},
		{"preserve dirty merged", next.Facts{Repo: "api", Branch: "topic", Dirty: 2, PR: &next.PR{Number: 7, State: "merged"}}, "new-branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := next.ChoicesFor(tc.f)
			s := c.Primary()
			if s == nil || s.Step != tc.primary {
				t.Fatalf("%+v", c)
			}
			if c.Select(s.Key) == nil {
				t.Fatal("primary cannot be selected")
			}
		})
	}
}

func TestGeneratedChoices(t *testing.T) {
	for _, tc := range nexttest.Cases() {
		t.Run(tc.ID, func(t *testing.T) {
			c := next.ChoicesFor(tc.Facts)
			if !reflect.DeepEqual(c, next.ChoicesFor(tc.Facts)) {
				t.Fatal("nondeterministic")
			}
			seen := map[string]bool{}
			for _, a := range c.Actions {
				if seen[a.Key] {
					t.Fatal("duplicate key")
				}
				seen[a.Key] = true
				if a.Repo != tc.Facts.Repo {
					t.Fatal("wrong repo")
				}
				if !a.Available && (a.Blocked == "" || c.Select(a.Key) != nil) {
					t.Fatal("blocked action dispatchable")
				}
				if a.Step.Step == "merge-pr" && a.Available && (tc.Facts.Dirty > 0 || tc.Facts.ToPush > 0 || tc.Facts.Behind > 0 || tc.Facts.PR == nil || tc.Facts.PR.State != "open" || tc.Facts.PR.Draft || tc.Facts.PR.Checks != "success" || tc.Facts.PR.Review != "approved" || tc.Facts.PR.Mergeable != "mergeable") {
					t.Fatal("unsafe merge suggestion")
				}
				if tc.Facts.PR != nil && tc.Facts.PR.State == "merged" && a.Step.Step == "push" {
					t.Fatal("push on completed branch")
				}
				moved := tc.Facts
				moved.Head += "-moved"
				if next.ChoicesFor(moved).Select(a.Key) != nil {
					t.Fatal("stale key accepted")
				}
			}
		})
	}
}
