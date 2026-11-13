package openclaw

import (
	"fmt"
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

func TestRecoveryMarkersCompareCorrespondingSavedLayers(t *testing.T) {
	const plain = "content\n"
	const markers = "content\n<<<<<<< fixture\n=======\n>>>>>>> fixture\n"
	for _, tc := range []struct {
		name, head, savedIndex, savedWorktree, index, worktree string
		refuse                                                 bool
	}{
		{"saved-index-fixture", plain, markers, plain, markers, plain, false},
		{"saved-worktree-fixture", plain, plain, markers, plain, markers, false},
		{"new-index-markers-from-other-layer", plain, plain, markers, markers, plain, true},
		{"new-worktree-markers-from-other-layer", plain, markers, plain, plain, markers, true},
		{"new-index-markers", plain, plain, plain, markers, plain, true},
		{"new-worktree-markers", plain, plain, plain, plain, markers, true},
		{"head-fixture", markers, plain, plain, markers, markers, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, g := managedWorktree(t, "https://example.invalid/project/docs.git")
			write(t, c, "README.md", tc.head)
			write(t, c, "anchor.txt", "baseline\n")
			g("add", "README.md", "anchor.txt")
			g("commit", "-qm", "baseline")
			write(t, c, "README.md", tc.savedIndex)
			g("add", "README.md")
			write(t, c, "README.md", tc.savedWorktree)
			write(t, c, "anchor.txt", "saved snapshot\n")
			saved := g("stash", "create")
			if saved == "" {
				t.Fatal("missing saved index/worktree snapshot")
			}
			g("update-ref", "refs/test/recovery", saved)
			write(t, c, "README.md", tc.index)
			g("add", "README.md")
			write(t, c, "README.md", tc.worktree)
			head, index, status := g("rev-parse", "HEAD"), g("write-tree"), g("status", "--porcelain")
			err := c.checkRecoveryMarkers("refs/test/recovery")
			if (err != nil) != tc.refuse {
				t.Fatalf("refuse=%v, got %v", tc.refuse, err)
			}
			if err != nil && !strings.Contains(err.Error(), "README.md") {
				t.Fatalf("missing affected file: %v", err)
			}
			content, readErr := os.ReadFile(filepath.Join(c.Dir, "README.md"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(content) != tc.worktree || g("write-tree") != index || g("status", "--porcelain") != status || g("rev-parse", "HEAD") != head || g("rev-parse", "refs/test/recovery") != saved {
				t.Fatal("marker verification mutated the saved or current work")
			}
		})
	}
}

func TestFinishRecoveryRetainsMarkerFixturesInPartialStaging(t *testing.T) {
	c, g, bare := dirtyCatchUpFixture(t)
	const staged = "saved staged\n<<<<<<< fixture\n=======\n>>>>>>> fixture\n"
	const unstaged = "saved unstaged\n"
	write(t, c, "README.md", staged)
	g("add", "README.md")
	write(t, c, "README.md", unstaged)
	write(t, c, "notes.txt", "saved untracked\n")
	advanceOrigin(t, bare, "README.md", "upstream\n")
	r, err := c.CatchUp()
	if err == nil || !r.RecoveryRequired || r.RecoveryRef == "" {
		t.Fatalf("expected recovery stop: %+v %v", r, err)
	}
	if g("show", r.RecoveryRef+"^2:README.md") != strings.TrimSpace(staged) || g("show", r.RecoveryRef+":README.md") != strings.TrimSpace(unstaged) {
		t.Fatal("saved layers differ from intended partial staging")
	}
	write(t, c, "README.md", "upstream\n"+staged)
	g("add", "README.md")
	write(t, c, "README.md", "upstream\n"+unstaged)
	head, index, status, backup := g("rev-parse", "HEAD"), g("write-tree"), g("status", "--porcelain"), g("rev-parse", r.RecoveryRef)
	receipt, err := c.FinishCatchUpRecovery(r.RecoveryRef, head)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(c.Dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "upstream\n"+unstaged || receipt.IndexTree != index || g("write-tree") != index || g("status", "--porcelain") != status || g("rev-parse", "HEAD") != head || g("rev-parse", r.RecoveryRef) != backup {
		t.Fatal("finishing changed partial staging, working edits, HEAD or backup")
	}
	if active, exists, err := c.recoveryMarker(); err != nil || exists || active != "" {
		t.Fatalf("verified marker not archived: %q %v %v", active, exists, err)
	}
}

func TestRecoveryMarkersKeepGitPathIdentityAcrossBaselines(t *testing.T) {
	for _, name := range []string{"z-normal.txt", "z space.txt", "z\ttab.txt", "z\nline.txt", "zé.txt", "z\"quote.txt", "z\\slash.txt"} {
		for _, quote := range []string{"true", "false"} {
			for _, cached := range []bool{false, true} {
				t.Run(fmt.Sprintf("%q/quote=%s/cached=%v", name, quote, cached), func(t *testing.T) {
					c, g := managedWorktree(t, "https://example.invalid/project/docs.git")
					config := filepath.Join(t.TempDir(), "gitconfig")
					if err := os.WriteFile(config, []byte("[core]\n\tquotePath = "+quote+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("GIT_CONFIG_GLOBAL", config)
					write(t, c, "aa-saved.txt", "baseline\n")
					write(t, c, name, "baseline\n")
					g("add", "--", "aa-saved.txt", name)
					g("commit", "-qm", "baseline paths")
					write(t, c, "aa-saved.txt", "saved-only edit\n")
					g("add", "aa-saved.txt")
					saved := g("stash", "create")
					if saved == "" {
						t.Fatal("missing snapshot")
					}
					g("update-ref", "refs/test/parser", saved)
					write(t, c, "aa-saved.txt", "baseline\n")
					g("add", "aa-saved.txt")
					write(t, c, name, "<<<<<<< unresolved\nnew\n>>>>>>> unresolved\n")
					if cached {
						g("add", "--", name)
					}
					head, index, status := g("rev-parse", "HEAD"), g("write-tree"), g("status", "--porcelain")
					err := c.checkRecoveryMarkers("refs/test/parser")
					if err == nil || !strings.Contains(err.Error(), name) {
						t.Fatalf("new markers hidden or wrong file identity: %v", err)
					}
					if g("rev-parse", "HEAD") != head || g("write-tree") != index || g("status", "--porcelain") != status || g("rev-parse", "refs/test/parser") != saved {
						t.Fatal("verification changed work or snapshot")
					}
				})
			}
		}
	}
}

func TestRecoveryMarkersDoNotReadAddedTextAsFileHeaders(t *testing.T) {
	c, g := managedWorktree(t, "https://example.invalid/project/docs.git")
	write(t, c, "aa-saved.txt", "baseline\n")
	write(t, c, "z.txt", "baseline\n")
	g("add", ".")
	g("commit", "-qm", "baseline paths")
	write(t, c, "aa-saved.txt", "saved-only edit\n")
	g("add", "aa-saved.txt")
	saved := g("stash", "create")
	g("update-ref", "refs/test/parser", saved)
	write(t, c, "aa-saved.txt", "baseline\n")
	g("add", "aa-saved.txt")
	write(t, c, "z.txt", "++ b/not-the-path\n<<<<<<< unresolved\n>>>>>>> unresolved\n")
	g("add", "z.txt")
	err := c.checkRecoveryMarkers("refs/test/parser")
	if err == nil || !strings.Contains(err.Error(), "z.txt") || strings.Contains(err.Error(), "not-the-path") {
		t.Fatalf("hunk text replaced the actual file: %v", err)
	}
}

func TestSandboxContinueRefusesQuotedMarkersBeforeCommitting(t *testing.T) {
	for _, name := range []string{"z\ttab.txt", "zé.txt"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			c, gw := managedWorktree(t, "https://github.com/example-org/docs.git")
			write(t, c, "aa-saved.txt", "baseline\n")
			write(t, c, name, "baseline\n")
			gw("add", "--", "aa-saved.txt", name)
			gw("commit", "-qm", "baseline paths")
			gateway, sb, g := sandboxCopy(t, c)
			writeAt(t, sb, "README.md", "sandbox\n")
			g("commit", "-qam", "sandbox work")
			upstream(t, c, "README.md", "upstream\n")
			upstream(t, c, "aa-saved.txt", "other side\n")
			out, err := SandboxCatchUpCheckout(sb, gateway, false)
			if err != nil || len(out.Conflicts) == 0 {
				t.Fatalf("expected merge conflict: %+v %v", out, err)
			}
			writeAt(t, sb, "README.md", "resolved\n")
			writeAt(t, sb, "aa-saved.txt", "baseline\n")
			writeAt(t, sb, name, "<<<<<<< unresolved\n>>>>>>> unresolved\n")
			g("add", "--", "README.md", "aa-saved.txt", name)
			head, index, merge, gatewayHead := g("rev-parse", "HEAD"), g("write-tree"), g("rev-parse", "MERGE_HEAD"), gw("rev-parse", "HEAD")
			if _, err = SandboxCatchUpCheckout(sb, gateway, true); err == nil || !strings.Contains(err.Error(), "conflict markers") {
				t.Fatalf("unresolved marker continuation: %v", err)
			}
			if g("rev-parse", "HEAD") != head || g("write-tree") != index || g("rev-parse", "MERGE_HEAD") != merge || gw("rev-parse", "HEAD") != gatewayHead {
				t.Fatal("unresolved markers were committed or delivered")
			}
		})
	}
}

func TestAddedMarkerDiffParserBoundaries(t *testing.T) {
	const marker = "<<<<<<< pending"
	ordinary := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n"
	for _, tc := range []struct {
		name, diff, path string
		refuse           bool
	}{
		{"ordinary", ordinary + "+" + marker + "\n", "a.txt", false},
		{"header-like-content", ordinary + "+++ b/not-a-header\n+" + marker + "\n", "a.txt", false},
		{"quoted-octal", "diff --git x y\n--- x\n+++ \"b/z\\303\\251.txt\"\n@@ -0,0 +1 @@\n+" + marker + "\n", "zé.txt", false},
		{"spaces-with-tab-separator", "diff --git x y\n--- x\n+++ b/z space \t\n@@ -0,0 +1 @@\n+" + marker + "\n", "z space ", false},
		{"deleted", "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-" + marker + "\n", "", false},
		{"binary-after-text", ordinary + "+" + marker + "\ndiff --git a/z.bin b/z.bin\nBinary files a/z.bin and b/z.bin differ\n", "a.txt", false},
		{"mode-only", "diff --git a/a b/a\nold mode 100644\nnew mode 100755\n", "", false},
		{"missing-header-after-patch", ordinary + "+" + marker + "\ndiff --git a/z b/z\n@@ -0,0 +1 @@\n+" + marker + "\n", "", true},
		{"bad-quote", "diff --git x y\n+++ \"b/unterminated\n@@ -0,0 +1 @@\n+" + marker + "\n", "", true},
		{"bad-prefix", "diff --git x y\n+++ z/unknown\n@@ -0,0 +1 @@\n+" + marker + "\n", "", true},
		{"nul-path", "diff --git x y\n+++ \"b/\\000name\"\n@@ -0,0 +1 @@\n+" + marker + "\n", "", true},
		{"missing-path", "diff --git x y\n+++ b/\n@@ -0,0 +1 @@\n+" + marker + "\n", "", true},
		{"bad-hunk", "diff --git x y\n+++ b/file\n@@ malformed\n+" + marker + "\n", "", true},
		{"unsupported-combined", "diff --cc z.txt\n+++ b/z.txt\n@@@ -1 -1 +1 @@@\n++" + marker + "\n", "", true},
		{"no-identity", "+" + marker + "\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAddedConflictMarkers(tc.diff)
			if (err != nil) != tc.refuse {
				t.Fatalf("refuse=%v got %v", tc.refuse, err)
			}
			if err != nil {
				return
			}
			want := map[string]string{}
			if tc.path != "" {
				want[tc.path+"\x00"+marker] = tc.path
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wrong patch identity: got %#v want %#v", got, want)
			}
		})
	}
}

func TestStagedMarkerVerificationErrorsFailClosed(t *testing.T) {
	c, _ := managedWorktree(t, "https://example.invalid/project/docs.git")
	if _, err := stagedConflictMarkers(c.Dir); err == nil {
		t.Fatal("missing merge parent treated as a verified clean index")
	}
}

func TestFinishRecoveryRefusesQuotedMarkersAndRetainsOperation(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%v", cached), func(t *testing.T) {
			c, g, bare := dirtyCatchUpFixture(t)
			const name = "z\tfixture.txt"
			write(t, c, "aa-saved.txt", "baseline\n")
			write(t, c, name, "baseline\n")
			g("add", ".")
			g("commit", "-qm", "baseline paths")
			write(t, c, "aa-saved.txt", "saved-only edit\n")
			write(t, c, "README.md", "saved staged\n")
			g("add", ".")
			write(t, c, "README.md", "saved unstaged\n")
			advanceOrigin(t, bare, "README.md", "upstream\n")
			r, err := c.CatchUp()
			if err == nil || !r.RecoveryRequired {
				t.Fatalf("expected recovery stop: %+v %v", r, err)
			}
			write(t, c, "README.md", "resolved\n")
			write(t, c, "aa-saved.txt", "baseline\n")
			g("add", ".")
			write(t, c, name, "<<<<<<< unresolved\n>>>>>>> unresolved\n")
			if cached {
				g("add", "--", name)
			}
			head, index, status, backup := g("rev-parse", "HEAD"), g("write-tree"), g("status", "--porcelain"), g("rev-parse", r.RecoveryRef)
			if _, err := c.FinishCatchUpRecovery(r.RecoveryRef, head); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("quoted marker accepted or hidden: %v", err)
			}
			if g("rev-parse", "HEAD") != head || g("write-tree") != index || g("status", "--porcelain") != status || g("rev-parse", r.RecoveryRef) != backup {
				t.Fatal("refusal changed work or backup")
			}
			if active, exists, err := c.recoveryMarker(); err != nil || !exists || active != r.RecoveryRef {
				t.Fatalf("refusal released recovery: %q %v %v", active, exists, err)
			}
			if receipt, err := c.recoveryCompletion(); err != nil || receipt != nil {
				t.Fatalf("refusal published completion: %+v %v", receipt, err)
			}
		})
	}
}
