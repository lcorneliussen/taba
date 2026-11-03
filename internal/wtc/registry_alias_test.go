package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renameWidget renames widget to gadget in a collection's registry, keeping
// widget as an alias, and points it at a new remote as a forge rename does.
func renameWidget(t *testing.T, collection, remote string) {
	t.Helper()
	path := filepath.Join(collection, "harness", ".harness-repos.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if line == "  - name: widget" {
			lines[i] = "  - name: gadget\n    aliases: [widget]"
			lines[i+1] = "    remote: " + remote
		}
	}
	fixtureFile(t, path, strings.Join(lines, "\n"), 0644)
}

func TestRegistryAliasKeepsRenamedRepositoryWorking(t *testing.T) {
	c := newWorkspaceFixture(t)
	t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
	old, err := c.NewCollection(NewOptions{Slug: "before", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	// The forge repository moves: same history, new remote.
	// The forge serves the repository as forge/widget, then as forge/gadget.
	forge := filepath.Join(c.Workspace, "forge")
	fixtureGit(t, "clone", "-q", "--bare", filepath.Join(c.Workspace, "source-widget"), filepath.Join(forge, "widget"))
	fixtureGit(t, "clone", "-q", "--bare", filepath.Join(c.Workspace, "source-widget"), filepath.Join(forge, "gadget"))
	moved := "file://" + filepath.Join(forge, "gadget")
	fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "widget.git"), "remote", "set-url", "origin", "file://"+filepath.Join(forge, "widget"))
	fixtureFile(t, filepath.Join(c.ConfigRoot, "widget", "secrets.txt"), "synthetic fixture\n", 0600)
	renameWidget(t, old.Collection, moved)
	renameWidget(t, c.Collection, moved)
	configRoot := c.ConfigRoot
	if c, err = OpenCollection(c.Collection); err != nil {
		t.Fatal(err)
	}
	c.ConfigRoot = configRoot
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")

	target, err := OpenCollection(old.Collection)
	if err != nil {
		t.Fatal(err)
	}
	if repo, err := target.Repository("widget"); err != nil || repo.Name != "gadget" {
		t.Fatalf("alias lookup = %+v, %v", repo, err)
	}
	if dir := target.WorktreeDir("gadget"); dir != filepath.Join(old.Collection, "widget") {
		t.Fatalf("worktree dir = %s", dir)
	}
	if bare, err := target.bareFor("gadget"); err != nil || bare != owner {
		t.Fatalf("owner = %s, %v", bare, err)
	}
	if refresh, err := target.RefreshRegistry(); err != nil || len(refresh.Missing) != 0 || len(refresh.Unlisted) != 0 {
		t.Fatalf("registry refresh should count the alias owner: %+v, %v", refresh, err)
	}
	if worktree, _, _, err := target.ReviewRepo("gadget"); worktree != filepath.Join(old.Collection, "widget") && (err == nil || strings.Contains(err.Error(), "no worktree")) {
		t.Fatalf("review should find the alias folder: %s, %v", worktree, err)
	}
	env, err := target.RenderEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "GADGET_PORT=42001\n") || !strings.Contains(string(env), "WIDGET_PORT=42001\n") {
		t.Fatalf("both port names should be emitted:\n%s", env)
	}

	report, err := target.CatchUp(CatchUpOptions{})
	if err != nil {
		t.Fatalf("catch-up: %v %+v", err, report.Outcomes)
	}
	updated := false
	for _, row := range report.Outcomes {
		updated = updated || row.Kind == "origin" && row.Outcome == "updated" && row.Repo == "widget"
	}
	if !updated {
		t.Fatalf("catch-up did not report the origin change: %+v", report.Outcomes)
	}
	if got := fixtureGit(t, "--git-dir="+owner, "remote", "get-url", "origin"); got != moved {
		t.Fatalf("owner origin = %s, want %s", got, moved)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, ".bare", "gadget.git")); !os.IsNotExist(err) {
		t.Fatalf("a second owner was cloned: %v", err)
	}

	snapshot, err := target.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range snapshot.Repos {
		if row.Dir == "widget" {
			found = row.RenamedTo == "gadget"
		}
	}
	if !found {
		t.Fatalf("status should note the alias folder: %+v", snapshot.Repos)
	}

	if _, err := target.AddRepositories(AddRepoOptions{Repos: []string{"gadget"}}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a second checkout under the new name was accepted: %v", err)
	}

	// A new collection asked for the old name checks out the new folder from
	// the existing owner.
	fresh, err := c.NewCollection(NewOptions{Slug: "after", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fresh.Collection, "gadget", ".git")); err != nil {
		t.Fatalf("new collection lacks gadget/: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fresh.Collection, "widget")); !os.IsNotExist(err) {
		t.Fatalf("new collection used the old folder name: %v", err)
	}
	if common := fixtureGit(t, "-C", filepath.Join(fresh.Collection, "gadget"), "rev-parse", "--path-format=absolute", "--git-common-dir"); !sameFile(common, owner) {
		t.Fatalf("new checkout owner = %s, want %s", common, owner)
	}
	// Secrets kept under the old name link into the new folder. The fixture
	// registry edit is uncommitted, so the new harness needs it too.
	renameWidget(t, fresh.Collection, moved)
	if after, err := OpenCollection(fresh.Collection); err != nil {
		t.Fatal(err)
	} else if _, err := after.LinkSecrets(SecretLinkOptions{}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(fresh.Collection, "gadget", "secrets.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("secret under the alias was not linked: %v", err)
	}

	// The registry edit stands in for a harness commit; it is the only change.
	retired, err := c.RetireCollection(RetireOptions{Name: "before", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !retired.FolderRemoved {
		t.Fatalf("alias-folder collection was not retired: %+v", retired)
	}
}

func TestRegistryRejectsAliasCollisions(t *testing.T) {
	for _, registry := range []string{
		"repos:\n  - name: one\n    remote: a\n    aliases: [two]\n  - name: two\n    remote: b\n",
		"repos:\n  - name: one\n    remote: a\n    aliases: [old]\n  - name: two\n    remote: b\n    aliases: [old]\n",
		"repos:\n  - name: one\n    remote: a\n    aliases: [harness]\n",
		"repos:\n  - name: one\n    remote: a\n    port_offset: 1\n    aliases: [old_x]\n  - name: old-x\n    remote: b\n    port_offset: 2\n",
	} {
		root := t.TempDir()
		fixtureFile(t, filepath.Join(root, "c", "harness", ".harness-repos.yml"), registry, 0644)
		if _, err := Discover(filepath.Join(root, "c")); err == nil {
			t.Errorf("accepted:\n%s", registry)
		}
	}
}

func sameFile(a, b string) bool {
	x, errX := os.Stat(a)
	y, errY := os.Stat(b)
	return errX == nil && errY == nil && os.SameFile(x, y)
}

func TestOwnerOriginFollowsOnlyARenameInPlace(t *testing.T) {
	site := Repo{Name: "wtc-site", Remote: "git@github.com:org/taba-site.git"}
	gadget := Repo{Name: "gadget", Aliases: []string{"widget"}, Remote: "git@github.com:org/gadget.git"}
	older := Repo{Name: "widget", Remote: "git@github.com:org/widget.git"} // registry before the rename
	for _, c := range []struct {
		from string
		repo Repo
		want bool
	}{
		{"git@github.com:org/wtc-site.git", site, true},
		{"https://github.com/org/wtc-site", site, true},
		{"git@github.com:org/widget.git", gadget, true},
		{"git@github.com:org/taba-site.git", Repo{Name: "taba-site", Remote: "https://github.com/org/taba-site"}, false},
		{"git@github.com:fork/widget.git", gadget, false},
		{"git@gitlab.com:org/widget.git", gadget, false},
		{"git@github.com:org/widget-mirror.git", gadget, false},
		{"git@github.com:org/gadget.git", older, false},
		{"/srv/mirror/widget", gadget, false},
		{"git@github.com:org/widget.git", Repo{Name: "gadget", Aliases: []string{"widget"}, Remote: "/srv/gadget"}, false},
	} {
		owner := filepath.Join(t.TempDir(), "o.git")
		fixtureGit(t, "init", "-q", "--bare", owner)
		fixtureGit(t, "--git-dir="+owner, "remote", "add", "origin", c.from)
		changed, err := syncOwnerOrigin(owner, c.repo)
		if err != nil || changed != c.want {
			t.Errorf("%s -> %s: changed=%t err=%v, want %t", c.from, c.repo.Remote, changed, err, c.want)
		}
	}
}

func TestCheckoutPointsRenamedOwnerAtNewRemote(t *testing.T) {
	c := newWorkspaceFixture(t)
	forge := filepath.Join(c.Workspace, "forge")
	for _, name := range []string{"widget", "gadget"} {
		fixtureGit(t, "clone", "-q", "--bare", filepath.Join(c.Workspace, "source-widget"), filepath.Join(forge, name))
	}
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "file://"+filepath.Join(forge, "widget"))
	renameWidget(t, c.Collection, "file://"+filepath.Join(forge, "gadget"))
	configRoot := c.ConfigRoot
	c, err := OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	c.ConfigRoot = configRoot
	if _, err := c.NewCollection(NewOptions{Slug: "fresh", Repos: []string{"gadget"}}); err != nil {
		t.Fatal(err)
	}
	if got := fixtureGit(t, "--git-dir="+owner, "remote", "get-url", "origin"); got != "file://"+filepath.Join(forge, "gadget") {
		t.Fatalf("checkout left origin at %s", got)
	}
}
