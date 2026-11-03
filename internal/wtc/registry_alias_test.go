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
	moved := filepath.Join(c.Workspace, "source-gadget")
	fixtureGit(t, "clone", "-q", filepath.Join(c.Workspace, "source-widget"), moved)
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
