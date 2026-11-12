package openclaw

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func recoveryFixture(t *testing.T) (*Checkout, func(...string) string, CatchUp) {
	t.Helper()
	c, g, bare := dirtyCatchUpFixture(t)
	write(t, c, "README.md", "saved staged\n")
	g("add", "README.md")
	write(t, c, "README.md", "saved staged\nsaved unstaged\n")
	write(t, c, "notes.txt", "saved untracked\n")
	advanceOrigin(t, bare, "README.md", "upstream\n")
	r, err := c.CatchUp()
	if err == nil || !r.RecoveryRequired || r.RecoveryRef == "" {
		t.Fatalf("expected recoverable stop: %+v %v", r, err)
	}
	return c, g, r
}

func TestFinishRecoveryRetainsWorkAndBackupAndPublishesExactReceipt(t *testing.T) {
	c, g, r := recoveryFixture(t)
	// The agent reconciles upstream with intended edits and preserves partial staging.
	write(t, c, "README.md", "upstream\nsaved staged\n")
	g("add", "README.md")
	write(t, c, "README.md", "upstream\nsaved staged\nsaved unstaged\n")
	head, index, status, stash := g("rev-parse", "HEAD"), g("write-tree"), g("status", "--porcelain"), g("rev-parse", "refs/stash")
	receipt, err := c.FinishCatchUpRecovery(r.RecoveryRef, head)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Kind != RecoveryCompletionKind || receipt.Head != head || receipt.IndexTree != index || receipt.RecoveryRef != r.RecoveryRef {
		t.Fatalf("receipt: %+v", receipt)
	}
	if g("write-tree") != index || g("status", "--porcelain") != status || g("rev-parse", "refs/stash") != stash || g("rev-parse", "HEAD") != head {
		t.Fatal("finishing changed user work or stash")
	}
	if g("show", r.RecoveryRef+"^2:README.md") != "saved staged" || g("show", r.RecoveryRef+"^3:notes.txt") != "saved untracked" {
		t.Fatal("backup changed")
	}
	marker := g("rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("active marker remains", err)
	}
	if _, err := os.Stat(marker + "-" + strings.TrimPrefix(r.RecoveryRef, "refs/wtc-catch-up/") + ".finished"); err != nil {
		t.Fatal("archive missing", err)
	}
	s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]
	if s.CatchUpRecovery != "" || !reflect.DeepEqual(s.CatchUpRecoveryCompleted, receipt) || s.Next == nil || s.Next.Step != "commit" {
		t.Fatalf("completion status: %+v", s)
	}
	again, err := c.FinishCatchUpRecovery(r.RecoveryRef, head)
	if err != nil || !reflect.DeepEqual(again, receipt) {
		t.Fatalf("retry should return original receipt: %+v %v", again, err)
	}
}

func TestFinishRecoveryRefusesStaleOrUnresolvedOperation(t *testing.T) {
	for _, mode := range []string{"wrong-ref", "wrong-head", "unmerged", "markers", "lock", "bad-marker", "missing-backup", "changed-pin", "merge"} {
		t.Run(mode, func(t *testing.T) {
			c, g, r := recoveryFixture(t)
			head := g("rev-parse", "HEAD")
			ref := r.RecoveryRef
			write(t, c, "README.md", "upstream and saved work\n")
			g("add", "README.md")
			switch mode {
			case "wrong-ref":
				ref = "refs/wtc-catch-up/" + strings.Repeat("a", 40) + "-1"
			case "wrong-head":
				head = strings.Repeat("a", 40)
			case "unmerged": // Create divergent histories so Git writes unmerged stages.
				g("reset", "--hard", "-q", "HEAD")
				g("checkout", "-q", "-b", "other")
				write(t, c, "README.md", "other\n")
				g("add", ".")
				g("commit", "-qm", "other")
				other := g("rev-parse", "HEAD")
				g("checkout", "-q", c.Branch())
				write(t, c, "README.md", "branch\n")
				g("add", ".")
				g("commit", "-qm", "branch")
				_, _ = git(c.Dir, "merge", other)
				head = g("rev-parse", "HEAD")
			case "markers":
				global := filepath.Join(t.TempDir(), "gitconfig")
				if err := os.WriteFile(global, []byte("[diff]\n\tnoprefix = true\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GIT_CONFIG_GLOBAL", global)
				write(t, c, "README.md", "<<<<<<< ours\nours\n=======\ntheirs\n>>>>>>> theirs\n")
				g("add", "README.md")
			case "lock":
				p := g("rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up.lock")
				_ = os.WriteFile(p, nil, 0o600)
			case "bad-marker":
				p := g("rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
				_ = os.WriteFile(p, []byte("invalid"), 0o600)
			case "missing-backup":
				g("update-ref", "-d", ref)
			case "changed-pin":
				g("update-ref", ref, head)
			case "merge":
				p := g("rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
				_ = os.WriteFile(p, []byte(head+"\n"), 0o600)
			}
			before := g("status", "--porcelain")
			index := g("ls-files", "--stage")
			if _, err := c.FinishCatchUpRecovery(ref, head); err == nil {
				t.Fatal("unsafe finish accepted")
			} else if mode == "markers" && !strings.Contains(err.Error(), "README.md") {
				t.Fatalf("custom Git diff prefix hid conflict path: %v", err)
			}
			if g("status", "--porcelain") != before || g("ls-files", "--stage") != index {
				t.Fatal("refusal changed work")
			}
			if s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]; s.CatchUpRecoveryCompleted != nil {
				t.Fatal("refusal emitted completion")
			}
		})
	}
}

func TestRecoveryStatusFailsClosedAndIgnoresBadOptionalReceipt(t *testing.T) {
	c, g := managedWorktree(t, "https://example.invalid/project/docs.git")
	p := g("rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
	if err := os.WriteFile(p, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]
	if s.Error != "" || s.CatchUpRecoveryError == "" || s.Next == nil || s.Next.Step != "resolve" {
		t.Fatalf("invalid marker hidden: %+v", s)
	}
	if _, err := c.CatchUp(); err == nil {
		t.Fatal("invalid marker allowed catch-up")
	}
	_ = os.Remove(p)
	_ = os.WriteFile(filepath.Join(filepath.Dir(p), recoveryCompletionFile), []byte("broken json"), 0o600)
	s = CheckoutsStatus([]string{c.Dir}, false).Repos[0]
	if s.Error != "" || s.CatchUpRecoveryCompleted != nil {
		t.Fatalf("optional receipt broke facts: %+v", s)
	}
}

func TestFailedRecoveryArchiveCannotReportCompletion(t *testing.T) {
	c, g, r := recoveryFixture(t)
	write(t, c, "README.md", "upstream and intended edits\n")
	g("add", "README.md")
	marker := g("rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
	archive := marker + "-" + strings.TrimPrefix(r.RecoveryRef, "refs/wtc-catch-up/") + ".finished"
	if err := os.Mkdir(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FinishCatchUpRecovery(r.RecoveryRef, g("rev-parse", "HEAD")); err == nil {
		t.Fatal("archive failure hidden")
	}
	s := CheckoutsStatus([]string{c.Dir}, false).Repos[0]
	if s.CatchUpRecovery != r.RecoveryRef || s.CatchUpRecoveryCompleted != nil || s.Next == nil || s.Next.Step != "resolve" {
		t.Fatalf("failure falsely completed recovery: %+v", s)
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FinishCatchUpRecovery(r.RecoveryRef, g("rev-parse", "HEAD")); err != nil {
		t.Fatal("safe retry failed", err)
	}
}

func TestActiveRecoveryBlocksPublishAndWorkerMerge(t *testing.T) {
	for _, marker := range []string{"refs/wtc-catch-up/" + strings.Repeat("a", 40) + "-1", "malformed"} {
		t.Run(marker[len(marker)-1:], func(t *testing.T) {
			c, g, _ := dirtyCatchUpFixture(t)
			write(t, c, "notes.txt", "intended work\n")
			g("add", "notes.txt")
			g("commit", "-qm", "work")
			g("branch", "other")
			p := g("rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up-recovery")
			if err := os.WriteFile(p, []byte(marker+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			head := g("rev-parse", "HEAD")
			if _, err := c.PlanPublish(PublishOptions{}); err == nil || !strings.Contains(err.Error(), "recovery") {
				t.Fatalf("publication not blocked: %v", err)
			}
			if _, err := c.MergeFrom("other"); err == nil || !strings.Contains(err.Error(), "recovery") {
				t.Fatalf("merge not blocked: %v", err)
			}
			if g("rev-parse", "HEAD") != head {
				t.Fatal("readiness moved HEAD")
			}
			if data, err := os.ReadFile(p); err != nil || string(data) != marker+"\n" {
				t.Fatal("readiness removed recovery marker")
			}
		})
	}
}
