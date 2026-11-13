package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := gitOutput(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fixtureFile(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func newWorkspaceFixture(t *testing.T) *Context {
	t.Helper()
	// The fixture has the same shared-bare geometry as an actual workspace but
	// all remotes are local, so a fetch never reaches the network.
	t.Setenv("PATH", fixtureSystemPath(t))
	t.Setenv("WTC_HARNESS_REPO", "agent-harness")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".bare"), 0755); err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, name := range []string{"agent-harness", "widget"} {
		source := filepath.Join(root, "source-"+name)
		if err := os.Mkdir(source, 0755); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, "-C", source, "init", "-q", "-b", "main")
		fixtureFile(t, filepath.Join(source, "README.md"), name+"\n", 0644)
		sources[name] = source
	}
	harness := sources["agent-harness"]
	registry := "repos:\n" +
		"  - name: agent-harness\n    remote: " + harness + "\n    default_ref: origin/main\n" +
		"  - name: widget\n    remote: " + sources["widget"] + "\n    default_ref: origin/main\n    port_offset: 1\n    issues_prefix: wid-\n"
	fixtureFile(t, filepath.Join(harness, ".harness-repos.yml"), registry, 0644)
	fixtureFile(t, filepath.Join(harness, ".wtc-cli-version"), "0.1.9\n", 0644)
	fixtureFile(t, filepath.Join(harness, "collection-AGENTS.md"), "# Fixture\n", 0644)
	fixtureFile(t, filepath.Join(harness, ".gitignore"), "harness-secret.txt\n", 0644)
	fixtureFile(t, filepath.Join(sources["widget"], ".gitignore"), "secrets.txt\n", 0644)
	fixtureFile(t, filepath.Join(sources["widget"], ".harness", "init.sh"), "#!/bin/sh\nprintf '%s|%s|%s' \"$WTC_COLLECTION\" \"$WTC_CONFIG_ROOT\" \"$WIDGET_PORT\" > init-ran\nif [ -L secrets.txt ]; then printf '|secret-present' >> init-ran; fi\n", 0755)
	for name, source := range sources {
		fixtureGit(t, "-C", source, "add", "-A")
		fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
		bare := filepath.Join(root, ".bare", name+".git")
		fixtureGit(t, "init", "-q", "--bare", bare)
		fixtureGit(t, "--git-dir="+bare, "remote", "add", "origin", source)
		fixtureGit(t, "--git-dir="+bare, "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
	}
	main := filepath.Join(root, "main")
	if err := os.Mkdir(main, 0755); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "--git-dir="+filepath.Join(root, ".bare", "agent-harness.git"), "worktree", "add", "-q", "--detach", filepath.Join(main, "harness"), "origin/main")
	c, err := OpenCollection(main)
	if err != nil {
		t.Fatal(err)
	}
	c.ConfigRoot = filepath.Join(root, "control")
	return c
}

func TestNewCollectionCreatesDetachedSiblingsAndGeneratedSurfaces(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureFile(t, filepath.Join(c.ConfigRoot, "widget", "secrets.txt"), "synthetic fixture\n", 0600)
	fixtureFile(t, filepath.Join(c.ConfigRoot, "agent-harness", "harness-secret.txt"), "synthetic fixture\n", 0600)
	r, err := c.NewCollection(NewOptions{Slug: "demo", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.IntendedBranch != "demo" || !strings.HasSuffix(r.Collection, "/demo") {
		t.Fatalf("unexpected result: %+v", r)
	}
	for _, name := range []string{"harness", "widget"} {
		worktree := filepath.Join(r.Collection, name)
		if ref := fixtureGit(t, "-C", worktree, "rev-parse", "--abbrev-ref", "HEAD"); ref != "HEAD" {
			t.Errorf("%s should be detached, got %q", name, ref)
		}
	}
	for _, path := range []string{".env.collection", ".env.collection.local", "mise.toml", "HANDOFF.md", "WTC-SCOPE.md", "AGENTS.md", "widget/init-ran"} {
		if _, err := os.Stat(filepath.Join(r.Collection, path)); err != nil {
			t.Errorf("missing %s: %v", path, err)
		}
	}
	init, err := os.ReadFile(filepath.Join(r.Collection, "widget", "init-ran"))
	if err != nil || string(init) != "demo|"+c.ConfigRoot+"|42001|secret-present" {
		t.Errorf("init hook received %q, error %v", init, err)
	}
	if info, err := os.Lstat(filepath.Join(r.Collection, "widget", "secrets.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("secret link missing before init: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(r.Collection, "harness", "harness-secret.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("harness secret link missing: %v", err)
	}
	target, err := OpenCollection(r.Collection)
	if err != nil {
		t.Fatal(err)
	}
	target.ConfigRoot = c.ConfigRoot
	if linked, err := target.LinkSecrets(SecretLinkOptions{Repo: "harness"}); err != nil || linked.Current != 1 {
		t.Errorf("harness alias did not select its control directory: %+v %v", linked, err)
	}
	env, err := os.ReadFile(filepath.Join(r.Collection, ".env.collection"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"COLLECTION_PORT_BASE=42000", "WIDGET_PORT=42001", "WTC_CONFIG_ROOT='" + c.ConfigRoot + "'"} {
		if !strings.Contains(string(env), expected) {
			t.Errorf("generated env lacks %q", expected)
		}
	}
	other, err := c.NewCollection(NewOptions{Issue: "wid-7", Slug: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Repositories) != 2 || other.Repositories[1] != "widget" {
		t.Fatalf("issue primary was not included: %+v", other)
	}
	env, _ = os.ReadFile(filepath.Join(other.Collection, ".env.collection"))
	if !strings.Contains(string(env), "COLLECTION_PORT_BASE=42100") {
		t.Errorf("second collection did not receive distinct port block: %s", env)
	}
}

func TestNewCollectionRejectsInvalidNameBeforeWriting(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.NewCollection(NewOptions{Slug: "../escape", Repos: []string{"widget"}}); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := c.NewCollection(NewOptions{PR: "agent-harness#14", Branch: "other"}); err == nil || !strings.Contains(err.Error(), "cannot override") {
		t.Fatalf("conflicting PR branch was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "escape")); !os.IsNotExist(err) {
		t.Fatalf("unexpected destination created: %v", err)
	}
}

func TestNewCollectionExplicitBranchLaunchNote(t *testing.T) {
	c := newWorkspaceFixture(t)
	branch := "x;echo${IFS}pwned"
	r, err := c.NewCollection(NewOptions{Slug: "assigned", Repos: []string{"widget"}, Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"harness", "widget"} {
		if got := fixtureGit(t, "-C", filepath.Join(r.Collection, name), "branch", "--show-current"); got != branch {
			t.Errorf("%s branch = %q", name, got)
		}
	}
	note, err := os.ReadFile(filepath.Join(r.Collection, "HANDOFF.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(note), "git switch -c") || !strings.Contains(string(note), "already on the explicitly requested branch") {
		t.Fatalf("incorrect launch note: %s", note)
	}
}

func TestNewCollectionPreHookCanVetoBeforeCreatingDirectory(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureFile(t, filepath.Join(c.Harness, "hooks", "wtc", "new.pre.sh"), "#!/bin/sh\nexit 19\n", 0755)
	if _, err := c.NewCollection(NewOptions{Slug: "vetoed", Repos: []string{"widget"}}); err == nil || !strings.Contains(err.Error(), "new.pre") {
		t.Fatalf("pre-hook did not veto: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "vetoed")); !os.IsNotExist(err) {
		t.Fatalf("vetoed collection exists: %v", err)
	}
}

func TestPRWorktreeFetchesExactHeadAndRefusesMissingHead(t *testing.T) {
	c := newWorkspaceFixture(t)
	source := filepath.Join(c.Workspace, "source-widget")
	fixtureGit(t, "-C", source, "checkout", "-qb", "fork-feature")
	fixtureFile(t, filepath.Join(source, "feature.txt"), "review change\n", 0644)
	fixtureGit(t, "-C", source, "add", "feature.txt")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "PR head")
	head := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureGit(t, "-C", source, "update-ref", "refs/pull/12/head", head)
	fork := filepath.Join(c.Workspace, "fork-widget.git")
	fixtureGit(t, "clone", "-q", "--bare", source, fork)
	fixtureGit(t, "-C", source, "checkout", "-q", "main")
	fixtureGit(t, "-C", source, "branch", "-D", "fork-feature")
	destination := filepath.Join(c.Workspace, "review")
	if err := os.Mkdir(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddPRWorktree("widget", "widget", destination, "fork-feature", "13", head, fork); err == nil {
		t.Fatal("missing PR ref was accepted")
	}
	if _, err := os.Stat(filepath.Join(destination, "widget")); !os.IsNotExist(err) {
		t.Fatalf("missing PR ref created a worktree: %v", err)
	}
	checkout, err := c.AddPRWorktree("widget", "widget", destination, "fork-feature", "12", head, fork)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.LocalBranch != "fork-feature" || checkout.PushRemote != "wtc-pr-12" {
		t.Fatalf("unexpected first checkout: %+v", checkout)
	}
	if got := fixtureGit(t, "-C", filepath.Join(destination, "widget"), "rev-parse", "HEAD"); got != head {
		t.Fatalf("PR worktree at %s, want %s", got, head)
	}
	if got := fixtureGit(t, "-C", filepath.Join(destination, "widget"), "branch", "--show-current"); got != "fork-feature" {
		t.Fatalf("PR head branch = %q", got)
	}
	if got := fixtureGit(t, "-C", filepath.Join(destination, "widget"), "rev-parse", "--abbrev-ref", "@{u}"); got != "wtc-pr-12/fork-feature" {
		t.Fatalf("fork PR upstream = %q", got)
	}
	second := filepath.Join(c.Workspace, "review-again")
	if err := os.Mkdir(second, 0755); err != nil {
		t.Fatal(err)
	}
	checkout, err = c.AddPRWorktree("widget", "widget", second, "fork-feature", "12", head, fork)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.LocalBranch != "wtc-pr-12-review" || checkout.PushRemote != "wtc-pr-12" {
		t.Fatalf("unexpected second checkout: %+v", checkout)
	}
	if got := fixtureGit(t, "-C", filepath.Join(second, "widget"), "rev-parse", "HEAD"); got != head {
		t.Fatalf("second checkout at %s, want %s", got, head)
	}
	bare := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+bare, "worktree", "remove", filepath.Join(destination, "widget"))
	fixtureGit(t, "--git-dir="+bare, "branch", "-f", "fork-feature", "origin/main")
	third := filepath.Join(c.Workspace, "review-after-stale")
	if err := os.Mkdir(third, 0755); err != nil {
		t.Fatal(err)
	}
	checkout, err = c.AddPRWorktree("widget", "widget", third, "fork-feature", "12", head, fork)
	if err != nil || checkout.LocalBranch != "wtc-pr-12-review-2" {
		t.Fatalf("stale local branch prevented exact checkout: %+v %v", checkout, err)
	}
	if got := fixtureGit(t, "-C", filepath.Join(third, "widget"), "rev-parse", "HEAD"); got != head {
		t.Fatalf("checkout after stale branch at %s, want %s", got, head)
	}
}

func TestNewCollectionCanReviewHarnessPR(t *testing.T) {
	c := newWorkspaceFixture(t)
	source := filepath.Join(c.Workspace, "source-agent-harness")
	branch := "x;echo${IFS}pwned"
	fixtureGit(t, "-C", source, "checkout", "-qb", branch)
	fixtureFile(t, filepath.Join(source, "review.txt"), "review change\n", 0644)
	fixtureGit(t, "-C", source, "add", "review.txt")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "PR head")
	head := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureGit(t, "-C", source, "update-ref", "refs/pull/14/head", head)
	fixtureGit(t, "-C", source, "checkout", "-q", "main")
	c.Registry.Repos[0].Remote = "https://github.com/example/agent-harness.git"
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' '{\"headRefName\":\""+branch+"\",\"headRefOid\":\""+head+"\",\"title\":\"Review fixture\"}'\n", 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	r, err := c.NewCollection(NewOptions{PR: "agent-harness#14"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fixtureGit(t, "-C", filepath.Join(r.Collection, "harness"), "rev-parse", "HEAD"); got != head {
		t.Fatalf("harness PR at %s, want %s", got, head)
	}
	if got := fixtureGit(t, "-C", filepath.Join(r.Collection, "harness"), "branch", "--show-current"); got != branch {
		t.Fatalf("harness PR branch = %q", got)
	}
	if got := fixtureGit(t, "-C", filepath.Join(r.Collection, "harness"), "rev-parse", "--abbrev-ref", "@{u}"); got != "origin/"+branch {
		t.Fatalf("harness PR upstream = %q", got)
	}
	note, err := os.ReadFile(filepath.Join(r.Collection, "HANDOFF.md"))
	if err != nil || !strings.Contains(string(note), "git -C 'harness' push 'origin' 'HEAD:refs/heads/"+branch+"'") {
		t.Fatalf("review launch note lacks push target: %s (%v)", note, err)
	}
	target, err := OpenCollection(r.Collection)
	if err != nil {
		t.Fatal(err)
	}
	prs, err := target.ListPRs()
	if err != nil || len(prs) != 1 || prs[0].Number != "14" || prs[0].Branch != branch || prs[0].Title != "Review fixture" {
		t.Fatalf("review PR was not enlisted: %+v %v", prs, err)
	}
}
