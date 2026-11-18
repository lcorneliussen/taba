package wtc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Counts against a different development base still require their own read.
// The independent oracle deliberately retains the pre-batching commands.
func TestStatusResourceTrackingWholeRows(t *testing.T) {
	c := newWorkspaceFixture(t)
	git := func(args ...string) string { return fixtureGit(t, append([]string{"-C", c.Harness}, args...)...) }
	git("switch", "-qc", "topic")
	git("branch", "--set-upstream-to=origin/main")
	target := catchUpTarget{path: c.Harness, collection: filepath.Base(c.Collection), repo: "agent-harness", ref: "origin/main"}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$3" = for-each-ref ] || [ "$3" = rev-list ]; then printf x >> "$RESOURCE_TRACKING_CALLS"; fi
exec /usr/bin/git "$@"
`, 0700)
	t.Setenv("RESOURCE_TRACKING_CALLS", calls)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repeated := 0
	check := func(label string, expectBatch bool) {
		t.Helper()
		before := resourceCalls(calls)
		got, ge := statusLocalRepoContext(context.Background(), target)
		used := resourceCalls(calls) - before
		want, we := statusResourceLocalReference(context.Background(), target)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s got%+v/%v want%+v/%v", label, got, ge, want, we)
		}
		t.Logf("%s whole-row oracle equal; independent upstream/count dispatches=%d", label, used)
		if expectBatch && used != 0 {
			repeated += used
		}
		if !expectBatch && used == 0 {
			t.Fatalf("%s skipped required fallback", label)
		}
	}
	check("tracking-clean", true)
	fixtureFile(t, filepath.Join(c.Harness, "synthetic"), "one", 0600)
	check("tracking-untracked", true)
	git("add", "synthetic")
	check("tracking-staged", true)
	git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic ahead")
	check("tracking-ahead", true)
	git("branch", "alternate", "HEAD")
	git("checkout", "-q", "--detach", "origin/main")
	fixtureFile(t, filepath.Join(c.Harness, "remote-only"), "remote", 0600)
	git("add", "remote-only")
	git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic remote")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("switch", "-q", "topic")
	check("tracking-diverged", true)
	git("branch", "--set-upstream-to=alternate")
	check("different-upstream", false)
	git("branch", "--set-upstream-to=origin/main")
	git("config", "status.aheadBehind", "false")
	check("suppressed-counts", false)
	git("config", "--unset", "status.aheadBehind")
	git("update-ref", "-d", "refs/remotes/origin/main")
	target.ref = "alternate"
	check("gone-valid-distinct-base", false)
	target.ref = "origin/main"
	check("missing-base", false)
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	check("changed-base", true)
	git("branch", "--unset-upstream")
	check("no-upstream", false)
	git("checkout", "-q", "--detach")
	check("detached", false)
	if repeated != 0 {
		t.Fatalf("fresh exact-upstream porcelain counts repeated %d independent Git reads", repeated)
	}
}
