package openclaw

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func identityGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func identityClone(t *testing.T, state, group, name, remote string) string {
	t.Helper()
	dir := filepath.Join(state, "projects", group, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	identityGit(t, dir, "init", "-q")
	identityGit(t, dir, "remote", "add", "origin", remote)
	return dir
}

func identityAliases(t *testing.T, dir string, aliases ...string) {
	t.Helper()
	origin := strings.TrimSpace(identityGit(t, dir, "remote", "get-url", "origin"))
	data, _ := json.Marshal(map[string]any{"version": 1, "origin": origin, "repositoryId": "verified-test-id", "aliases": aliases})
	if err := os.WriteFile(filepath.Join(dir, ".git", "wtc-origin-aliases.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayProjectRenameKeepsIdentityAndLookupReadOnly(t *testing.T) {
	state := t.TempDir()
	dir := identityClone(t, state, "old-hash", "widget", "https://github.com/example-org/gadget.git")
	old := "git@github.com:example-org/widget.git"
	if got := GatewayProjectDir(state, ParseOrigin(old)); got != "" {
		t.Fatalf("unverified old URL guessed: %s", got)
	}
	identityAliases(t, dir, old)
	config := filepath.Join(dir, ".git", "config")
	before, _ := os.ReadFile(config)
	for _, remote := range []string{old, "https://github.com/example-org/gadget", "ssh://git@github.com:22/example-org/widget.git"} {
		if got := GatewayProjectDir(state, ParseOrigin(remote)); got != dir {
			t.Errorf("%s resolved to %q, want original clone", remote, got)
		}
		if got := GatewayProject(state, ParseOrigin(remote)); got != "widget" {
			t.Errorf("project ID changed to %q", got)
		}
		if !CheckoutOf(dir, remote) {
			t.Errorf("checkout no longer recognized for %s", remote)
		}
	}
	for _, name := range []string{"widget", "gadget"} {
		if got := GatewayProjectDirByName(state, name); got != dir {
			t.Errorf("%s: %q", name, got)
		}
	}
	for _, remote := range []string{"https://github.com/other-org/widget", "https://bitbucket.org/example-org/widget", "https://github.com/example-org/elsewhere"} {
		if got := GatewayProjectDir(state, ParseOrigin(remote)); got != "" {
			t.Errorf("unverified identity resolved: %s", remote)
		}
	}
	for _, name := range []string{"*", "../widget", ".", ".."} {
		if GatewayProjectDirByName(state, name) != "" || GatewayProjectDir(state, Origin{ForgeGitHub, "example-org", name}) != "" {
			t.Errorf("unsafe name resolved: %s", name)
		}
	}
	after, _ := os.ReadFile(config)
	if string(before) != string(after) {
		t.Fatal("discovery rewrote Git configuration")
	}
}

func TestGatewayProjectRenameRejectsAmbiguityAndUnverifiedTransfers(t *testing.T) {
	state := t.TempDir()
	a := identityClone(t, state, "a", "widget", "https://github.com/new-team/gadget.git")
	identityClone(t, state, "b", "other-project", "https://github.com/other-team/gadget.git")
	if got := GatewayProjectDirByName(state, "gadget"); got != "" {
		t.Fatalf("ambiguous short name chose %s", got)
	}
	old := "https://github.com/old-team/widget.git"
	if got := GatewayProjectDir(state, ParseOrigin(old)); got != "" {
		t.Fatalf("unverified owner transfer guessed: %s", got)
	}
	identityAliases(t, a, old)
	if got := GatewayProjectDir(state, ParseOrigin(old)); got != a {
		t.Fatalf("explicit verified transfer not retained: %s", got)
	}
	identityClone(t, state, "c", "second-copy", "https://github.com/new-team/gadget.git")
	if got := GatewayProjectDir(state, ParseOrigin("https://github.com/new-team/gadget.git")); got != "" {
		t.Fatalf("duplicate identity chose %s", got)
	}
}

func TestGatewayProjectOriginAliasRecordCannotFollowARepoint(t *testing.T) {
	state := t.TempDir()
	dir := identityClone(t, state, "a", "widget", "https://github.com/example-org/gadget.git")
	old := "https://github.com/example-org/widget.git"
	identityAliases(t, dir, old)
	if err := untrustedConfig(dir); err != nil {
		t.Fatalf("alias metadata changed Git trust policy: %v", err)
	}
	identityGit(t, dir, "remote", "set-url", "origin", "https://github.com/example-org/unrelated.git")
	if GatewayProjectDir(state, ParseOrigin(old)) != "" {
		t.Fatal("stale identity record followed a different origin")
	}
}

func TestGatewayProjectCurrentOriginWinsOverOldDirectoryAlias(t *testing.T) {
	state := t.TempDir()
	oldURL := "https://github.com/example-org/widget.git"
	renamed := identityClone(t, state, "a", "widget", "https://github.com/example-org/gadget.git")
	identityAliases(t, renamed, oldURL)
	reused := identityClone(t, state, "b", "reused", "https://github.com/example-org/widget.git")
	if got := GatewayProjectDir(state, ParseOrigin(oldURL)); got != reused {
		t.Fatalf("alias hid current repository: %s", got)
	}
	identityClone(t, state, "c", "widget-copy", oldURL)
	if got := GatewayProjectDir(state, ParseOrigin(oldURL)); got != "" {
		t.Fatalf("duplicate current origins chose %s", got)
	}
}
