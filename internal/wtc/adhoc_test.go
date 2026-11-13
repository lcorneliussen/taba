package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// adHocFixture isolates the per-user owner cache, registry and control root,
// and returns a directory holding synthetic source repositories.
func adHocFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("PATH", fixtureSystemPath(t))
	t.Setenv("WTC_HARNESS_REPO", "")
	t.Setenv("WTC_CONFIG_ROOT", "")
	root := t.TempDir()
	t.Setenv("WTC_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	return root
}

func adHocSource(t *testing.T, root, name string, branches ...string) string {
	t.Helper()
	source := filepath.Join(root, "src", name)
	fixtureFile(t, filepath.Join(source, "README.md"), name+"\n", 0644)
	fixtureGit(t, "-C", source, "init", "-q", "-b", "main")
	commit := func(message string) {
		fixtureGit(t, "-C", source, "add", "-A")
		fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", message)
	}
	commit("main")
	for _, branch := range branches {
		fixtureGit(t, "-C", source, "switch", "-q", "-c", branch)
		fixtureFile(t, filepath.Join(source, branch+".txt"), branch+"\n", 0644)
		commit(branch)
		fixtureGit(t, "-C", source, "switch", "-q", "main")
	}
	return source
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func initAdHoc(t *testing.T, dir string) *Context {
	t.Helper()
	if _, err := InitAdHoc(dir); err != nil {
		t.Fatal(err)
	}
	c, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.AdHoc {
		t.Fatalf("expected an ad-hoc collection: %+v", c)
	}
	return c
}

func addAdHoc(t *testing.T, c *Context, opt AdHocRepoOptions) Repo {
	t.Helper()
	repo, err := c.RegisterAdHocRepo(opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddRepositories(AddRepoOptions{Repos: []string{repo.Name}}); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestInitAdHocCreatesCollectionInAnEmptyFolder(t *testing.T) {
	root := adHocFixture(t)
	dir := filepath.Join(root, "task")
	c := initAdHoc(t, dir)
	if c.Collection != dir || c.Harness != filepath.Join(dir, ".wtc", "config") {
		t.Fatalf("unexpected geometry: %+v", c)
	}
	if c.ConfigRoot != filepath.Join(realPath(t, dir), ".config") {
		t.Fatalf("ad-hoc control root should be collection-local, got %s", c.ConfigRoot)
	}
	for _, path := range []string{"AGENTS.md", "WTC-SCOPE.md", ".env.collection", "mise.toml", ".wtc/skills/wtc-start/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	if harness, err := c.HarnessRepoName(); err != nil || harness != "" {
		t.Fatalf("ad-hoc collection has no harness repository: %q %v", harness, err)
	}
	nested := filepath.Join(dir, "sub")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if found, err := Discover(nested); err != nil || found.Collection != dir {
		t.Fatalf("discovery from a subfolder: %v %+v", err, found)
	}
	if _, err := InitAdHoc(nested); err == nil || !strings.Contains(err.Error(), "already inside a collection") {
		t.Fatalf("nested init was accepted: %v", err)
	}
	busy := filepath.Join(root, "busy")
	fixtureFile(t, filepath.Join(busy, "notes.txt"), "keep\n", 0644)
	if _, err := InitAdHoc(busy); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty folder was accepted: %v", err)
	}
	if _, err := Discover(filepath.Join(root, "src")); err == nil || !strings.Contains(err.Error(), "wtc init") {
		t.Fatalf("discovery outside a collection should name wtc init: %v", err)
	}
}

func TestAdHocAddRepoClonesIntoUserCacheAndRemembersIt(t *testing.T) {
	root := adHocFixture(t)
	source := adHocSource(t, root, "widget")
	remote := "file://" + source
	c := initAdHoc(t, filepath.Join(root, "first"))
	repo := addAdHoc(t, c, AdHocRepoOptions{Spec: remote})
	if repo.Name != "widget" || repo.DefaultRef != "origin/main" || repo.Owner != "" {
		t.Fatalf("unexpected registration: %+v", repo)
	}
	owner, err := c.bareFor("widget")
	if err != nil {
		t.Fatal(err)
	}
	if owner, err = filepath.EvalSymlinks(owner); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(owner, filepath.Join("data", "bare", "local")) {
		t.Fatalf("owner outside the per-user cache: %s", owner)
	}
	worktree := filepath.Join(c.Collection, "widget")
	if head := fixtureGit(t, "-C", worktree, "rev-parse", "--abbrev-ref", "HEAD"); head != "HEAD" {
		t.Fatalf("worktree should rest detached, got %s", head)
	}
	if common := fixtureGit(t, "-C", worktree, "rev-parse", "--path-format=absolute", "--git-common-dir"); realPath(t, common) != owner {
		t.Fatalf("worktree owner %s, want %s", common, owner)
	}
	// The next ad-hoc collection adds it by name, sharing the same owner.
	second := initAdHoc(t, filepath.Join(root, "second"))
	again := addAdHoc(t, second, AdHocRepoOptions{Spec: "widget"})
	if again.Remote != remote || again.DefaultRef != "origin/main" {
		t.Fatalf("per-user registry was not reused: %+v", again)
	}
	if common := fixtureGit(t, "-C", filepath.Join(second.Collection, "widget"), "rev-parse", "--path-format=absolute", "--git-common-dir"); realPath(t, common) != owner {
		t.Fatalf("second collection did not share the owner: %s", common)
	}
	// A different repository may not take a remembered name.
	other := adHocSource(t, root, "other")
	if _, err := second.RegisterAdHocRepo(AdHocRepoOptions{Spec: "file://" + other, Name: "widget"}); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("name collision was accepted: %v", err)
	}
	if _, err := second.RegisterAdHocRepo(AdHocRepoOptions{Spec: "unknown"}); err == nil || !strings.Contains(err.Error(), "clone URL") {
		t.Fatalf("unknown name was accepted: %v", err)
	}
}

func TestAdHocCheckoutOwnerStartsFromTargetNotItsBranch(t *testing.T) {
	root := adHocFixture(t)
	source := adHocSource(t, root, "service", "develop")
	checkout := filepath.Join(root, "checkout")
	fixtureGit(t, "clone", "-q", source, checkout)
	fixtureGit(t, "-C", checkout, "switch", "-q", "-c", "local-feature")
	c := initAdHoc(t, filepath.Join(root, "task"))
	var asked string
	repo := addAdHoc(t, c, AdHocRepoOptions{Spec: checkout, Ask: func(name, suggested string) (string, error) {
		asked = name + "@" + suggested
		return "develop", nil
	}})
	if asked != "service@main" {
		t.Fatalf("target prompt got %q", asked)
	}
	owner := fixtureGit(t, "-C", checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if repo.Owner != owner || repo.DefaultRef != "origin/develop" || repo.Remote != source {
		t.Fatalf("unexpected registration: %+v", repo)
	}
	worktree := filepath.Join(c.Collection, "service")
	want := fixtureGit(t, "-C", source, "rev-parse", "develop")
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != want {
		t.Fatalf("worktree at %s, want develop tip %s", got, want)
	}
	if branch := fixtureGit(t, "-C", checkout, "branch", "--show-current"); branch != "local-feature" {
		t.Fatalf("existing checkout was moved to %s", branch)
	}
	if _, err := c.RegisterAdHocRepo(AdHocRepoOptions{Spec: "service", Target: "main"}); err == nil {
		t.Fatal("re-registration silently changed the development tip")
	}
	if _, err := c.RegisterAdHocRepo(AdHocRepoOptions{Spec: "file://" + source, Name: "missing-branch", Target: "nope"}); err == nil || !strings.Contains(err.Error(), "no branch") {
		t.Fatalf("missing target branch was accepted: %v", err)
	}
}

func TestAdHocRegistryKeepsHandEditsAndLocalHooksRun(t *testing.T) {
	root := adHocFixture(t)
	c := initAdHoc(t, filepath.Join(root, "task"))
	registry := filepath.Join(c.Harness, "repos.yml")
	data, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, registry, string(data)+"  # hand-written note\n", 0644)
	marker := filepath.Join(root, "hook-ran")
	fixtureFile(t, filepath.Join(c.Harness, "hooks", "wtc", "add-repo.post.sh"), "#!/bin/sh\ncat > '"+marker+"'\n", 0755)
	source := adHocSource(t, root, "widget")
	addAdHoc(t, c, AdHocRepoOptions{Spec: "file://" + source, Target: "main"})
	updated, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "# hand-written note") || !strings.Contains(string(updated), "name: widget") {
		t.Fatalf("registry lost hand edits or the entry:\n%s", updated)
	}
	payload, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(payload), `"repositories":"widget"`) {
		t.Fatalf("collection-local hook did not run: %q %v", payload, err)
	}
}

func TestAdHocRefusesWorkspaceOperationsAndRetiresInPlace(t *testing.T) {
	root := adHocFixture(t)
	c := initAdHoc(t, filepath.Join(root, "task"))
	addAdHoc(t, c, AdHocRepoOptions{Spec: "file://" + adHocSource(t, root, "widget")})
	if _, _, err := c.CatchUpInventory(CatchUpOptions{All: true, DryRun: true}); err == nil {
		t.Fatal("an ad-hoc collection must not sweep its parent folder")
	}
	if _, err := c.AddRepositories(AddRepoOptions{Collection: "elsewhere", Repos: []string{"widget"}}); err == nil {
		t.Fatal("an ad-hoc collection has no sibling collections")
	}
	if _, err := c.RefreshRegistry(); err == nil {
		t.Fatal("an ad-hoc collection has no bare-owner map")
	}
	owner, err := c.bareFor("widget")
	if err != nil {
		t.Fatal(err)
	}
	// Generated catch-up reports must not keep the folder alive.
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-catch-up.json"), "{}\n", 0644)
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-catch-up.json.md"), "# report\n", 0644)
	result, err := c.RetireCollection(RetireOptions{Name: "task", Self: true, InPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.FolderRemoved {
		t.Fatalf("collection folder remains: %+v", result)
	}
	if _, err := os.Stat(owner); err != nil {
		t.Fatalf("retire removed the shared owner: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "repos.yml")); err != nil {
		t.Fatalf("retire removed the per-user registry: %v", err)
	}
}

func TestRemoteKeySeparatesHostsAndRejectsTraversal(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:acme/api.git":          "github.com/acme/api",
		"https://github.com/acme/api":          "github.com/acme/api",
		"ssh://git@git.example:22/x/api.git":   "git.example/x/api",
		"ssh://git@git.example:2222/x/api.git": "git.example_2222/x/api",
		"https://git.example:8443/x/api":       "git.example_8443/x/api",
		"/srv/git/api.git":                     "local/srv/git/api",
	} {
		key, err := remoteKey(remote)
		if err != nil || strings.Join(key, "/") != want {
			t.Fatalf("%s: got %v %v, want %s", remote, key, err, want)
		}
	}
	for _, remote := range []string{"https://example.com/../x", "api", "git@host:"} {
		if _, err := remoteKey(remote); err == nil {
			t.Fatalf("accepted %q", remote)
		}
	}
}

func TestUserRegistryKeepsConcurrentRegistrations(t *testing.T) {
	t.Setenv("WTC_DATA_HOME", t.TempDir())
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- saveUserRegistry(Repo{Name: fmt.Sprintf("repo%d", i), Remote: fmt.Sprintf("https://example.invalid/repo%d.git", i), DefaultRef: "origin/main"})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := loadUserRegistry()
	if err != nil || len(r.Repos) != 16 {
		t.Fatalf("kept %d of 16 registrations: %v", len(r.Repos), err)
	}
}

func TestAdHocCatchUpStatusAndRenderedLinks(t *testing.T) {
	root := adHocFixture(t)
	source := adHocSource(t, root, "widget")
	c := initAdHoc(t, filepath.Join(root, "task"))
	fixtureFile(t, filepath.Join(c.Harness, "skills", "local-skill", "SKILL.md"), "---\nname: local-skill\n---\n", 0644)
	fixtureFile(t, filepath.Join(c.Harness, "instructions", "secrets.md"), "# Local secrets policy\n", 0644)
	addAdHoc(t, c, AdHocRepoOptions{Spec: "file://" + source})
	for _, path := range []string{".claude/skills/local-skill/SKILL.md", ".wtc/instructions/secrets.md"} {
		data, err := os.ReadFile(filepath.Join(c.Collection, path))
		if err != nil {
			t.Fatalf("rendered link %s does not resolve: %v", path, err)
		}
		if want := map[string]string{".claude/skills/local-skill/SKILL.md": "---\nname: local-skill\n---\n", ".wtc/instructions/secrets.md": "# Local secrets policy\n"}[path]; string(data) != want {
			t.Fatalf("%s does not link to its override: %q", path, data)
		}
	}
	if _, err := c.RegisterAdHocRepo(AdHocRepoOptions{Spec: "file://" + source, Target: "other"}); err == nil {
		t.Fatal("re-adding by URL silently ignored a different target")
	}
	fixtureFile(t, filepath.Join(source, "next.txt"), "next\n", 0644)
	fixtureGit(t, "-C", source, "add", "-A")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "next")
	report, err := c.CatchUp(CatchUpOptions{})
	if err != nil {
		t.Fatalf("catch-up failed: %v\n%s", err, report.Markdown())
	}
	want := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	if got := fixtureGit(t, "-C", filepath.Join(c.Collection, "widget"), "rev-parse", "HEAD"); got != want {
		t.Fatalf("catch-up left widget at %s, want %s", got, want)
	}
	snapshot, _, err := c.StatusLiveSnapshot(false, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, repo := range snapshot.Repos {
		found = found || repo.Repo == "widget"
	}
	if !found {
		t.Fatalf("status did not list the ad-hoc worktree: %+v", snapshot.Repos)
	}
}

func TestAdHocCheckoutOwnerFetchesTargetAndForgetsDeletedCheckouts(t *testing.T) {
	root := adHocFixture(t)
	source := adHocSource(t, root, "service", "develop")
	checkout := filepath.Join(root, "narrow")
	fixtureGit(t, "clone", "-q", "--single-branch", "--branch", "main", "file://"+source, checkout)
	c := initAdHoc(t, filepath.Join(root, "task"))
	repo := addAdHoc(t, c, AdHocRepoOptions{Spec: checkout, Target: "develop"})
	want := fixtureGit(t, "-C", source, "rev-parse", "develop")
	if got := fixtureGit(t, "-C", filepath.Join(c.Collection, repo.Name), "rev-parse", "HEAD"); got != want {
		t.Fatalf("single-branch owner did not get the target: %s, want %s", got, want)
	}
	// Later plain fetches must keep the tip moving.
	fixtureGit(t, "-C", source, "switch", "-q", "develop")
	fixtureFile(t, filepath.Join(source, "later.txt"), "later\n", 0644)
	fixtureGit(t, "-C", source, "add", "-A")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "later")
	fixtureGit(t, "-C", source, "switch", "-q", "main")
	if report, err := c.CatchUp(CatchUpOptions{}); err != nil {
		t.Fatalf("catch-up failed: %v\n%s", err, report.Markdown())
	}
	want = fixtureGit(t, "-C", source, "rev-parse", "develop")
	if got := fixtureGit(t, "-C", filepath.Join(c.Collection, repo.Name), "rev-parse", "HEAD"); got != want {
		t.Fatalf("single-branch owner froze its tip: %s, want %s", got, want)
	}
	if err := os.RemoveAll(filepath.Join(c.Collection, repo.Name)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(checkout); err != nil {
		t.Fatal(err)
	}
	second := initAdHoc(t, filepath.Join(root, "second"))
	again := addAdHoc(t, second, AdHocRepoOptions{Spec: "service"})
	if again.Owner != "" {
		t.Fatalf("a deleted checkout is still the owner: %+v", again)
	}
	if owner, err := second.bareFor("service"); err != nil || !strings.Contains(owner, filepath.Join("data", "bare")) {
		t.Fatalf("owner did not fall back to the cache: %s %v", owner, err)
	}
}

func TestAdHocServicesEnlistRunAndRetireWithTheCollection(t *testing.T) {
	root := adHocFixture(t)
	c := initAdHoc(t, filepath.Join(root, "task"))
	addAdHoc(t, c, AdHocRepoOptions{Spec: "file://" + adHocSource(t, root, "widget")})
	c.Config.Runtime = runtimeFixture(t).Config.Runtime
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "WIDGET_WEB_PORT=43011\n", 0600)
	if _, err := c.AddService(ServiceOptions{Service: "web", PortEnv: "WIDGET_WEB_PORT", Cmd: []string{"python3", "-m", "http.server"}, NoStart: true}); err != nil {
		t.Fatal(err)
	}
	items := c.RuntimeEnlisted()
	if len(items) != 1 || items[0].Path != "widget/web/main" || items[0].URL != "http://127.0.0.1:43011" {
		t.Fatalf("ad-hoc service not enlisted: %+v", items)
	}
	if _, err := os.Stat(c.runtimeLocalFragment("widget")); err != nil {
		t.Fatalf("no collection-local task file: %v", err)
	}
	// A sibling made with wtc new keeps the runtime setting.
	fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[runtime]\nbackend = \"dekit\"\n", 0644)
	next, err := c.NewCollection(NewOptions{Slug: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if sibling, err := OpenCollection(next.Collection); err != nil || sibling.Config.Runtime.Backend != "dekit" {
		t.Fatalf("sibling lost the runtime setting: %v", err)
	}
	result, err := c.RetireCollection(RetireOptions{Name: "task", Self: true, InPlace: true})
	if err != nil {
		t.Fatal(err)
	}
	// Only the hand-written config stays for inspection, not the services.
	if _, err := os.Stat(filepath.Join(c.Collection, ".wtc", "svc")); !os.IsNotExist(err) || len(result.Leftovers) != 1 {
		t.Fatalf("services outlived the collection: %+v %v", result, err)
	}
	// The user's own folder is not marked, so it can be initialized again.
	if RetiredFolder(c.Collection) {
		t.Fatal("ad-hoc leftovers were marked as retired")
	}
}

func TestAdHocNewCreatesSiblingWithSameRegistrations(t *testing.T) {
	root := adHocFixture(t)
	source := adHocSource(t, root, "widget", "develop")
	c := initAdHoc(t, filepath.Join(root, "task"))
	addAdHoc(t, c, AdHocRepoOptions{Spec: "file://" + source, Target: "develop"})
	registry := filepath.Join(c.Harness, "repos.yml")
	data, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, registry, string(data)+"    port_offset: 3\n", 0644)
	if c, err = OpenCollection(c.Collection); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NewCollection(NewOptions{PR: "widget#1"}); err == nil {
		t.Fatal("--pr needs a harness registry")
	}
	result, err := c.NewCollection(NewOptions{Slug: "next", Tracker: "PROJ-9", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Collection != filepath.Join(root, "proj-9-next") || result.IntendedBranch != "proj-9-next" {
		t.Fatalf("unexpected result: %+v", result)
	}
	next, err := OpenCollection(result.Collection)
	if err != nil || !next.AdHoc {
		t.Fatalf("sibling is not an ad-hoc collection: %v", err)
	}
	repo, err := next.Repository("widget")
	if err != nil || repo.DefaultRef != "origin/develop" || repo.PortOffset == nil || *repo.PortOffset != 3 {
		t.Fatalf("registration was not carried over: %+v %v", repo, err)
	}
	want := fixtureGit(t, "-C", source, "rev-parse", "develop")
	worktree := filepath.Join(result.Collection, "widget")
	if got := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD"); got != want {
		t.Fatalf("sibling worktree at %s, want %s", got, want)
	}
	for path, want := range map[string]string{"HANDOFF.md": "git switch -c proj-9-next", ".env.collection": "WIDGET_PORT="} {
		data, err := os.ReadFile(filepath.Join(result.Collection, path))
		if err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("%s lacks %q: %v\n%s", path, want, err, data)
		}
	}
	// Two specs naming one repository resolve to a single entry.
	same, err := c.NewCollection(NewOptions{Slug: "same", Repos: []string{"file://" + source, "widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(same.Repositories) != 1 {
		t.Fatalf("duplicate resolved names: %v", same.Repositories)
	}
	if _, err := OpenCollection(same.Collection); err != nil {
		t.Fatalf("registry no longer opens: %v", err)
	}
	if _, err := c.NewCollection(NewOptions{Slug: "next", Tracker: "PROJ-9"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("an existing sibling was overwritten: %v", err)
	}
	// A typo is refused before anything exists; an unreachable remote leaves
	// nothing behind either, so the corrected rerun is not blocked.
	for _, spec := range []string{"widgte", "file://" + filepath.Join(root, "missing.git")} {
		if _, err := c.NewCollection(NewOptions{Slug: "retry", Repos: []string{spec}}); err == nil {
			t.Fatalf("%s was accepted", spec)
		}
		if _, err := os.Lstat(filepath.Join(root, "retry")); !os.IsNotExist(err) {
			t.Fatalf("a failed new with %s left a folder behind: %v", spec, err)
		}
	}
	if _, err := c.NewCollection(NewOptions{Slug: "retry", Repos: []string{"widget"}}); err != nil {
		t.Fatalf("corrected rerun: %v", err)
	}
}

func TestTabaConfigCarriesIntoFreshAdHocSibling(t *testing.T) {
	root := adHocFixture(t)
	source := adHocSource(t, root, "widget")
	c := initAdHoc(t, filepath.Join(root, "task"))
	addAdHoc(t, c, AdHocRepoOptions{Spec: "file://" + source})
	fixtureFile(t, filepath.Join(c.Harness, "taba.toml"), "[status]\nreturn_merged = false\n", 0644)
	result, err := c.NewCollection(NewOptions{Slug: "next", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	next, err := OpenCollection(result.Collection)
	if err != nil || next.Config.Status.ReturnMerged == nil || *next.Config.Status.ReturnMerged {
		t.Fatalf("selected config did not carry over: %+v %v", next, err)
	}
	if _, err := os.Stat(filepath.Join(next.Harness, "wtc.toml")); err != nil {
		t.Fatalf("fresh sibling did not keep the phase-0 output name: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(next.Harness, "taba.toml")); !os.IsNotExist(err) {
		t.Fatal("fresh sibling wrote the future default name")
	}
}

func TestAdHocNewOptionsHooksAndControlRoot(t *testing.T) {
	root := adHocFixture(t)
	widget := adHocSource(t, root, "widget")
	gadget := adHocSource(t, root, "gadget")
	c := initAdHoc(t, filepath.Join(root, "task"))
	addAdHoc(t, c, AdHocRepoOptions{Spec: "file://" + widget})
	// gadget is known per user only, from another collection.
	other := initAdHoc(t, filepath.Join(root, "other"))
	if _, err := other.RegisterAdHocRepo(AdHocRepoOptions{Spec: "file://" + gadget}); err != nil {
		t.Fatal(err)
	}

	fixtureFile(t, filepath.Join(c.ConfigRoot, "widget", "secret.txt"), "synthetic\n", 0600)
	fixtureFile(t, filepath.Join(widget, ".gitignore"), "secret.txt\n", 0644)
	fixtureGit(t, "-C", widget, "add", "-A")
	fixtureGit(t, "-C", widget, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "ignore")
	log := filepath.Join(root, "hooks.log")
	for _, event := range []string{"new.pre", "new.post"} {
		fixtureFile(t, filepath.Join(c.Harness, "hooks", "wtc", event+".sh"), "#!/bin/sh\necho "+event+" >> '"+log+"'\n", 0755)
	}
	for _, bad := range []NewOptions{
		{Issue: "wid-1", Slug: "x"},
		{Slug: "bad slug"},
		{Slug: "x", Tracker: "bad key"},
		{Slug: "x", Branch: "bad branch"},
		{Slug: "x", Repos: []string{"widget", "widget"}},
	} {
		if _, err := c.NewCollection(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !os.IsNotExist(err) {
		t.Fatalf("a refused request created a folder: %v", err)
	}
	result, err := c.NewCollection(NewOptions{Slug: "next", Branch: "feature-x", Repos: []string{"widget", "gadget"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"widget", "gadget"} {
		if branch := fixtureGit(t, "-C", filepath.Join(result.Collection, name), "branch", "--show-current"); branch != "feature-x" {
			t.Fatalf("%s is on %q, want feature-x", name, branch)
		}
	}
	if _, err := os.Lstat(filepath.Join(result.Collection, "widget", "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("source collection secret propagated: %v", err)
	}
	if data, err := os.ReadFile(log); err != nil || string(data) != "new.pre\nnew.post\n" {
		t.Fatalf("source hooks did not run in order: %q %v", data, err)
	}
}

func TestRegistryEntryKeepsEveryRepoField(t *testing.T) {
	offset := 7
	repo := Repo{Name: "widget", Remote: "git@example.invalid:x/widget.git", DefaultRef: "origin/develop", Owner: "/srv/widget/.git",
		PortOffset: &offset, Role: "role", IssuesPrefix: "wid-", ProductionRef: "origin/prod", ReleaseRef: "origin/release",
		ProdPaths: []string{"config/prod"}, IDE: "idea", MergeStrategy: "squash", Extra: map[string]any{"custom": "kept"}}
	collection := t.TempDir()
	path := filepath.Join(collection, AdHocConfigDir, "repos.yml")
	fixtureFile(t, path, adHocRegistryHeader, 0644)
	if err := appendRegistryEntry(path, repo); err != nil {
		t.Fatal(err)
	}
	c := &Context{Collection: collection, Harness: filepath.Dir(path), Workspace: filepath.Dir(collection), AdHoc: true}
	if err := c.load(); err != nil {
		t.Fatal(err)
	}
	got := c.Registry.Repos[0]
	if got.Owner != repo.Owner || *got.PortOffset != 7 || got.IssuesPrefix != "wid-" || got.ProductionRef != repo.ProductionRef ||
		got.ReleaseRef != repo.ReleaseRef || len(got.ProdPaths) != 1 || got.IDE != "idea" || got.MergeStrategy != "squash" ||
		got.Role != "role" || got.Extra["custom"] != "kept" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
}
