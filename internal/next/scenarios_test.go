package next_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"taba.sh/taba/internal/next"
	"taba.sh/taba/internal/next/nexttest"
)

func TestGeneratedInputsAreConsistent(t *testing.T) {
	for _, c := range nexttest.Cases() {
		t.Run(c.ID, func(t *testing.T) {
			f := c.Facts
			if f.Branch == "" && (f.ToPush != 0 || f.PR != nil) {
				t.Fatal("detached fixture has branch publication state")
			}
			if f.Host == next.HostLocal && f.Branch != "" && f.ToPush != f.Ahead {
				t.Fatal("fixture does not match local publication count semantics")
			}
			if pr := f.PR; pr != nil {
				if pr.Mergeable == "conflicting" && (f.Behind == 0 || len(f.MergeConflicts) == 0) {
					t.Fatal("conflicting PR has no divergent target or predicted conflict")
				}
				if pr.State == "merged" && f.AfterMerge == 0 && f.ToPush != 0 {
					t.Fatal("completed branch has unpublished work without follow-up commits")
				}
			}
		})
	}
}

// These are safety and identity properties, not a second implementation of
// recommendation precedence. Review-missing states intentionally have no
// expected action here: the design adds choices the current planner lacks.
func TestGeneratedStatusInvariants(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range nexttest.Cases() {
		if seen[c.ID] {
			t.Fatalf("duplicate fixture identity %q", c.ID)
		}
		seen[c.ID] = true
		t.Run(c.ID, func(t *testing.T) {
			f := c.Facts
			before, _ := json.Marshal(f)
			got := next.For(f)
			if !reflect.DeepEqual(got, next.For(f)) {
				t.Fatal("same observations changed the recommendation")
			}
			after, _ := json.Marshal(f)
			if string(before) != string(after) {
				t.Fatal("deriving a recommendation mutated its input")
			}
			if got == nil {
				return
			}
			if got.Key == "" || got.Repo != f.Repo {
				t.Fatalf("recommendation lost its identity: %+v", got)
			}
			if f.Dirty > 0 && f.InProgress == "" && f.Conflicted == 0 {
				t.Fatalf("author work was bypassed: %+v", got)
			}
			if f.PR != nil && f.PR.State == "merged" && (got.Step == "push" || got.Step == "merge-pr") {
				t.Fatalf("completed branch received publication: %+v", got)
			}
			if got.Step == "merge-pr" && (f.PR == nil || f.PR.State != "open" || f.PR.Draft ||
				f.PR.Checks != "success" || f.PR.Review != "approved" || f.ToPush > 0 || f.Behind > 0) {
				t.Fatalf("merge offered before prerequisites: %+v", f)
			}
			if got.Step == "push" && f.Host == next.HostOpenClaw &&
				(got.By != next.ByWTC || got.Approve != next.ApprovePerson || got.Gateway != "github_publish") {
				t.Fatalf("gateway publication lost its authority boundary: %+v", got)
			}
			if len(got.Command) > 0 {
				if f.Host == next.HostLocal && !reflect.DeepEqual(got.Command, []string{"wtc", "catch-up", f.Collection, "--repos", f.Repo}) {
					t.Fatalf("local command escaped its selected target: %+v", got)
				}
			}
			other := f
			other.Collection = "another-sample"
			if second := next.For(other); second == nil || second.Key == got.Key {
				t.Fatal("another collection reused the action identity")
			}
		})
	}
}
