package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitHarnessPin changes the pin in the synthetic harness origin and fetches
// the shared owner, like a merged upgrade arriving with catch-up.
func commitHarnessPin(t *testing.T, c *Context, version string) {
	t.Helper()
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	source := fixtureGit(t, "--git-dir="+owner, "remote", "get-url", "origin")
	fixtureFile(t, filepath.Join(source, ".wtc-cli-version"), version+"\n", 0644)
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qam", "pin "+version)
	fixtureGit(t, "--git-dir="+owner, "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
}

// divergeCollectionHarness moves a collection's own harness checkout to a
// local commit with another pin, as a collection on an older or unmerged
// harness revision would be.
func divergeCollectionHarness(t *testing.T, c *Context, version string) {
	t.Helper()
	fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), version+"\n", 0644)
	fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qam", "local pin "+version)
}

func syncFromCollection(t *testing.T, c *Context, opt RootSyncOptions) (RootSelectionReport, error) {
	t.Helper()
	harness, err := c.RootSelectionHarness()
	if err != nil {
		t.Fatal(err)
	}
	opt.Root, opt.Harness = c.Workspace, harness
	return SyncRootSelection(opt)
}

func readRoot(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, RootSelectionFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRootSelectionFollowsProjectSourceNotCollectionCheckout(t *testing.T) {
	c := newWorkspaceFixture(t)
	divergeCollectionHarness(t, c, "0.1.8")
	report, err := syncFromCollection(t, c, RootSyncOptions{Create: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Changed || report.State != RootSelectionCurrent || report.Source.Version != "0.1.9" || report.Source.Pin != ".wtc-cli-version" {
		t.Fatalf("unexpected report: %+v source %+v", report, report.Source)
	}
	ownerTip := fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "agent-harness.git"), "rev-parse", "origin/main")
	if report.Source.Commit != ownerTip {
		t.Fatalf("root selection read %s, want the owner's default ref %s", report.Source.Commit, ownerTip)
	}
	content := readRoot(t, c.Workspace)
	if !strings.Contains(content, `"`+cliMiseTool+`" = "0.1.9"`) {
		t.Fatalf("root selection does not select the project pin:\n%s", content)
	}
	// The root receives the CLI selection only: no collection environment,
	// ports, secrets or dev-build state.
	for _, leaked := range []string{"[env]", ".env.collection", "PORT", "CONFIG_ROOT", c.Collection, "dev"} {
		if strings.Contains(content, leaked) {
			t.Errorf("root selection contains collection state %q:\n%s", leaked, content)
		}
	}
	collection := c.CollectionSelection(report.Source)
	if collection.Version != "0.1.8" || !collection.Differs {
		t.Fatalf("collection selection not distinguished: %+v", collection)
	}
	mise, err := c.RenderMise()
	if err != nil || !strings.Contains(string(mise), `"0.1.8"`) {
		t.Fatalf("collection lost its own selection: %s %v", mise, err)
	}
}

func TestRootSelectionMissingStaleAndRefresh(t *testing.T) {
	c := newWorkspaceFixture(t)
	report, err := syncFromCollection(t, c, RootSyncOptions{Check: true})
	if err != nil || report.State != RootSelectionMissing || report.Changed || report.Source == nil {
		t.Fatalf("missing selection not reported: %+v %v", report, err)
	}
	// Generating a collection environment never creates the root file.
	if err := c.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(c.Workspace, RootSelectionFile)); !os.IsNotExist(err) {
		t.Fatalf("environment generation created a root selection: %v", err)
	}
	if _, err := syncFromCollection(t, c, RootSyncOptions{Create: true, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(c.Workspace, ".mise")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote: %v", err)
	}
	if _, err := syncFromCollection(t, c, RootSyncOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	again, err := syncFromCollection(t, c, RootSyncOptions{Create: true})
	if err != nil || again.Changed || again.State != RootSelectionCurrent {
		t.Fatalf("second sync was not a no-op: %+v %v", again, err)
	}

	commitHarnessPin(t, c, "0.1.10")
	stale, err := SyncRootSelection(RootSyncOptions{Root: c.Workspace, Check: true})
	if err != nil || stale.State != RootSelectionStale || stale.Recorded.Version != "0.1.9" || stale.Source.Version != "0.1.10" {
		t.Fatalf("stale selection not reported: %+v %v", stale, err)
	}
	// A collection on another harness revision refreshes the existing file
	// from the recorded source; it cannot redirect it to its own checkout.
	divergeCollectionHarness(t, c, "0.1.8")
	if err := c.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	if content := readRoot(t, c.Workspace); !strings.Contains(content, `= "0.1.10"`) {
		t.Fatalf("existing root selection was not refreshed from the project source:\n%s", content)
	}
}

func TestRootSelectionPreservesUserConfiguration(t *testing.T) {
	c := newWorkspaceFixture(t)
	user := "[tools]\n\"" + cliMiseTool + "\" = \"9.9.9\"\n"
	fixtureFile(t, filepath.Join(c.Workspace, "mise.toml"), user, 0644)
	report, err := syncFromCollection(t, c, RootSyncOptions{Create: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Overrides) != 1 || report.Overrides[0] != "mise.toml" {
		t.Fatalf("user root selection not reported as an override: %+v", report.Overrides)
	}
	if data, _ := os.ReadFile(filepath.Join(c.Workspace, "mise.toml")); string(data) != user {
		t.Fatalf("user root configuration changed: %q", data)
	}

	// A file at the generated path that taba did not write is the user's.
	other := newWorkspaceFixture(t)
	owned := "[tools]\nnode = \"22\"\n"
	fixtureFile(t, filepath.Join(other.Workspace, RootSelectionFile), owned, 0644)
	if _, err := syncFromCollection(t, other, RootSyncOptions{Create: true}); err == nil || !strings.Contains(err.Error(), "not generated by taba") {
		t.Fatalf("conflict not refused: %v", err)
	}
	check, err := syncFromCollection(t, other, RootSyncOptions{Check: true})
	if err != nil || check.State != RootSelectionConflict {
		t.Fatalf("conflict not reported by check: %+v %v", check, err)
	}
	if err := other.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	if got := readRoot(t, other.Workspace); got != owned {
		t.Fatalf("user file changed: %q", got)
	}
}

// commitHarnessFiles commits files to the synthetic harness origin on branch
// and fetches the shared owner.
func commitHarnessFiles(t *testing.T, c *Context, branch string, files map[string]string, remove ...string) {
	t.Helper()
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	source := fixtureGit(t, "--git-dir="+owner, "remote", "get-url", "origin")
	if current := fixtureGit(t, "-C", source, "branch", "--show-current"); current != branch {
		if _, err := gitOutput("-C", source, "switch", "-q", branch); err != nil {
			fixtureGit(t, "-C", source, "switch", "-qc", branch)
		}
	}
	for name, data := range files {
		fixtureFile(t, filepath.Join(source, name), data, 0644)
	}
	for _, name := range remove {
		fixtureGit(t, "-C", source, "rm", "-q", name)
	}
	fixtureGit(t, "-C", source, "add", "-A")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "change "+branch)
	fixtureGit(t, "--git-dir="+owner, "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
}

func setHarnessDefaultRef(t *testing.T, c *Context, branch, ref string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.Harness, ".harness-repos.yml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	inHarness := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "- name:") {
			inHarness = strings.Contains(line, "agent-harness")
		}
		if inHarness && strings.Contains(line, "default_ref:") {
			lines[i] = "    default_ref: " + ref
		}
	}
	commitHarnessFiles(t, c, branch, map[string]string{".harness-repos.yml": strings.Join(lines, "\n")})
}

func TestRootSelectionFollowsDeclaredDefaultRef(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := syncFromCollection(t, c, RootSyncOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	// The project moves its development line: release declares itself and
	// main points at it. The root follows the declaration, not the old ref.
	commitHarnessFiles(t, c, "release", map[string]string{".wtc-cli-version": "0.1.11\n"})
	setHarnessDefaultRef(t, c, "release", "origin/release")
	setHarnessDefaultRef(t, c, "main", "origin/release")
	report, err := SyncRootSelection(RootSyncOptions{Root: c.Workspace})
	if err != nil || !report.Changed || report.Source.Ref != "origin/release" || report.Source.Version != "0.1.11" {
		t.Fatalf("declared default ref not followed: %+v %+v %v", report, report.Source, err)
	}
	// A declared ref that names yet another ref is inconsistent, not followed.
	setHarnessDefaultRef(t, c, "release", "origin/main")
	check, err := SyncRootSelection(RootSyncOptions{Root: c.Workspace, Check: true})
	if err != nil || check.Source != nil || !strings.Contains(check.SourceError, "inconsistent") {
		t.Fatalf("inconsistent declaration accepted: %+v", check)
	}
}

func TestRootSelectionRequiresAPinAtTheSource(t *testing.T) {
	c := newWorkspaceFixture(t)
	commitHarnessFiles(t, c, "main", nil, ".wtc-cli-version")
	if _, err := syncFromCollection(t, c, RootSyncOptions{Create: true}); err == nil || !strings.Contains(err.Error(), "pins no CLI release") {
		t.Fatalf("unpinned source accepted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(c.Workspace, ".mise")); !os.IsNotExist(err) {
		t.Fatalf("failed source left files: %v", err)
	}
}

func TestRootSelectionRejectsTamperedProvenance(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := syncFromCollection(t, c, RootSyncOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Workspace, RootSelectionFile)
	good := readRoot(t, c.Workspace)
	// Outside paths and owners that are not this project's harness are refused.
	for _, harness := range []string{"../escape", "widget", "absent"} {
		tampered := strings.Replace(good, `harness = "agent-harness"`, `harness = "`+harness+`"`, 1)
		fixtureFile(t, path, tampered, 0644)
		if _, err := SyncRootSelection(RootSyncOptions{Root: c.Workspace}); err == nil {
			t.Errorf("tampered owner %q accepted", harness)
		}
		if _, _, err := NewCollectionAtRoot(c.Workspace, NewOptions{Slug: "tampered"}); err == nil {
			t.Errorf("root creation accepted tampered owner %q", harness)
		}
		if _, err := os.Lstat(filepath.Join(c.Workspace, "tampered")); !os.IsNotExist(err) {
			t.Fatalf("tampered owner %q created files: %v", harness, err)
		}
	}
	// A symlinked owner that leaves the project is refused.
	outside := t.TempDir()
	fixtureGit(t, "init", "-q", "--bare", filepath.Join(outside, "linked.git"))
	if err := os.Symlink(filepath.Join(outside, "linked.git"), filepath.Join(c.Workspace, ".bare", "linked.git")); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncRootSelection(RootSyncOptions{Root: c.Workspace, Harness: "linked", Create: true}); err == nil {
		t.Error("symlinked owner accepted")
	}
	// A recorded ref, commit or version is provenance only: tampering makes
	// the file stale and a refresh restores the derived content.
	tampered := strings.Replace(strings.Replace(good, `ref = "origin/main"`, `ref = "origin/elsewhere"`, 1), `= "0.1.9"`, `= "9.9.9"`, 1)
	fixtureFile(t, path, tampered, 0644)
	check, err := SyncRootSelection(RootSyncOptions{Root: c.Workspace, Check: true})
	if err != nil || check.State != RootSelectionStale || check.Source.Ref != "origin/main" || check.Source.Version != "0.1.9" {
		t.Fatalf("tampered provenance not reported stale: %+v %+v %v", check, check.Source, err)
	}
	if _, err := SyncRootSelection(RootSyncOptions{Root: c.Workspace}); err != nil {
		t.Fatal(err)
	}
	if got := readRoot(t, c.Workspace); got != good {
		t.Fatalf("refresh did not restore derived content:\n%s", got)
	}
}

func TestRootSelectionIgnoresLocalUpgradePin(t *testing.T) {
	c := newWorkspaceFixture(t)
	// An upgrade writes the pin in this collection's harness without
	// committing. Neither that nor an unpushed commit is the project source.
	fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.1.12\n", 0644)
	var result UpgradeResult
	c.upgradeRootSelection(&result)
	if result.RootSelectionError != "" || result.RootSelection.Source.Version != "0.1.9" {
		t.Fatalf("local pin became the root authority: %+v %+v", result, result.RootSelection)
	}
	divergeCollectionHarness(t, c, "0.1.12")
	if err := c.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	if content := readRoot(t, c.Workspace); !strings.Contains(content, `= "0.1.9"`) {
		t.Fatalf("unpushed pin reached the root:\n%s", content)
	}
	// Once merged and fetched, the supported path (catch-up regenerating a
	// collection environment) refreshes the root.
	commitHarnessPin(t, c, "0.1.12")
	if err := c.EnsureEnvSupport(); err != nil {
		t.Fatal(err)
	}
	if content := readRoot(t, c.Workspace); !strings.Contains(content, `= "0.1.12"`) {
		t.Fatalf("merged pin did not refresh the root:\n%s", content)
	}
}

func TestUpgradeCreatesRootSelection(t *testing.T) {
	c := newWorkspaceFixture(t)
	var result UpgradeResult
	c.upgradeRootSelection(&result)
	if result.RootSelectionError != "" || result.RootSelection == nil || !result.RootSelection.Changed {
		t.Fatalf("upgrade did not create the root selection: %+v", result)
	}
	fixtureFile(t, filepath.Join(c.Workspace, RootSelectionFile), "[tools]\n", 0644)
	result = UpgradeResult{}
	c.upgradeRootSelection(&result)
	if !strings.Contains(result.RootSelectionError, "not generated by taba") {
		t.Fatalf("upgrade conflict not reported: %+v", result)
	}
}

func TestNewCollectionAtRootUsesProjectSource(t *testing.T) {
	c := newWorkspaceFixture(t)
	root := c.Workspace
	if _, _, err := NewCollectionAtRoot(root, NewOptions{Slug: "early"}); err == nil {
		t.Fatal("root creation without a recorded source accepted")
	}
	if _, err := syncFromCollection(t, c, RootSyncOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	divergeCollectionHarness(t, c, "0.1.8")
	mainHead := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
	commitHarnessPin(t, c, "0.1.10")

	if found, ok := FindProjectRoot(filepath.Join(root, ".bare")); !ok || !sameCanonicalPath(found, root) {
		t.Fatalf("project root not found: %q %v", found, ok)
	}
	result, creating, err := NewCollectionAtRoot(root, NewOptions{Slug: "demo", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if !sameCanonicalPath(result.Collection, filepath.Join(root, "demo")) || !sameCanonicalPath(creating.Collection, result.Collection) {
		t.Fatalf("unexpected collection: %+v", result)
	}
	ownerTip := fixtureGit(t, "--git-dir="+filepath.Join(root, ".bare", "agent-harness.git"), "rev-parse", "origin/main")
	if head := fixtureGit(t, "-C", filepath.Join(result.Collection, "harness"), "rev-parse", "HEAD"); head != ownerTip {
		t.Fatalf("new harness at %s, want project source %s", head, ownerTip)
	}
	if _, err := os.Stat(filepath.Join(result.Collection, "widget", "README.md")); err != nil {
		t.Fatalf("sibling missing: %v", err)
	}
	mise, _ := os.ReadFile(filepath.Join(result.Collection, "mise.toml"))
	if !strings.Contains(string(mise), `"0.1.10"`) {
		t.Fatalf("new collection does not select the project pin: %s", mise)
	}
	if head := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD"); head != mainHead {
		t.Fatalf("existing collection moved: %s", head)
	}
	// Creating the collection generated its environment, which refreshed the
	// generated root selection to the fetched project source.
	if content := readRoot(t, root); !strings.Contains(content, `= "0.1.10"`) {
		t.Fatalf("root selection not refreshed:\n%s", content)
	}
	if _, _, err := NewCollectionAtRoot(root, NewOptions{Slug: "demo"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing collection not refused: %v", err)
	}
}

func TestNewCollectionAtRootRemovesInterruptedCreation(t *testing.T) {
	c := newWorkspaceFixture(t)
	root := c.Workspace
	if _, err := syncFromCollection(t, c, RootSyncOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewCollectionAtRoot(root, NewOptions{Slug: "broken", Repos: []string{"not-registered"}}); err == nil {
		t.Fatal("unknown repository accepted")
	}
	if _, err := os.Lstat(filepath.Join(root, "broken")); !os.IsNotExist(err) {
		t.Fatalf("interrupted creation left a folder: %v", err)
	}
	worktrees := fixtureGit(t, "--git-dir="+filepath.Join(root, ".bare", "agent-harness.git"), "worktree", "list", "--porcelain")
	if strings.Contains(worktrees, filepath.Join("broken", "harness")) {
		t.Fatalf("interrupted creation left a registered worktree:\n%s", worktrees)
	}
	if _, _, err := NewCollectionAtRoot(root, NewOptions{Slug: "branched", Branch: "topic", Repos: []string{"not-registered"}}); err == nil {
		t.Fatal("unknown repository accepted")
	}
	if gitRefExists(filepath.Join(root, ".bare", "agent-harness.git"), "refs/heads/topic") {
		t.Fatal("interrupted creation left the branch it created")
	}
	if _, _, err := NewCollectionAtRoot(root, NewOptions{Slug: "bad/name"}); err == nil {
		t.Fatal("invalid name accepted")
	}
}

// Root and collection invocations take identical options and must create the
// same collection; only the creating configuration's location differs.
func TestNewCollectionAtRootMatchesCollectionInvocation(t *testing.T) {
	for _, opt := range []NewOptions{
		{Slug: "demo", Repos: []string{"widget"}},
		{Issue: "wid-7", Slug: "fix"},
		{Tracker: "TRK-3", Slug: "audit", Repos: []string{"harness", "widget"}},
		{Slug: "on-branch", Branch: "topic", Repos: []string{"widget"}},
	} {
		fromCollection := newWorkspaceFixture(t)
		want, err := fromCollection.NewCollection(opt)
		if err != nil {
			t.Fatalf("%+v from collection: %v", opt, err)
		}
		fromRoot := newWorkspaceFixture(t)
		if _, err := syncFromCollection(t, fromRoot, RootSyncOptions{Create: true}); err != nil {
			t.Fatal(err)
		}
		got, _, err := NewCollectionAtRoot(fromRoot.Workspace, opt)
		if err != nil {
			t.Fatalf("%+v from root: %v", opt, err)
		}
		if filepath.Base(got.Collection) != filepath.Base(want.Collection) || got.IntendedBranch != want.IntendedBranch ||
			got.Source != want.Source || strings.Join(got.Repositories, ",") != strings.Join(want.Repositories, ",") {
			t.Errorf("%+v: root result %+v differs from collection result %+v", opt, got, want)
		}
		for _, name := range []string{"harness", "widget"} {
			_, wantErr := os.Stat(filepath.Join(want.Collection, name))
			_, gotErr := os.Stat(filepath.Join(got.Collection, name))
			if (wantErr == nil) != (gotErr == nil) {
				t.Errorf("%+v: %s presence differs: collection %v, root %v", opt, name, wantErr, gotErr)
			}
		}
		for _, name := range []string{"harness", "widget"} {
			wantBranch, _ := gitOutput("-C", filepath.Join(want.Collection, name), "branch", "--show-current")
			gotBranch, _ := gitOutput("-C", filepath.Join(got.Collection, name), "branch", "--show-current")
			if wantBranch != gotBranch {
				t.Errorf("%+v: %s branch differs: collection %q, root %q", opt, name, wantBranch, gotBranch)
			}
		}
	}
}

func TestNewCollectionAtRootHonoursHarnessBase(t *testing.T) {
	opt := NewOptions{Slug: "based", Repos: []string{"widget"}, RepoBases: map[string]string{"harness": "origin/release"}, BaseReason: "synthetic release line"}
	setup := func() *Context {
		c := newWorkspaceFixture(t)
		commitHarnessFiles(t, c, "release", map[string]string{"RELEASE.md": "release line\n"})
		return c
	}
	fromCollection := setup()
	want, err := fromCollection.NewCollection(opt)
	if err != nil {
		t.Fatal(err)
	}
	fromRoot := setup()
	if _, err := syncFromCollection(t, fromRoot, RootSyncOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	got, _, err := NewCollectionAtRoot(fromRoot.Workspace, opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []NewResult{want, got} {
		if _, err := os.Stat(filepath.Join(r.Collection, "harness", "RELEASE.md")); err != nil {
			t.Errorf("%s harness is not at the requested base: %v", r.Collection, err)
		}
	}
	if _, _, err := NewCollectionAtRoot(fromRoot.Workspace, NewOptions{Slug: "nobase", RepoBases: map[string]string{"harness": "origin/absent"}, BaseReason: "synthetic"}); err == nil {
		t.Fatal("unavailable harness base accepted")
	}
	if _, err := os.Lstat(filepath.Join(fromRoot.Workspace, "nobase")); !os.IsNotExist(err) {
		t.Fatalf("unavailable base left a folder: %v", err)
	}
}

func TestNewCollectionAtRootRefusesHarnessPR(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := syncFromCollection(t, c, RootSyncOptions{Create: true}); err != nil {
		t.Fatal(err)
	}
	_, _, err := NewCollectionAtRoot(c.Workspace, NewOptions{PR: "agent-harness#5"})
	if err == nil || !strings.Contains(err.Error(), "created from a collection") {
		t.Fatalf("harness PR at the root not refused: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(c.Workspace, "agent-harness-pr5")); !os.IsNotExist(err) {
		t.Fatalf("refused PR left a folder: %v", err)
	}
}

func TestRootSelectionFindsANonMainDefaultBranch(t *testing.T) {
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	// A project whose development line is "develop": the bare owner's HEAD
	// names it, as a bare clone of such a forge repository does, and there is
	// no origin/main or origin/HEAD.
	setHarnessDefaultRef(t, c, "main", "origin/develop")
	source := fixtureGit(t, "--git-dir="+owner, "remote", "get-url", "origin")
	fixtureGit(t, "-C", source, "branch", "-m", "main", "develop")
	fixtureGit(t, "--git-dir="+owner, "fetch", "-q", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*")
	fixtureGit(t, "--git-dir="+owner, "symbolic-ref", "HEAD", "refs/heads/develop")
	if _, err := gitOutput("--git-dir="+owner, "rev-parse", "--verify", "origin/main"); err == nil {
		t.Fatal("fixture still has origin/main")
	}
	report, err := syncFromCollection(t, c, RootSyncOptions{Create: true})
	if err != nil || report.Source.Ref != "origin/develop" {
		t.Fatalf("non-main default branch not found: %+v %v", report, err)
	}
	fixtureGit(t, "--git-dir="+owner, "symbolic-ref", "HEAD", "refs/heads/absent")
	check, _ := SyncRootSelection(RootSyncOptions{Root: c.Workspace, Check: true})
	if !strings.Contains(check.SourceError, "cannot find the harness default branch") {
		t.Fatalf("unhelpful probe error: %+v", check)
	}
}
