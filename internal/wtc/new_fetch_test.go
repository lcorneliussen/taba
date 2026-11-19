package wtc

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fetchGateGit wraps git so an owner fetch waits until expect owner fetches
// are in flight together. A fetch that gives up waiting is recorded as
// alone: creation fetched it without overlapping the others.
const fetchGateGit = `#!/bin/sh
case "$*" in
  --git-dir=*" fetch --prune origin")
    owner=$(basename "${1#--git-dir=}")
    echo "$owner" >> "$FETCH_LOG"
    if [ "$owner" = "$FETCH_FAIL" ]; then echo "synthetic fetch failure" >&2; exit 1; fi
    mkdir -p "$FETCH_GATE"; : > "$FETCH_GATE/$owner.$$"
    i=0
    while [ "$(ls "$FETCH_GATE" | wc -l)" -lt "$FETCH_EXPECT" ]; do
      i=$((i+1))
      if [ $i -gt 50 ]; then echo "$owner" >> "$FETCH_ALONE"; break; fi
      sleep 0.1
    done
    ;;
esac
exec "$REAL_GIT" "$@"
`

type fetchGate struct{ log, alone string }

func installFetchGate(t *testing.T, expect, fail string) fetchGate {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	fixtureFile(t, filepath.Join(bin, "git"), fetchGateGit, 0755)
	gate := fetchGate{log: filepath.Join(dir, "fetches"), alone: filepath.Join(dir, "alone")}
	t.Setenv("REAL_GIT", real)
	t.Setenv("FETCH_LOG", gate.log)
	t.Setenv("FETCH_ALONE", gate.alone)
	t.Setenv("FETCH_GATE", filepath.Join(dir, "gate"))
	t.Setenv("FETCH_EXPECT", expect)
	t.Setenv("FETCH_FAIL", fail)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return gate
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	sort.Strings(lines)
	return lines
}

// Creation waits on the network for its existing owners together, fetches
// each owner once, and still checks the repositories out in order.
func TestNewCollectionFetchesExistingOwnersTogetherOnce(t *testing.T) {
	c := newWorkspaceFixture(t)
	gate := installFetchGate(t, "2", "")
	r, err := c.NewCollection(NewOptions{Slug: "overlap", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if alone := readLines(t, gate.alone); len(alone) != 0 {
		t.Fatalf("owners fetched one after another: %v", alone)
	}
	if got := strings.Join(readLines(t, gate.log), " "); got != "agent-harness.git widget.git" {
		t.Fatalf("each owner should be fetched exactly once, got %q", got)
	}
	for _, name := range []string{"harness", "widget"} {
		if ref := fixtureGit(t, "-C", filepath.Join(r.Collection, name), "rev-parse", "--abbrev-ref", "HEAD"); ref != "HEAD" {
			t.Errorf("%s should be detached at the tip, got %q", name, ref)
		}
	}
	// A later creation from the same context fetches again: the record
	// belongs to one creation only.
	if _, err := c.NewCollection(NewOptions{Slug: "again", Repos: []string{"widget"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(readLines(t, gate.log)); got != 4 {
		t.Fatalf("second creation reused an earlier fetch: %d fetches in total", got)
	}
}

// One owner has nothing to overlap with and is fetched once, in order.
func TestNewCollectionSingleOwnerFetchesOnce(t *testing.T) {
	c := newWorkspaceFixture(t)
	gate := installFetchGate(t, "1", "")
	if _, err := c.NewCollection(NewOptions{Slug: "solo"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(readLines(t, gate.log), " "); got != "agent-harness.git" {
		t.Fatalf("harness-only creation fetched %q", got)
	}
}

// A failed fetch is not hidden by the early attempt: the ordered checkout of
// that repository fetches again and fails where it always did.
func TestNewCollectionFetchFailureSurfacesInOrder(t *testing.T) {
	c := newWorkspaceFixture(t)
	installFetchGate(t, "1", "widget.git")
	r, err := c.NewCollection(NewOptions{Slug: "broken", Repos: []string{"widget"}})
	if err == nil || !strings.Contains(err.Error(), "add widget:") || !strings.Contains(err.Error(), "synthetic fetch failure") {
		t.Fatalf("fetch failure not reported for widget: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(r.Collection, "harness", "README.md")); statErr != nil {
		t.Fatalf("harness checkout before the failing repository is missing: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(r.Collection, "widget")); !os.IsNotExist(statErr) {
		t.Fatalf("failing repository was checked out: %v", statErr)
	}
}

// An owner whose origin moved after its fetch is fetched again.
func TestOwnerFetchRecordFollowsOrigin(t *testing.T) {
	c := newWorkspaceFixture(t)
	bare := filepath.Join(c.Workspace, ".bare", "widget.git")
	record := &ownerFetches{}
	record.fetched(bare)
	if !record.current(bare) {
		t.Fatal("fetched owner not recorded")
	}
	fixtureGit(t, "--git-dir="+bare, "remote", "set-url", "origin", filepath.Join(c.Workspace, "elsewhere"))
	if record.current(bare) {
		t.Fatal("owner with a moved origin counted as fetched")
	}
	var none *ownerFetches
	if none.current(bare) {
		t.Fatal("no record must never skip a fetch")
	}
}
