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
	shared := c.WithStatusContext(context.Background()).resourceContext()
	check := func(label string) {
		t.Helper()
		got, ge := statusLocalRepoContext(shared, target)
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
	originalRef := target.ref
	target.ref = "alternate"
	check("gone-upstream-valid-base")
	target.ref = originalRef
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

func TestStatusResourceIdentityBoundAndMutableBypass(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
	config := fixtureGit(t, "-C", c.Harness, "rev-parse", "--git-path", "config")
	if !filepath.IsAbs(config) {
		config = filepath.Join(c.Harness, config)
	}
	original, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 140; i++ {
		fixtureFile(t, config, string(original)+fmt.Sprintf("\n# change %d\n", i), 0600)
		if _, err := statusResourceGit(c.resourceContext(), args, func() (string, error) { return "identity", nil }); err != nil {
			t.Fatal(err)
		}
		if len(c.statusIdentityCache.entries) > 128 {
			t.Fatal("identity memo exceeded bound")
		}
	}
	calls := 0
	for _, command := range [][]string{{"rev-parse", "HEAD"}, {"status", "--porcelain"}, {"rev-list", "--count", "HEAD"}} {
		for i := 0; i < 2; i++ {
			_, err := statusResourceGit(c.resourceContext(), append([]string{"-C", c.Harness}, command...), func() (string, error) { calls++; return "mutable", nil })
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls != 6 {
		t.Fatal("mutable facts reused", calls)
	}
	if err := os.Chmod(config, 0666); err != nil {
		t.Fatal(err)
	}
	if c.statusIdentityCache.fingerprint(c.resourceContext(), args) != "" {
		t.Fatal("untrusted writable config reused")
	}
}

func TestStatusResourceIdentityProvenancePathsAndFallback(t *testing.T) {
	for _, mode := range []string{"discovered-missing", "unsupported", "include", "symlink", "unknown-env"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
			c := newWorkspaceFixture(t).WithStatusContext(context.Background())
			bin := t.TempDir()
			system := filepath.Join(bin, "system-config")
			global := filepath.Join(bin, "global-config")
			t.Setenv("GIT_CONFIG_NOSYSTEM", "")
			t.Setenv("GIT_CONFIG_SYSTEM", "")
			t.Setenv("GIT_CONFIG_GLOBAL", "")
			t.Setenv("PROVENANCE_SYSTEM", system)
			t.Setenv("PROVENANCE_GLOBAL", global)
			body := `#!/bin/sh
case "$1 $2" in
 'var GIT_CONFIG_SYSTEM') printf '%s\n' "$PROVENANCE_SYSTEM" ;;
 'var GIT_CONFIG_GLOBAL') printf '%s\n' "$PROVENANCE_GLOBAL" ;;
 *) exit 99 ;;
esac
`
			if mode == "unsupported" {
				body = "#!/bin/sh\nexit 129\n"
			}
			fixtureFile(t, filepath.Join(bin, "git"), body, 0700)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if mode == "include" {
				fixtureFile(t, global, "[includeIf \"onbranch:topic\"]\n path = unknown\n", 0600)
			}
			if mode == "symlink" {
				if err := os.Symlink("/dev/null", global); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unknown-env" {
				t.Setenv("GIT_NAMESPACE", "opaque")
			}
			args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
			calls := 0
			read := func() (string, error) { calls++; return "identity", nil }
			for i := 0; i < 2; i++ {
				if _, err := statusResourceGit(c.resourceContext(), args, read); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "discovered-missing" {
				if calls != 1 {
					t.Fatal("known missing config paths did not permit reuse", calls)
				}
				fixtureFile(t, system, "[url \"https://example.invalid/\"]\n insteadOf = local:\n", 0600)
				if _, err := statusResourceGit(c.resourceContext(), args, read); err != nil {
					t.Fatal(err)
				}
				if calls != 2 {
					t.Fatal("newly created system config failed to invalidate")
				}
			} else if calls != 2 {
				t.Fatal("unknown config/env authorized reuse", mode, calls)
			}
			ctx, cancel := context.WithCancel(c.resourceContext())
			cancel()
			if _, err := statusResourceGit(ctx, args, func() (string, error) { t.Error("postcancel dispatch"); return "", nil }); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestStatusResourceIdentityProvenanceCancellation(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "")
	t.Setenv("GIT_CONFIG_SYSTEM", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nexec /bin/sleep 2\n", 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
	ctx, cancel := context.WithTimeout(c.resourceContext(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := statusResourceGit(ctx, args, func() (string, error) { t.Error("canceled discovery dispatched identity"); return "", nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("config discovery child exceeded caller bound")
	}
	if len(c.statusIdentityCache.entries) != 0 {
		t.Fatal("canceled discovery published identity")
	}
	// The provenance gate is cancellable independently of the in-flight reader.
	<-c.statusIdentityCache.provenanceGate
	defer func() { c.statusIdentityCache.provenanceGate <- struct{}{} }()
	queued, stop := context.WithTimeout(c.resourceContext(), 40*time.Millisecond)
	defer stop()
	if _, err := statusResourceGit(queued, args, func() (string, error) { t.Error("queued canceled reader dispatched"); return "", nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestStatusResourceIdentityLiveWaiterAfterLeaderCancel(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	args := []string{"-C", c.Harness, "rev-parse", "--show-toplevel"}
	leader, cancel := context.WithCancel(c.resourceContext())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	leadDone := make(chan error, 1)
	go func() {
		_, err := statusResourceGit(leader, args, func() (string, error) { close(entered); <-release; return "discarded", leader.Err() })
		leadDone <- err
	}()
	<-entered
	waiter := &resourceChecksQueuedContext{Context: c.resourceContext(), waiting: make(chan struct{})}
	type outcome struct {
		value string
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		v, e := statusResourceGit(waiter, args, func() (string, error) { return "current", nil })
		done <- outcome{v, e}
	}()
	select {
	case <-waiter.waiting:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("waiter did not queue")
	}
	cancel()
	close(release)
	if err := <-leadDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.value != "current" {
			t.Fatalf("live waiter inherited canceled leader: %q %v", got.value, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("live waiter failed to retry")
	}
}

func TestStatusResourceIdentityLegacyRemoteFallback(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	root := t.TempDir()
	fixtureGit(t, "init", "-q", root)
	remote := filepath.Join(root, ".git/remotes/origin")
	c := (&Context{}).WithStatusContext(context.Background())
	args := []string{"-C", root, "remote", "get-url", "origin"}
	for _, url := range []string{"https://example.invalid/first.git", "https://example.invalid/second.git"} {
		fixtureFile(t, remote, "URL: "+url+"\nPull: refs/heads/main:refs/remotes/origin/main\n", 0600)
		want, err := gitOutput(args...)
		if err != nil {
			t.Skip("Git does not support legacy remote definitions")
		}
		got, err := gitOutputContext(c.resourceContext(), args...)
		if err != nil || got != want {
			t.Fatalf("legacy remote identity reused: got%q want%q err%v", got, want, err)
		}
	}
}

func TestStatusResourceIdentityNullConfigDiscardDoesNotInvalidate(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
	calls := 0
	read := func() (string, error) { calls++; return gitOutput(args...) }
	first, err := statusResourceGit(c.resourceContext(), args, read)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	discarded, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := discarded.Write([]byte("Synthetic discarded output\n"))
	closeErr := discarded.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal(writeErr, closeErr)
	}
	after, err := os.Stat(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if before.ModTime() == after.ModTime() {
		t.Skip("null-device writes do not update metadata on this platform")
	}
	got, err := statusResourceGit(c.resourceContext(), args, read)
	if err != nil || got != first || calls != 1 {
		t.Fatalf("discarded output invalidated unchanged empty configuration: calls%d got%q want%q err%v", calls, got, first, err)
	}
}

func TestStatusResourceIdentityRegularGlobalConfigStillInvalidates(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	fixtureGit(t, "-C", c.Harness, "remote", "set-url", "origin", "synthetic:widget")
	config := filepath.Join(t.TempDir(), "gitconfig")
	fixtureFile(t, config, "# Synthetic config\n", 0600)
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
	calls := 0
	read := func() (string, error) { calls++; return gitOutput(args...) }
	if got, err := statusResourceGit(c.resourceContext(), args, read); err != nil || got != "synthetic:widget" {
		t.Fatal(got, err)
	}
	fixtureFile(t, config, "[url \"https://example.invalid/\"]\n insteadOf = synthetic:\n", 0600)
	got, err := statusResourceGit(c.resourceContext(), args, read)
	if err != nil || got != "https://example.invalid/widget" || calls != 2 {
		t.Fatal("changed real config was reused", got, err, calls)
	}
}

func TestStatusResourceIdentityOwnerRemoteReuse(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	owner := fixtureGit(t, "-C", c.Harness, "rev-parse", "--path-format=absolute", "--git-common-dir")
	args := []string{"--git-dir=" + owner, "remote"}
	calls := 0
	read := func() (string, error) { calls++; return gitOutput(args...) }
	for i := 0; i < 2; i++ {
		if got, err := statusResourceGit(c.resourceContext(), args, read); err != nil || got != "origin" {
			t.Fatal(got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("allowlisted owner remote never reused: %d", calls)
	}
}

func TestStatusResourceIdentityMarkerPathsRemainFresh(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	args := []string{"-C", c.Harness, "rev-parse", "--path-format=absolute"}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		args = append(args, "--git-path", marker)
	}
	calls := 0
	read := func() (string, error) { calls++; return gitOutput(args...) }
	for i := 0; i < 2; i++ {
		if _, err := statusResourceGit(c.resourceContext(), args, read); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("unchanged operation-path identities redispatched: %d", calls)
	}
	target := catchUpTarget{path: c.Harness}
	marker := fixtureGit(t, "-C", c.Harness, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
	fixtureFile(t, marker, strings.Repeat("a", 40)+"\n", 0600)
	if got := statusResourceOperationState(c.resourceContext(), target); got != "MERGE_HEAD" {
		t.Fatal("cached marker paths hid new operation", got)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if got := statusResourceOperationState(c.resourceContext(), target); got != "" {
		t.Fatal("cached marker paths retained old operation", got)
	}
}

func TestStatusResourceUpstreamReadIsOneCurrentObservation(t *testing.T) {
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
		t.Fatal("missing target")
	}
	fixtureGit(t, "-C", target.path, "switch", "-qc", "topic")
	bin := t.TempDir()
	calls := filepath.Join(bin, "upstream-calls")
	t.Setenv("RESOURCE_UPSTREAM_CALLS", calls)
	fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$3 $4 $5" = 'rev-parse --abbrev-ref @{u}' ] || [ "$3" = for-each-ref ]; then printf x >> "$RESOURCE_UPSTREAM_CALLS"; fi
exec /usr/bin/git "$@"
`, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := statusLocalRepoContext(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	n := resourceCalls(calls)
	want, err := statusResourceLocalReference(context.Background(), target)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, want, err)
	}
	if n != 1 {
		t.Fatalf("unchanged local upstream required %d queries", n)
	}
}

func TestStatusResourceIdentityRelativeConfigFallsBack(t *testing.T) {
	for _, kind := range []string{"global", "system"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			c := newWorkspaceFixture(t).WithStatusContext(context.Background())
			fixtureGit(t, "-C", c.Harness, "remote", "set-url", "origin", "synthetic:widget")
			relative := "synthetic-relative-config"
			config := filepath.Join(c.Harness, relative)
			fixtureFile(t, config, "# Synthetic config\n", 0600)
			if kind == "global" {
				t.Setenv("GIT_CONFIG_GLOBAL", relative)
			} else {
				t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
				t.Setenv("GIT_CONFIG_SYSTEM", relative)
			}
			args := []string{"-C", c.Harness, "remote", "get-url", "origin"}
			if got, err := gitOutputContext(c.resourceContext(), args...); err != nil || got != "synthetic:widget" {
				t.Fatal(got, err)
			}
			fixtureFile(t, config, "[url \"https://example.invalid/\"]\n insteadOf = synthetic:\n", 0600)
			want, err := gitOutput(args...)
			if err != nil || want != "https://example.invalid/widget" {
				t.Fatal("invalid actualGit fixture", want, err)
			}
			got, err := gitOutputContext(c.resourceContext(), args...)
			if err != nil || got != want {
				t.Fatalf("relative config reused wrong-cwd identity: got%q want%q err%v", got, want, err)
			}
		})
	}
}
