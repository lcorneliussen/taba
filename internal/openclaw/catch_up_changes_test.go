package openclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dirtyCatchUpFixture(t *testing.T) (*Checkout, func(...string) string, string) {
	t.Helper()
	c, g := managedWorktree(t, "https://example.invalid/project/docs.git")
	bare := filepath.Join(filepath.Dir(c.Dir), "origin.git")
	g("remote", "set-url", "origin", bare)
	c.Origin = ParseOrigin(bare)
	return c, g, bare
}

func TestGatewayCatchUpRestoresStagedUnstagedAndUntracked(t *testing.T) {
	c, g, bare := dirtyCatchUpFixture(t)
	write(t, c, "README.md", "staged\n")
	g("add", "README.md")
	write(t, c, "README.md", "staged\nunstaged\n")
	write(t, c, "new.txt", "untracked\n")
	before := g("status", "--porcelain=v1")
	advanceOrigin(t, bare, "upstream.txt", "upstream\n")
	// The recovery snapshot needs no configured personal author.
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	r, err := c.CatchUp()
	if err != nil || !r.Merged || !r.ChangesRestored || r.RecoveryRef == "" {
		t.Fatalf("catch-up: %+v %v", r, err)
	}
	if got := g("status", "--porcelain=v1"); got != before {
		t.Fatalf("staging changed: %q != %q", got, before)
	}
	if g("show", ":README.md") != "staged" || g("diff", "--", "README.md") == "" {
		t.Fatal("partial staging was not preserved")
	}
	if b, _ := os.ReadFile(filepath.Join(c.Dir, "new.txt")); string(b) != "untracked\n" {
		t.Fatal("untracked file changed")
	}
	if g("show", r.RecoveryRef+"^2:README.md") != "staged" || g("show", r.RecoveryRef+"^3:new.txt") != "untracked" {
		t.Fatal("recovery copy incomplete")
	}
	if g("rev-list", "--count", "HEAD..origin/main") != "0" || g("rev-list", "--count", "origin/main..HEAD") != "0" {
		t.Fatal("catch-up created a user commit or did not reach the base")
	}
}

func TestGatewayCatchUpRetainsRecoveryOnRestoreConflict(t *testing.T) {
	c, g, bare := dirtyCatchUpFixture(t)
	write(t, c, "README.md", "staged local\n")
	g("add", "README.md")
	write(t, c, "README.md", "unstaged local\n")
	advanceOrigin(t, bare, "README.md", "upstream\n")
	r, err := c.CatchUp()
	if err == nil || !strings.Contains(err.Error(), "local changes need recovery") || !r.Merged || r.ChangesRestored {
		t.Fatalf("restore conflict hidden: %+v %v", r, err)
	}
	if g("show", r.RecoveryRef+":README.md") != "unstaged local" || g("show", r.RecoveryRef+"^2:README.md") != "staged local" {
		t.Fatal("local content or staging lost")
	}
	status := CheckoutsStatus([]string{c.Dir}, false).Repos[0]
	if status.CatchUpRecovery != r.RecoveryRef || status.Next == nil || status.Next.Step != "resolve" {
		t.Fatalf("recovery does not offer agent resolution: %+v", status)
	}
	head, stash := g("rev-parse", "HEAD"), g("rev-parse", "refs/stash")
	if _, err := c.CatchUp(); err == nil {
		t.Fatal("retry reapplied unresolved local work")
	}
	if g("rev-parse", "HEAD") != head || g("rev-parse", "refs/stash") != stash {
		t.Fatal("retry mutated unresolved work")
	}
}

func TestRecoveryStashIgnoresAnotherWorktreesNewerEntry(t *testing.T) {
	entries := "newer\x00On openclaw/other: taba catch-up recovery other\nours\x00On openclaw/example: taba catch-up recovery own\nolder\x00On openclaw/other: previous"
	if got := recoveryStash(entries, "taba catch-up recovery own"); got != "ours" {
		t.Fatalf("selected another worktree's copy: %s", got)
	}
}

func TestGatewayCatchUpRestoresAfterAbortedMerge(t *testing.T) {
	c, g, bare := dirtyCatchUpFixture(t)
	write(t, c, "README.md", "branch\n")
	g("add", "README.md")
	g("commit", "-qm", "branch change")
	write(t, c, "local.txt", "local\n")
	g("add", "local.txt")
	advanceOrigin(t, bare, "README.md", "upstream\n")
	head, status := g("rev-parse", "HEAD"), g("status", "--porcelain")
	r, err := c.CatchUp()
	if err == nil || !strings.Contains(err.Error(), "The merge was aborted") || !r.ChangesRestored {
		t.Fatalf("merge conflict: %+v %v", r, err)
	}
	if g("rev-parse", "HEAD") != head || g("status", "--porcelain") != status || g("show", ":local.txt") != "local" {
		t.Fatal("aborted merge changed local work")
	}
}

func TestGatewayCatchUpKeepsUntrackedCollisionAndPriorStash(t *testing.T) {
	c, g, bare := dirtyCatchUpFixture(t)
	write(t, c, "previous.txt", "previous\n")
	g("stash", "push", "-u", "-m", "previous")
	previous := g("rev-parse", "refs/stash")
	write(t, c, "incoming.txt", "local untracked\n")
	advanceOrigin(t, bare, "incoming.txt", "upstream\n")
	r, err := c.CatchUp()
	if err == nil || r.RecoveryRef == "" || r.ChangesRestored {
		t.Fatalf("collision hidden: %+v %v", r, err)
	}
	if g("show", r.RecoveryRef+"^3:incoming.txt") != "local untracked" || g("rev-parse", "stash@{1}") != previous {
		t.Fatal("recovery copy or previous shared stash lost")
	}
	if b, _ := os.ReadFile(filepath.Join(c.Dir, "incoming.txt")); string(b) != "upstream\n" {
		t.Fatal("upstream file was overwritten")
	}
}

func TestGatewayCatchUpDoesNotOverwriteIgnoredFiles(t *testing.T) {
	c, g, bare := dirtyCatchUpFixture(t)
	write(t, c, ".gitignore", "private.txt\n")
	g("add", ".gitignore")
	g("commit", "-qm", "ignore local configuration")
	write(t, c, "private.txt", "private local\n")
	write(t, c, "notes.txt", "local work\n")
	advanceOrigin(t, bare, "private.txt", "upstream\n")
	head := g("rev-parse", "HEAD")
	if _, err := c.CatchUp(); err == nil {
		t.Fatal("ignored file collision accepted")
	}
	if b, _ := os.ReadFile(filepath.Join(c.Dir, "private.txt")); string(b) != "private local\n" || g("rev-parse", "HEAD") != head {
		t.Fatal("ignored file or original HEAD changed")
	}
	if b, _ := os.ReadFile(filepath.Join(c.Dir, "notes.txt")); string(b) != "local work\n" {
		t.Fatal("local work not restored after merge rejection")
	}
}

func TestGatewayCatchUpRefusesConcurrentOperation(t *testing.T) {
	c, g, _ := dirtyCatchUpFixture(t)
	lock := g("rev-parse", "--path-format=absolute", "--git-path", "wtc-catch-up.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, c, "notes.txt", "local\n")
	if _, err := c.CatchUp(); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("lock ignored: %v", err)
	}
	if g("status", "--porcelain") != "?? notes.txt" {
		t.Fatal("busy checkout changed")
	}
}
