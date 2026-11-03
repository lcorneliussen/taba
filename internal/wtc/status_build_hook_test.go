package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusBuildHookAddsTipAndProductionFacts(t *testing.T) {
	c := newWorkspaceFixture(t)
	c.Registry.Repos[0].ProductionRef = "origin/prod"
	hook := filepath.Join(c.Harness, "hooks", "wtc", "status.build.sh")
	if err := os.MkdirAll(filepath.Dir(hook), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TABA_COLLECTION", "inherited-from-another-collection")
	script := `#!/bin/sh
[ -n "$WTC_COLLECTION" ] && [ "$TABA_COLLECTION" = "$WTC_COLLECTION" ] && [ "$TABA_CONFIG_ROOT" = "$WTC_CONFIG_ROOT" ] || exit 4
body=$(cat)
case "$body" in
  *'"tier":"tip"'*) printf '{"branch":"main","checks":"SUCCESS","build":"123","url":"https://example.invalid/123"}\n' ;;
  *'"tier":"prod"'*) printf '{"branch":"prod","checks":"PENDING","build":"99"}\n' ;;
  *) exit 3 ;;
esac
`
	if err := os.WriteFile(hook, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.StatusForgePreview()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Repos {
		if row.Dir == "harness" {
			if row.Tip == nil || row.Tip.Build == nil || *row.Tip.Build != "123" || row.Tip.Checks == nil || *row.Tip.Checks != "SUCCESS" {
				t.Fatalf("tip build missing: %+v", row.Tip)
			}
			if row.Prod == nil || row.Prod.Branch != "prod" || row.Prod.Checks == nil || *row.Prod.Checks != "PENDING" {
				t.Fatalf("production build missing: %+v", row.Prod)
			}
			return
		}
	}
	t.Fatal("harness row missing")
}

func TestStatusBuildHookRejectsWrongBranch(t *testing.T) {
	c := newWorkspaceFixture(t)
	hook := filepath.Join(c.Harness, "hooks", "wtc", "status.build.sh")
	if err := os.MkdirAll(filepath.Dir(hook), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf '{\"branch\":\"other\",\"checks\":\"SUCCESS\"}\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	statusRequireBuildError(t, c, "different branch")
}

func TestStatusBuildHookRejectsUnsafeURL(t *testing.T) {
	c := newWorkspaceFixture(t)
	hook := filepath.Join(c.Harness, "hooks", "wtc", "status.build.sh")
	if err := os.MkdirAll(filepath.Dir(hook), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf '{\"checks\":\"SUCCESS\",\"url\":\"javascript:alert(1)\"}\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	statusRequireBuildError(t, c, "invalid build URL")
}

func TestStatusBuildHookSkipsUnregisteredWorktree(t *testing.T) {
	c := newWorkspaceFixture(t)
	hook := filepath.Join(c.Harness, "hooks", "wtc", "status.build.sh")
	if err := os.MkdirAll(filepath.Dir(hook), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf '{\"checks\":\"SUCCESS\"}\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	snapshot := StatusSnapshot{Repos: []StatusRepo{{Repo: "extra", Worktree: c.Harness}}}
	if err := c.statusBuildFacts(&snapshot); err != nil || snapshot.Repos[0].Tip != nil {
		t.Fatalf("unregistered row blocked status: %+v %v", snapshot, err)
	}
}

// A provider failure marks its own cell and leaves the rest of the refresh.
func statusRequireBuildError(t *testing.T, c *Context, want string) {
	t.Helper()
	snapshot, err := c.StatusForgePreview()
	if err != nil {
		t.Fatalf("a provider failure aborted the refresh: %v", err)
	}
	for _, row := range snapshot.Repos {
		if row.Dir == "harness" {
			build := statusSoleBuild(row)
			if build == nil || build.Checks == nil || *build.Checks != "ERROR" || !strings.Contains(build.Error, want) {
				t.Fatalf("build should carry the error %q: %+v", want, build)
			}
			return
		}
	}
	t.Fatal("harness row missing")
}
