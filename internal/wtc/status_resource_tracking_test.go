package wtc

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"taba.sh/taba/internal/gitref"
)

// Counts against a different development base still require their own read.
// The independent oracle deliberately retains the pre-batching commands.
func TestStatusResourceTrackingWholeRows(t *testing.T) {
	c := newWorkspaceFixture(t)
	git := func(args ...string) string { return fixtureGit(t, append([]string{"-C", c.Harness}, args...)...) }
	git("switch", "-qc", "topic")
	git("branch", "--set-upstream-to=origin/main")
	target := catchUpTarget{path: c.Harness, collection: filepath.Base(c.Collection), repo: "agent-harness", ref: gitref.RemoteBranch("origin", "main")}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$3" = for-each-ref ] || [ "$3" = rev-list ]; then printf x >> "$RESOURCE_TRACKING_CALLS"; fi
if [ "$3 $4" = 'status --porcelain=v2' ] && [ "$RESOURCE_SUPPRESS_COUNTS" = 1 ]; then exec /usr/bin/git "$@" --no-ahead-behind; fi
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
	t.Setenv("RESOURCE_SUPPRESS_COUNTS", "1")
	check("suppressed-counts", false)
	t.Setenv("RESOURCE_SUPPRESS_COUNTS", "")
	git("update-ref", "-d", "refs/remotes/origin/main")
	target.ref = gitref.LocalBranch("alternate")
	check("gone-valid-distinct-base", false)
	target.ref = gitref.RemoteBranch("origin", "main")
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

func TestStatusResourceTrackingEvidence(t *testing.T) {
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
printf '%s\n' "$RESOURCE_TRACKING_RAW"
`, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	base := "# branch.oid " + strings.Repeat("a", 40) + "\n# branch.head topic\n"
	for _, tc := range []struct {
		name, extra   string
		known         bool
		ahead, behind int
	}{
		{"valid", "# branch.upstream origin/main\n# branch.ab +7 -3", true, 7, 3},
		{"zero", "# branch.upstream origin/main\n# branch.ab +0 -0", true, 0, 0},
		{"none", "", false, 0, 0},
		{"duplicate-oid", "# branch.oid " + strings.Repeat("b", 40), false, 0, 0},
		{"missing-counts", "# branch.upstream origin/main", false, 0, 0},
		{"unknown-counts", "# branch.upstream origin/main\n# branch.ab +? -?", false, 0, 0},
		{"negative", "# branch.upstream origin/main\n# branch.ab +-1 -0", false, 0, 0},
		{"overflow", "# branch.upstream origin/main\n# branch.ab +999999999999999999999999 -0", false, 0, 0},
		{"reversed", "# branch.upstream origin/main\n# branch.ab -1 +2", false, 0, 0},
		{"extra-count", "# branch.upstream origin/main\n# branch.ab +1 -2 +3", false, 0, 0},
		{"missing-upstream", "# branch.ab +1 -0", false, 0, 0},
		{"duplicate-upstream", "# branch.upstream origin/main\n# branch.upstream origin/main\n# branch.ab +1 -0", false, 0, 0},
		{"duplicate-counts", "# branch.upstream origin/main\n# branch.ab +1 -0\n# branch.ab +1 -0", false, 0, 0},
		{"duplicate-branch", "# branch.head other\n# branch.upstream origin/main\n# branch.ab +1 -0", false, 0, 0},
		{"invalid-upstream", "# branch.upstream origin/ main\n# branch.ab +1 -0", false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RESOURCE_TRACKING_RAW", base+tc.extra)
			got, ok := statusResourceLocalRead(context.Background(), t.TempDir())
			if !ok || got.tracking != tc.known || got.noUpstream != (tc.name == "none") || tc.known && (got.ahead != tc.ahead || got.behind != tc.behind) {
				t.Fatalf("%s got%+v ok%v", tc.name, got, ok)
			}
		})
	}
}

func TestStatusResourceTrackingAmbiguousRef(t *testing.T) {
	c := newWorkspaceFixture(t)
	git := func(args ...string) string { return fixtureGit(t, append([]string{"-C", c.Harness}, args...)...) }
	git("switch", "-qc", "topic")
	git("branch", "--set-upstream-to=origin/main")
	git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic ahead")
	git("tag", "origin/main", "HEAD")
	target := catchUpTarget{path: c.Harness, collection: filepath.Base(c.Collection), repo: "agent-harness", ref: gitref.RemoteBranch("origin", "main")}
	for _, ambiguity := range []string{"true", "false"} {
		git("config", "core.warnAmbiguousRefs", ambiguity)
		got, ge := statusLocalRepoContext(context.Background(), target)
		want, we := statusResourceLocalReference(context.Background(), target)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("ambiguity%v got%+v/%v want%+v/%v", ambiguity, got, ge, want, we)
		}
		if ambiguity == "true" {
			if ge == nil || ge.Error() != we.Error() || !strings.Contains(ge.Error(), "is ambiguous") {
				t.Fatalf("ambiguous reference lost the existing unknown/error outcome: %v/%v", ge, we)
			}
			continue
		}
		if ge != nil || got.Ahead != 1 || got.Behind != 0 || got.AheadRef != "remotes/origin/main" {
			t.Fatal("ambiguous comparison fixture lost distinct counts", got)
		}
	}
}

func TestStatusResourceNoUpstreamWholeRows(t *testing.T) {
	c := newWorkspaceFixture(t)
	git := func(args ...string) string { return fixtureGit(t, append([]string{"-C", c.Harness}, args...)...) }
	git("switch", "-qc", "topic")
	target := catchUpTarget{path: c.Harness, collection: filepath.Base(c.Collection), repo: "agent-harness", ref: gitref.RemoteBranch("origin", "main")}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$3" = for-each-ref ] || [ "$3 $4 $5" = 'rev-parse --abbrev-ref @{u}' ]; then printf x >> "$RESOURCE_NO_UPSTREAM_CALLS"; fi
exec /usr/bin/git "$@"
`, 0700)
	t.Setenv("RESOURCE_NO_UPSTREAM_CALLS", calls)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repeated := 0
	check := func(label string) {
		t.Helper()
		before := resourceCalls(calls)
		got, ge := statusLocalRepoContext(context.Background(), target)
		n := resourceCalls(calls) - before
		want, we := statusResourceLocalReference(context.Background(), target)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s got%+v/%v want%+v/%v", label, got, ge, want, we)
		}
		if ge != nil {
			t.Fatal(label, ge)
		}
		if got.UpstreamState != "none" || got.AheadBasis != "development-tip" {
			t.Fatal(label, got)
		}
		t.Logf("%s complete row matches independent oracle; upstream reads=%d", label, n)
		repeated += n
	}
	check("clean")
	fixtureFile(t, filepath.Join(c.Harness, "synthetic"), "one", 0600)
	check("untracked")
	git("add", "synthetic")
	check("staged")
	git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic ahead")
	check("ahead")
	fixtureFile(t, filepath.Join(c.Harness, "synthetic"), "two", 0600)
	check("dirty")
	git("reset", "--hard", "HEAD")
	git("mv", "synthetic", "renamed")
	check("rename")
	git("reset", "--hard", "HEAD")
	git("switch", "-c", "(detached)")
	check("legal-detached-branch")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	check("changed-base")
	git("branch", "--set-upstream-to=origin/main")
	got, ge := statusLocalRepoContext(context.Background(), target)
	want, we := statusResourceLocalReference(context.Background(), target)
	if ge != nil || we != nil || !reflect.DeepEqual(got, want) || got.UpstreamState != "tracking" {
		t.Fatal("new upstream was not observed", got, want, ge, we)
	}
	git("branch", "--unset-upstream")
	check("removed-upstream")
	if repeated != 0 {
		t.Fatalf("valid fresh no-upstream porcelain repeated %d Git probes", repeated)
	}
}
