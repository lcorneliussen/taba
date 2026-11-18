package wtc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Frozen pre-optimization collector oracle; actual Git fixtures compare complete
// local rows, including development-base and operation authority.
func statusResourceLocalReference(ctx context.Context, t catchUpTarget) (StatusRepo, error) {
	dir := filepath.Base(t.path)
	row := StatusRepo{Collection: t.collection, Dir: dir, Repo: t.repo,
		Worktree: t.path, Tree: "clean", baseRef: t.ref, ComparisonRef: t.ref, DevelopmentBase: t.developmentBase, RenamedTo: t.renamed}
	remote, err := statusGitContext(ctx, t.path, "remote", "get-url", "origin")
	if err == nil {
		row.Slug, row.Forge = catchUpForge(remote)
	}
	branch, err := statusGitContext(ctx, t.path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		row.BranchKind = "branch"
		row.Branch = branch
		row.BranchDisplay = branch
	} else {
		row.BranchKind = "detached"
		row.Branch = catchUpBranchName(t.ref)
		row.BranchDisplay = "⌂ " + row.Branch
	}
	if reviewOnlyWorktree(t.path) {
		row.ReviewOnly = true
		row.BranchDisplay += " (review-only)"
	}
	upstream := t.ref
	row.AheadBasis, row.UpstreamState = "development-tip", "none"
	if row.BranchKind == "branch" {
		if found, err := statusGitContext(ctx, t.path, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
			upstream = found
			row.AheadBasis, row.UpstreamState = "upstream", "tracking"
		} else if tracking, err := statusGitContext(ctx, t.path, "for-each-ref", "--format=%(upstream:track)", "refs/heads/"+row.Branch); err == nil && tracking == "[gone]" {
			row.UpstreamState = "gone"
			row.BranchDisplay += " [gone]"
		}
	}
	row.AheadRef = upstream
	row.Ahead, err = statusCountContext(ctx, t.path, upstream+"..HEAD")
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s ahead count: %w", t.path, err)
	}
	row.Behind, err = statusCountContext(ctx, t.path, "HEAD.."+t.ref)
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s development-tip count: %w", t.path, err)
	}
	porcelain, err := statusGitContext(ctx, t.path, "status", "--porcelain")
	if err != nil {
		return StatusRepo{}, fmt.Errorf("%s working tree: %w", t.path, err)
	}
	if porcelain != "" {
		row.Changed = len(strings.Split(porcelain, "\n"))
		row.Tree = fmt.Sprintf("±%d", row.Changed)
	}
	row.Operation = catchUpOperation(t)
	if unmerged, err := statusGitContext(ctx, t.path, "diff", "--name-only", "--diff-filter=U"); err == nil && unmerged != "" {
		row.Conflict = true
	}
	return row, nil
}

func TestStatusResourceLocalActualGitComparison(t *testing.T) {
	c := newWorkspaceFixture(t)
	_, targets, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var target catchUpTarget
	for _, v := range targets {
		if v.path == c.Harness {
			target = v
		}
	}
	if target.path == "" {
		t.Fatal("no fixture target")
	}
	check := func(label string) {
		t.Helper()
		got, ge := statusLocalRepoContext(context.Background(), target)
		want, we := statusResourceLocalReference(context.Background(), target)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s got=%+v err=%v want=%+v err=%v", label, got, ge, want, we)
		}
	}
	check("detached")
	markers := []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"}
	args := []string{"-C", target.path, "rev-parse", "--path-format=absolute"}
	var expected []string
	for _, marker := range markers {
		args = append(args, "--git-path", marker)
		expected = append(expected, fixtureGit(t, "-C", target.path, "rev-parse", "--path-format=absolute", "--git-path", marker))
	}
	if got := fixtureGit(t, args...); got != strings.Join(expected, "\n") {
		t.Fatalf("batched actual Git paths differ: %q vs%q", got, expected)
	}
	fixtureGit(t, "-C", target.path, "switch", "-qc", "topic")
	check("branch-no-upstream")
	fixtureGit(t, "-C", target.path, "branch", "--set-upstream-to=origin/main")
	check("tracking")
	fixtureGit(t, "-C", target.path, "branch", "alternate", "HEAD")
	fixtureGit(t, "-C", target.path, "branch", "--set-upstream-to=alternate")
	check("distinct-upstream-and-development-base")
	fixtureGit(t, "-C", target.path, "branch", "--set-upstream-to=origin/main")
	fixtureFile(t, filepath.Join(target.path, "untracked"), "new", 0600)
	check("untracked")
	fixtureGit(t, "-C", target.path, "add", "untracked")
	check("staged")
	fixtureGit(t, "-C", target.path, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic local")
	check("ahead")
	fixtureFile(t, filepath.Join(target.path, "untracked"), "changed", 0600)
	check("dirty")
	fixtureGit(t, "-C", target.path, "update-ref", "-d", "refs/remotes/origin/main")
	check("missing-base")
	fixtureGit(t, "-C", target.path, "update-ref", "refs/remotes/origin/main", "HEAD")
	check("changed-base")
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		path := fixtureGit(t, "-C", target.path, "rev-parse", "--path-format=absolute", "--git-path", marker)
		fixtureFile(t, path, "fixture", 0600)
		check(marker)
		os.Remove(path)
	}
	fixtureGit(t, "-C", target.path, "reset", "--hard", "HEAD")
	fixtureGit(t, "-C", target.path, "switch", "--orphan", "unborn")
	check("unborn")
}

func TestStatusResourceGitMemoIdentityAndScope(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t)
	args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
	ctx := statusResourceOperation(context.Background())
	var calls int
	read := func() (string, error) { calls++; return gitOutput(args...) }
	first, err := statusResourceGit(ctx, args, read)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		v, e := statusResourceGit(ctx, args, read)
		if e != nil || v != first {
			t.Fatal(v, e)
		}
	}
	if calls != 1 {
		t.Fatalf("duplicate identity reads=%d", calls)
	}
	fixtureGit(t, "-C", c.Harness, "remote", "set-url", "origin", "https://github.com/example/other.git")
	v, e := statusResourceGit(ctx, args, read)
	if e != nil || v == first || calls != 2 {
		t.Fatalf("config change reused identity: %s %v %d", v, e, calls)
	}
	_, _ = statusResourceGit(statusResourceOperation(context.Background()), args, read)
	if calls != 3 {
		t.Fatal("memo escaped observation")
	}
	mutating := []string{"-C", c.Harness, "status", "--porcelain"}
	for i := 0; i < 2; i++ {
		_, _ = statusResourceGit(ctx, mutating, read)
	}
	if calls != 5 {
		t.Fatal("nonallowlisted read reused")
	}
}

func TestStatusResourceGitMemoConcurrentAndCancelledWaiter(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t)
	args := []string{"-C", c.Harness, "rev-parse", "--show-toplevel"}
	ctx := statusResourceOperation(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	read := func() (string, error) { calls.Add(1); close(entered); <-release; return "same", nil }
	done := make(chan error, 1)
	go func() { _, e := statusResourceGit(ctx, args, read); done <- e }()
	<-entered
	parent, cancel := context.WithCancel(ctx)
	defer cancel()
	waiting := &resourceChecksQueuedContext{Context: parent, waiting: make(chan struct{})}
	waiter := make(chan error, 1)
	go func() {
		_, e := statusResourceGit(waiting, args, func() (string, error) { t.Error("cancelled waiter dispatched"); return "", nil })
		waiter <- e
	}()
	select {
	case <-waiting.waiting:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("waiter did not queue")
	}
	cancel()
	select {
	case e := <-waiter:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("queued waiter did not cancel")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := statusResourceGit(ctx, args, read)
			if e != nil || v != "same" {
				t.Error(v, e)
			}
		}()
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestStatusResourceGitMemoNoCancelledPublication(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t)
	args := []string{"-C", c.Harness, "rev-parse", "--show-toplevel"}
	base := statusResourceOperation(context.Background())
	ctx, cancel := context.WithCancel(base)
	if _, e := statusResourceGit(ctx, args, func() (string, error) { cancel(); return "cancelled", nil }); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	v, e := statusResourceGit(base, args, func() (string, error) { return "current", nil })
	if e != nil || v != "current" {
		t.Fatal(v, e)
	}
}

func TestStatusResourceLocalBatchCancelled(t *testing.T) {
	c := newWorkspaceFixture(t)
	_, targets, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond)
		if _, e := statusLocalRepoContext(ctx, target); e == nil {
			t.Fatal("cancelled observation accepted")
		}
	}
}

func TestStatusResourceLocalUnsupportedGitFallback(t *testing.T) {
	c := newWorkspaceFixture(t)
	_, targets, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nif [ \"$3\" = status ] && [ \"$4\" = --porcelain=v2 ]; then printf 'unsupported-data\\n'; exit 0; fi\nif [ \"$3\" = rev-parse ] && [ \"$4\" = --path-format=absolute ] && [ \"$5\" = --git-path ]; then exit 2; fi\nif [ \"$3 $4 $5\" = 'rev-list --left-right --count' ]; then printf 'malformed'; exit 0; fi\nif [ \"$3 $4\" = 'rev-parse HEAD' ] && [ \"$#\" -eq 5 ]; then printf 'malformed'; exit 0; fi\nexec /usr/bin/git \"$@\"\n", 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, target := range targets {
		got, ge := statusLocalRepoContext(context.Background(), target)
		want, we := statusResourceLocalReference(context.Background(), target)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatal(got, ge, want, we)
		}
		if gotHead := statusFactsContext(context.Background(), got).Head; gotHead != fixtureGit(t, "-C", target.path, "rev-parse", "HEAD") {
			t.Fatal("malformed head batch bypassed fallback", gotHead)
		}
	}
}

func TestStatusResourceForegroundObservationSharesIdentityMemo(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t)
	ctx := context.Background()
	receiver, err := c.StatusReadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("STATUS_MEMO_CALLS", calls)
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nif [ \"$3 $4 $5\" = 'remote get-url origin' ]; then printf 'request\\n' >> \"$STATUS_MEMO_CALLS\"; fi\nexec /usr/bin/git \"$@\"\n", 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := receiver.StatusForegroundSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(calls)
	if n := strings.Count(string(raw), "request"); n > 3 {
		t.Fatalf("foreground discarded operation memo: %d remote subprocesses", n)
	}
}

func TestStatusResourceGitMemoUnknownConfigBypassesSafely(t *testing.T) {
	for _, mode := range []string{"unknown-system", "included-global", "symlink-global", "common-dir-override", "config-parameters"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
			c := newWorkspaceFixture(t)
			if mode == "unknown-system" {
				t.Setenv("GIT_CONFIG_NOSYSTEM", "")
				t.Setenv("GIT_CONFIG_SYSTEM", "")
			} else if mode == "common-dir-override" {
				t.Setenv("GIT_COMMON_DIR", t.TempDir())
			} else if mode == "config-parameters" {
				t.Setenv("GIT_CONFIG_PARAMETERS", "opaque-override")
			} else {
				path := filepath.Join(t.TempDir(), "gitconfig")
				if mode == "included-global" {
					fixtureFile(t, path, "[include]\n path = unsupported-include\n", 0600)
				} else {
					if err := os.Symlink("/dev/null", path); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("GIT_CONFIG_GLOBAL", path)
			}
			ctx := statusResourceOperation(context.Background())
			args := []string{"-C", c.Harness, "rev-parse", "--show-toplevel"}
			calls := 0
			read := func() (string, error) { calls++; return "current", nil }
			for i := 0; i < 2; i++ {
				if _, e := statusResourceGit(ctx, args, read); e != nil {
					t.Fatal(e)
				}
			}
			if calls != 2 {
				t.Fatal("unknown config must not authorize reuse")
			}
			canceled, cancel := context.WithCancel(ctx)
			if _, e := statusResourceGit(canceled, args, func() (string, error) { cancel(); return "cancelled", nil }); !errors.Is(e, context.Canceled) {
				t.Fatal("bypass dropped cancellation", e)
			}
		})
	}
}

func TestStatusResourceGitMemoIgnoresUnrelatedDirectoryWrites(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t)
	ctx := statusResourceOperation(context.Background())
	args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
	calls := 0
	read := func() (string, error) { calls++; return gitOutput(args...) }
	first, err := statusResourceGit(ctx, args, read)
	if err != nil {
		t.Fatal(err)
	}
	gitdir := fixtureGit(t, "-C", c.Harness, "rev-parse", "--absolute-git-dir")
	fixtureFile(t, filepath.Join(gitdir, "status-resource-unrelated"), "unrelated", 0600)
	got, err := statusResourceGit(ctx, args, read)
	if err != nil || got != first || calls != 1 {
		t.Fatalf("unrelated directory write invalidated identity: calls%d got%s err%v", calls, got, err)
	}
}

func TestStatusResourceGitMemoRevalidatesCanonicalParent(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	if err := os.MkdirAll(second, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "init", "-q", filepath.Join(first, "repo"))
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	args := []string{"-C", filepath.Join(alias, "repo"), "rev-parse", "--show-toplevel"}
	ctx := statusResourceOperation(context.Background())
	read := func() (string, error) { return gitOutput(args...) }
	before, err := statusResourceGit(ctx, args, read)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(first, "repo"), filepath.Join(second, "repo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	got, err := statusResourceGit(ctx, args, read)
	want := fixtureGit(t, args...)
	if err != nil || got != want || got == before {
		t.Fatalf("changed canonical association reused: before%s got%s want%s err%v", before, got, want, err)
	}
}

func TestStatusResourceLocalNamedDetachedBranch(t *testing.T) {
	c := newWorkspaceFixture(t)
	_, targets, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.path != c.Harness {
			continue
		}
		fixtureGit(t, "-C", target.path, "switch", "-qc", "(detached)")
		got, ge := statusLocalRepoContext(context.Background(), target)
		want, we := statusResourceLocalReference(context.Background(), target)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("actual named branch misread: got%+v err%v want%+v err%v", got, ge, want, we)
		}
	}
}

func TestStatusResourceIdentitySurvivesValidatedObservations(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
	calls := 0
	read := func() (string, error) { calls++; return gitOutput(args...) }
	for i := 0; i < 2; i++ {
		fresh, err := c.StatusReadContext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := statusResourceGit(fresh.resourceContext(), args, read); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("unchanged validated identity redispatched across observations: %d", calls)
	}
	fixtureGit(t, "-C", c.Harness, "remote", "set-url", "origin", "https://github.com/example/replaced.git")
	fresh, err := c.StatusReadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := statusResourceGit(fresh.resourceContext(), args, read)
	if err != nil || calls != 2 || got != "https://github.com/example/replaced.git" {
		t.Fatalf("stale identity %s %v %d", got, err, calls)
	}
}
