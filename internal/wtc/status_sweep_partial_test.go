package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// statusSweepFixture builds a synthetic workspace with a clean, a dirty and a
// policy-conflicting collection next to the initiating one. The conflicting
// collection's generated environment names a control root the receiving
// policy rejects, so it cannot be opened. It returns the rejected root.
func statusSweepFixture(t *testing.T) (*Context, string) {
	t.Helper()
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	c := newWorkspaceFixture(t)
	for _, slug := range []string{"clean", "dirty", "conflict"} {
		if _, err := c.NewCollection(NewOptions{Slug: slug}); err != nil {
			t.Fatal(err)
		}
	}
	fixtureFile(t, filepath.Join(c.Workspace, "dirty", "harness", "in-flight.txt"), "work in progress\n", 0644)
	stale := statusSweepConflict(t, filepath.Join(c.Workspace, "conflict"))
	return c, stale
}

// statusSweepConflict points a collection's generated control root elsewhere.
func statusSweepConflict(t *testing.T, collection string) string {
	t.Helper()
	env := filepath.Join(collection, ".env.collection")
	data, err := os.ReadFile(env)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	stale := filepath.Join(t.TempDir(), "stale-root-marker")
	if err := os.Mkdir(stale, 0755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "WTC_CONFIG_ROOT=") || strings.HasPrefix(line, "TABA_CONFIG_ROOT=") {
			continue
		}
		lines = append(lines, line)
	}
	lines = append(lines, "WTC_CONFIG_ROOT='"+stale+"'", "")
	if err := os.WriteFile(env, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCollectionContext(t.Context(), collection); err == nil {
		t.Fatal("conflicting fixture collection still opens")
	}
	return stale
}

// statusSweepHook installs a status build hook that leaves a marker if run.
func statusSweepHook(t *testing.T, collection string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	fixtureFile(t, filepath.Join(collection, "harness", "hooks", "wtc", "status.build.sh"), "#!/bin/sh\ntouch '"+marker+"'\n", 0755)
	return marker
}

// statusSweepTree records every path, size, mode and modification time below dir.
func statusSweepTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		tree[path] = info.ModTime().String() + "|" + info.Mode().String()
		if !info.IsDir() {
			tree[path] += "|" + strconv.FormatInt(info.Size(), 10)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func statusSweepSameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("rejected collection changed: %d paths before, %d after", len(before), len(after))
	}
	for path, value := range before {
		if after[path] != value {
			t.Fatalf("rejected collection changed at %s", path)
		}
	}
}

func statusSweepCollections(snapshot StatusSnapshot) map[string]bool {
	seen := map[string]bool{}
	for _, row := range snapshot.Repos {
		seen[row.Collection] = true
	}
	return seen
}

func statusSweepNoLeak(t *testing.T, stale string, outputs ...string) {
	t.Helper()
	for _, out := range outputs {
		for _, secret := range []string{stale, "stale-root-marker", "WTC_CONFIG_ROOT", "no files changed"} {
			if strings.Contains(out, secret) {
				t.Fatalf("report leaks %q:\n%s", secret, out)
			}
		}
	}
}

func TestStatusAllReportsReadableCollectionsBesideAnUnreadableOne(t *testing.T) {
	for _, noFetch := range []bool{false, true} {
		t.Run(map[bool]string{false: "fetch", true: "no-fetch"}[noFetch], func(t *testing.T) {
			c, stale := statusSweepFixture(t)
			conflict := filepath.Join(c.Workspace, "conflict")
			marker := statusSweepHook(t, conflict)
			before := statusSweepTree(t, conflict)
			snapshot, report, err := c.StatusLiveSnapshotWithFetchAge(true, noFetch, 0)
			if err != nil {
				t.Fatalf("status --all aborted on one unreadable collection: %v", err)
			}
			seen := statusSweepCollections(snapshot)
			for _, name := range []string{"main", "clean", "dirty"} {
				if !seen[name] {
					t.Fatalf("readable collection %s missing from sweep: %v", name, seen)
				}
			}
			if seen["conflict"] {
				t.Fatalf("rejected collection was read: %+v", snapshot.Repos)
			}
			want := []StatusUnreadable{{Collection: "conflict", Reason: unreadableGeneratedRoot}}
			if len(snapshot.Unreadable) != 1 || snapshot.Unreadable[0] != want[0] {
				t.Fatalf("wrong unreadable rows: %+v", snapshot.Unreadable)
			}
			if !noFetch && (report.Attempted == 0 || report.Failed != 0) {
				t.Fatalf("readable owners were not refreshed: %+v", report)
			}
			md := snapshot.Markdown()
			if !strings.Contains(md, "## Unreadable collections") || !strings.Contains(md, "**conflict** — generated environment names a control root") {
				t.Fatalf("unreadable collection not reported:\n%s", md)
			}
			data, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"unreadable":[{"collection":"conflict"`) {
				t.Fatalf("JSON lacks unreadable rows: %s", data)
			}
			statusSweepNoLeak(t, stale, md, string(data))
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("rejected collection hook ran: %v", err)
			}
			statusSweepSameTree(t, before, statusSweepTree(t, conflict))
		})
	}
}

func TestStatusLocalAllReportsReadableCollectionsBesideAnUnreadableOne(t *testing.T) {
	c, stale := statusSweepFixture(t)
	conflict := filepath.Join(c.Workspace, "conflict")
	before := statusSweepTree(t, conflict)
	snapshot, err := c.StatusLocalSnapshot(true)
	if err != nil {
		t.Fatalf("status --local --all aborted on one unreadable collection: %v", err)
	}
	seen := statusSweepCollections(snapshot)
	for _, name := range []string{"main", "clean", "dirty"} {
		if !seen[name] {
			t.Fatalf("readable collection %s missing from local sweep: %v", name, seen)
		}
	}
	if len(snapshot.Unreadable) != 1 || snapshot.Unreadable[0] != (StatusUnreadable{Collection: "conflict", Reason: unreadableGeneratedRoot}) {
		t.Fatalf("wrong unreadable rows: %+v", snapshot.Unreadable)
	}
	statusSweepNoLeak(t, stale, snapshot.Markdown())
	statusSweepSameTree(t, before, statusSweepTree(t, conflict))
}

// A scoped status keeps requiring its own inventory.
func TestStatusScopedUnaffectedByOtherUnreadableCollection(t *testing.T) {
	c, _ := statusSweepFixture(t)
	snapshot, _, err := c.StatusLiveSnapshot(false, false)
	if err != nil || snapshot.Collection != "main" || len(snapshot.Unreadable) != 0 {
		t.Fatalf("scoped status changed: %+v %v", snapshot.Unreadable, err)
	}
}

func TestStatusAllEveryCollectionUnreadable(t *testing.T) {
	for _, noFetch := range []bool{false, true} {
		t.Run(map[bool]string{false: "fetch", true: "no-fetch"}[noFetch], func(t *testing.T) {
			c, stale := statusSweepFixture(t)
			for _, name := range []string{"main", "clean", "dirty"} {
				statusSweepConflict(t, filepath.Join(c.Workspace, name))
			}
			snapshot, report, err := c.StatusLiveSnapshotWithFetchAge(true, noFetch, 0)
			if err != nil {
				t.Fatalf("all-unreadable sweep aborted: %v", err)
			}
			if len(snapshot.Repos) != 0 || len(snapshot.Unreadable) != 4 || report.Attempted != 0 {
				t.Fatalf("wrong all-unreadable result: repos=%d unreadable=%+v fetch=%+v", len(snapshot.Repos), snapshot.Unreadable, report)
			}
			for _, row := range snapshot.Unreadable {
				if row.Reason != unreadableGeneratedRoot {
					t.Fatalf("wrong reason: %+v", row)
				}
			}
			statusSweepNoLeak(t, stale, snapshot.Markdown())
		})
	}
}

func TestStatusAllCancellationEndsTheSweep(t *testing.T) {
	c, _ := statusSweepFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StatusProgress = func(step string) {
		if strings.HasPrefix(step, "Checking collection 1/") {
			cancel()
		}
	}
	snapshot, _, err := c.StatusLiveSnapshotContext(ctx, true, true, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became a partial result: %v %+v", err, snapshot.Unreadable)
	}
	if len(snapshot.Unreadable) != 0 {
		t.Fatalf("cancellation marked collections unreadable: %+v", snapshot.Unreadable)
	}
}

func TestStatusAllCancelledBeforeInventory(t *testing.T) {
	c, _ := statusSweepFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.WithStatusContext(ctx).StatusRefreshRefsWithAge(true, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ref refresh did not stop: %v", err)
	}
	if _, err := c.WithStatusContext(ctx).StatusLocalSnapshot(true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled local sweep did not stop: %v", err)
	}
}

// Control: a dirty worktree alone never blocks a status sweep.
func TestStatusAllDirtyCollectionAloneIsObserved(t *testing.T) {
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	c := newWorkspaceFixture(t)
	if _, err := c.NewCollection(NewOptions{Slug: "dirty"}); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.Workspace, "dirty", "harness", "in-flight.txt"), "work in progress\n", 0644)
	snapshot, report, err := c.StatusLiveSnapshot(true, false)
	if err != nil {
		t.Fatalf("dirty collection blocked status sweep: %v", err)
	}
	if !statusSweepCollections(snapshot)["dirty"] || report.Failed != 0 || len(snapshot.Unreadable) != 0 {
		t.Fatalf("dirty collection not observed: %+v %+v %+v", snapshot.Repos, report, snapshot.Unreadable)
	}
}

// Control: catch-up keeps its own refusal and exit semantics for the same
// workspace, and its open-failure row now names a sanitized class.
func TestCatchUpAllKeepsRefusalsForPartialWorkspace(t *testing.T) {
	c, stale := statusSweepFixture(t)
	report, err := c.CatchUp(CatchUpOptions{All: true, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "needs attention") {
		t.Fatalf("catch-up lost its refusal: %v", err)
	}
	if report.ExitStatus != 1 {
		t.Fatalf("catch-up lost its nonzero exit: %+v", report)
	}
	var conflict, dirty bool
	for _, row := range report.Outcomes {
		if row.Collection == "conflict" && row.Outcome == "failed" && row.Reason == catchUpOpenFailurePrefix+unreadableGeneratedRoot {
			conflict = true
		}
		if row.Collection == "dirty" && row.Outcome == "needs-owner" {
			dirty = true
		}
	}
	if !conflict || !dirty {
		t.Fatalf("catch-up rows changed: %+v", report.Outcomes)
	}
	statusSweepNoLeak(t, stale, report.Markdown())
}

func TestCollectionOpenFailureClassesAreFixed(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("TABA_CONFIG_ROOT in .env.collection conflicts with receiving secrets policy; preview x; no files changed"), unreadableGeneratedRoot},
		{errors.New("WTC_CONFIG_ROOT in .env.collection.local conflicts with receiving secrets policy; remove; no files changed"), unreadableLocalRoot},
		{errors.New("WTC_CONFIG_ROOT in inherited environment conflicts with receiving secrets policy; x"), unreadableInheritedRoot},
		{errors.New("receiving environment must be a regular file, not a symlink or special file"), unreadableEnvironment},
		{errors.New("external secrets root requires an exact receiving [secrets].root"), unreadablePolicy},
		{errors.New("registry escapes receiving owner"), unreadablePolicy},
		{errors.New("invalid or repeated repository name \"x\""), unreadableOther},
		{&fs.PathError{Op: "stat", Path: "/private/secret/path/.harness-repos.yml", Err: fs.ErrNotExist}, unreadableRegistry},
		{errors.New("not a collection"), unreadableNotCollection},
		{errors.New("token=abc123 /home/user/secret raw tool output"), unreadableOther},
	} {
		if got := collectionOpenFailure(tc.err); got != tc.want {
			t.Fatalf("%q classified as %q, want %q", tc.err, got, tc.want)
		}
	}
	rows := statusInventoryUnreadable(CatchUpReport{Outcomes: []CatchUpRow{
		{Kind: "collection", Collection: "raw", Outcome: "failed", Reason: "open /private/secret: permission denied"},
		{Kind: "collection", Collection: "named", Outcome: "failed", Reason: unreadableName},
		{Kind: "repo", Collection: "owner", Repo: "widget", Outcome: "failed", Reason: "cannot find Git owner"},
		{Kind: "repo", Collection: "dirty", Repo: "widget", Outcome: "needs-owner", Reason: "dirty worktree"},
	}})
	want := []StatusUnreadable{{"raw", unreadableInventory}, {"named", unreadableName}, {"owner", unreadableRepoOwner + " widget"}}
	if len(rows) != len(want) {
		t.Fatalf("wrong inventory rows: %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("wrong inventory row %d: %+v", i, rows[i])
		}
	}
}
