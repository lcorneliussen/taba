package openclaw

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRecoverySerializesDeliveryAndWorkerMerges(t *testing.T) {
	for _, operation := range []string{"delivery", "worker-merge"} {
		for _, barrier := range []string{"active-marker", "malformed-marker", "held-lock"} {
			t.Run(operation+"/"+barrier, func(t *testing.T) {
				c, g, recovery := recoveryFixture(t)
				// Reconciliation can be conflict-free without being acknowledged yet.
				write(t, c, "README.md", "upstream\nintended staged\n")
				g("add", "README.md")
				write(t, c, "README.md", "upstream\nintended staged\nintended unstaged\n")
				write(t, c, ".gitignore", "ignored.bin\n")
				write(t, c, "ignored.bin", "ignored data\n")
				head := g("rev-parse", "HEAD")
				candidate := g("commit-tree", head+"^{tree}", "-p", head, "-m", "sandbox work")
				g("branch", "worker", candidate)
				incoming := "refs/wtc/sandbox-incoming/" + c.Branch()
				g("update-ref", incoming, head)
				marker := g("rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
				wantError := "still need recovery"
				switch barrier {
				case "malformed-marker":
					if err := os.WriteFile(marker, []byte("invalid\n"), 0600); err != nil {
						t.Fatal(err)
					}
					wantError = "marker is invalid"
				case "held-lock":
					if err := os.Remove(marker); err != nil {
						t.Fatal(err)
					}
					unlock, err := c.catchUpLock()
					if err != nil {
						t.Fatal(err)
					}
					defer unlock()
					wantError = "already running"
				}
				snapshot := func() []string {
					result := []string{g("rev-parse", "HEAD"), g("write-tree"), g("diff"), g("status", "--porcelain"), g("show-ref"), g("reflog", "show", "--format=%H %gs", "refs/stash")}
					for _, p := range []string{marker, filepath.Join(c.Dir, "README.md"), filepath.Join(c.Dir, "notes.txt"), filepath.Join(c.Dir, "ignored.bin")} {
						data, err := os.ReadFile(p)
						if err != nil && !os.IsNotExist(err) {
							t.Fatal(err)
						}
						result = append(result, string(data))
					}
					return result
				}
				before := snapshot()
				var err error
				if operation == "delivery" {
					_, err = AcceptSandboxBranch(c.Dir, candidate, "")
				} else {
					_, err = c.MergeFrom("worker")
				}
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("barrier bypassed: %v", err)
				}
				if !reflect.DeepEqual(before, snapshot()) {
					t.Fatal("refused operation changed HEAD, index, files, marker or backup refs")
				}
				if operation == "delivery" {
					if _, err := AcceptSandboxBranch(c.Dir, candidate, "missing-bundle-must-not-be-read"); err == nil || !strings.Contains(err.Error(), wantError) {
						t.Fatalf("bundle inspected before recovery barrier: %v", err)
					}
				}
				if g("rev-parse", recovery.RecoveryRef) == "" {
					t.Fatal("recovery pin lost")
				}
				// A refusal must not remove another operation's lock.
				if barrier == "held-lock" {
					if unlock, err := c.catchUpLock(); err == nil {
						unlock()
						t.Fatal("held lock was removed")
					}
				}
			})
		}
	}
}

func TestWorkerMergeCannotMoveCleanCheckoutWhileCatchUpOwnsLock(t *testing.T) {
	c, g := managedWorktree(t, "https://github.com/example-org/docs.git")
	head, index := g("rev-parse", "HEAD"), g("write-tree")
	candidate := g("commit-tree", index, "-p", head, "-m", "worker change")
	g("branch", "worker", candidate)
	unlock, err := c.catchUpLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	if _, err := c.MergeFrom("worker"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent worker merge: %v", err)
	}
	if g("rev-parse", "HEAD") != head || g("write-tree") != index {
		t.Fatal("concurrent worker merge moved HEAD or index")
	}
	unlock()
	unlock = nil
	if _, err := c.MergeFrom("worker"); err != nil {
		t.Fatal("worker merge after lock release:", err)
	}
	if g("rev-parse", "HEAD") != candidate {
		t.Fatal("worker branch was not merged")
	}
}

func TestCheckoutStatusExposesUnfinishedOperationForCommitGuards(t *testing.T) {
	c, g := managedWorktree(t, "https://github.com/example-org/docs.git")
	marker := g("rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
	if err := os.WriteFile(marker, []byte(g("rev-parse", "HEAD")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]
	if s.InProgress != "a merge" {
		t.Fatalf("unfinished operation missing from status: %+v", s)
	}
	for _, choice := range s.Choices.Actions {
		if choice.Step.Step == "commit" && choice.Available {
			t.Fatal("commit offered before merge resolution")
		}
	}
}

func TestDeliveryAfterVerifiedRecoveryRetainsEditsAndIsIdempotent(t *testing.T) {
	c, g, recovery := recoveryFixture(t)
	write(t, c, "README.md", "upstream\nintended staged\n")
	g("add", "README.md")
	write(t, c, "README.md", "upstream\nintended staged\nintended unstaged\n")
	head := g("rev-parse", "HEAD")
	if _, err := c.FinishCatchUpRecovery(recovery.RecoveryRef, head); err != nil {
		t.Fatal(err)
	}
	// The sandbox commit contains only the staged part; pending files stay untouched.
	candidate := g("commit-tree", g("write-tree"), "-p", head, "-m", "intended change")
	before := g("diff")
	for i := 0; i < 2; i++ {
		result, err := AcceptSandboxBranch(c.Dir, candidate, "")
		if err != nil || result.Head != candidate {
			t.Fatalf("delivery %d: %+v %v", i, result, err)
		}
		if g("diff") != before || g("diff", "--cached") != "" || g("show", recovery.RecoveryRef+"^3:notes.txt") != "saved untracked" {
			t.Fatal("delivery lost pending edits or recovery backup")
		}
	}
}
