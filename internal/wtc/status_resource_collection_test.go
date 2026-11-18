package wtc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"taba.sh/taba/internal/next"
)

func TestStatusResourceForgeCalculatesFinalChoicesOnce(t *testing.T) {
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	fixtureFile(t, filepath.Join(c.Harness, "work"), "synthetic change\n", 0644)
	fixtureGit(t, "-C", c.Harness, "add", "work")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic change")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("RESOURCE_REAL_GIT", real)
	t.Setenv("RESOURCE_NEXT_CALLS", calls)
	fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$3 $4" = 'rev-parse HEAD' ]; then
 printf '%s\n' "$*" >> "$RESOURCE_NEXT_CALLS"
fi
exec "$RESOURCE_REAL_GIT" "$@"
`, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	local, err := c.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.Repos) != 1 || local.Repos[0].Next == nil || local.Repos[0].Next.Step != "push" {
		t.Fatalf("standalone local choices missing: %+v", local.Repos)
	}
	if err := os.WriteFile(calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	forge, err := c.statusForgePreview(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(forge.Repos) != 1 || !reflect.DeepEqual(local.Repos[0].Choices, forge.Repos[0].Choices) {
		t.Fatalf("final choices differ: local=%+v forge=%+v", local.Repos, forge.Repos)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); got != 1 {
		t.Fatalf("complete collector read action heads %d times, want one final read; argv:\n%s", got, raw)
	}
}

func TestStatusResourceFinalChoicesObserveChangedHead(t *testing.T) {
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic unpublished head")
	before, err := c.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	c.StatusProgress = func(message string) {
		if message != "Checking build providers" {
			return
		}
		fixtureGit(t, "-C", c.Harness, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic concurrent head")
		changed = true
	}
	snapshot, err := c.statusForgePreview(true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(snapshot.Repos) != 1 || len(before.Repos) != 1 {
		t.Fatal("missing actual head transition")
	}
	want := next.ChoicesFor(statusFactsContext(context.Background(), snapshot.Repos[0]))
	if !reflect.DeepEqual(snapshot.Repos[0].Choices, &want) || reflect.DeepEqual(snapshot.Repos[0].Choices, before.Repos[0].Choices) {
		t.Fatal("final choices reused authority from before the head transition")
	}
}

func TestStatusResourceCancelledFinalChoicesReturnError(t *testing.T) {
	base := newWorkspaceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := base.WithStatusContext(ctx)
	c.StatusProgress = func(message string) {
		if message == "Checking build providers" {
			cancel()
		}
	}
	_, err := c.statusForgePreview(true, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled final collector returned success: %v", err)
	}
	for _, name := range []string{".wtc-status.json", ".wtc-status.md", ".last-wtc-status.yml"} {
		if _, err := os.Stat(filepath.Join(c.Collection, name)); !os.IsNotExist(err) {
			t.Fatalf("canceled collector published %s: %v", name, err)
		}
	}
}

func TestStatusResourcePublicationUsesOneHeadObservation(t *testing.T) {
	c := newWorkspaceFixture(t).WithStatusContext(context.Background())
	fixtureGit(t, "-C", c.Harness, "config", "core.abbrev", "12")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("RESOURCE_REAL_GIT", real)
	t.Setenv("RESOURCE_NEXT_CALLS", calls)
	fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$3" = rev-parse ] && { [ "$4" = HEAD ] || [ "$4" = --short ]; }; then
 printf '%s\n' "$*" >> "$RESOURCE_NEXT_CALLS"
fi
exec "$RESOURCE_REAL_GIT" "$@"
`, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	snapshot, _, err := c.StatusLiveSnapshot(false, true)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := c.ReadStatusSnapshot()
	if err != nil || len(snapshot.Repos) != 1 || len(cached.Repos) != 1 || !reflect.DeepEqual(snapshot.Repos[0].Choices, cached.Repos[0].Choices) {
		t.Fatalf("canonical choices changed at publication: %v", err)
	}
	short, err := exec.Command(real, "-C", c.Harness, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(filepath.Join(c.Collection, ".last-wtc-status.yml"))
	if err != nil || !strings.Contains(string(legacy), "head: '"+strings.TrimSpace(string(short))+"'") {
		t.Fatalf("legacy unique abbreviation changed: %v", err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); got != 1 {
		t.Fatalf("publication read head %d times, want one coherent full/base/unique-short observation; argv:\n%s", got, raw)
	}
}
