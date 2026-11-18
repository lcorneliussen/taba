package wtc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
