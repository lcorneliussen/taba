package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two receivers share owners but keep independent development intent.
func repositoryBaseFixture(t *testing.T) (*Context, *Context, string) {
	t.Helper()
	c := newWorkspaceFixture(t)
	for _, name := range []string{"agent-harness", "widget"} {
		src := filepath.Join(c.Workspace, "source-"+name)
		fixtureGit(t, "-C", src, "switch", "-qc", "beta")
		fixtureFile(t, filepath.Join(src, "beta.txt"), "synthetic beta-only change\n", 0600)
		fixtureGit(t, "-C", src, "add", "beta.txt")
		fixtureGit(t, "-C", src, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "beta-only")
		fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", name+".git"), "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
	}
	otherPath := filepath.Join(c.Workspace, "receiver")
	if err := os.Mkdir(otherPath, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "agent-harness.git"), "worktree", "add", "-q", "--detach", filepath.Join(otherPath, "harness"), "origin/main")
	other, err := OpenCollection(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	// Only this receiving collection opts in; the invoking collection stays main.
	fixtureFile(t, filepath.Join(other.Collection, ".wtc", "repo-bases.toml"), `schema_version = 1
[repos.widget]
base = "origin/beta"
reason = "Synthetic staging work"
`, 0600)
	beta := fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "widget.git"), "rev-parse", "origin/beta")
	return c, other, beta
}

func TestRepositoryBaseReceivingCreation(t *testing.T) {
	source, receiver, beta := repositoryBaseFixture(t)
	if err := source.AddWorktree("widget", "widget", receiver.Collection, ""); err != nil {
		t.Fatal(err)
	}
	got := fixtureGit(t, "-C", filepath.Join(receiver.Collection, "widget"), "rev-parse", "HEAD")
	if got != beta {
		t.Errorf("receiver detached creation ignored explicit beta base: got main, want beta")
	}
	if err := source.AddWorktree("widget", "widget", source.Collection, ""); err != nil {
		t.Fatal(err)
	}
	if fixtureGit(t, "-C", filepath.Join(source.Collection, "widget"), "rev-parse", "HEAD") == beta {
		t.Error("override leaked into invoking collection")
	}
}
func TestRepositoryBaseReceivingBranchCreation(t *testing.T) {
	source, receiver, beta := repositoryBaseFixture(t)
	if err := source.AddWorktree("widget", "widget", receiver.Collection, "topic"); err != nil {
		t.Fatal(err)
	}
	if fixtureGit(t, "-C", filepath.Join(receiver.Collection, "widget"), "rev-parse", "HEAD") != beta {
		t.Error("new explicit topic branch started at main instead of receiver beta")
	}
}
func TestRepositoryBaseCountsAndReturnTip(t *testing.T) {
	_, receiver, beta := repositoryBaseFixture(t)
	path := filepath.Join(receiver.Collection, "widget")
	fixtureGit(t, "--git-dir="+filepath.Join(receiver.Workspace, ".bare", "widget.git"), "worktree", "add", "-q", "--detach", path, "origin/beta")
	snapshot, err := receiver.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Repos {
		if row.Repo == "widget" && (row.Ahead != 0 || row.Behind != 0 || row.Branch != "beta") {
			t.Errorf("status at beta should be 0/0 and beta; got ahead=%d behind=%d tip=%s", row.Ahead, row.Behind, row.Branch)
		}
		if row.Repo == "agent-harness" && row.Branch != "main" {
			t.Errorf("unconfigured sibling changed base: %s", row.Branch)
		}
	}
	_, targets, err := receiver.CatchUpInventory(CatchUpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.repo == "widget" {
			if target.ref != "origin/beta" {
				t.Errorf("catch-up inventory ref=%s want origin/beta", target.ref)
			}
			ref, err := receiver.catchUpDefaultRef(target)
			if err != nil {
				t.Fatal(err)
			}
			if ref != "origin/beta" {
				t.Errorf("post-refresh return tip=%s want origin/beta", ref)
			}
			if catchUpLanded(target, ref, catchUpPR{state: "MERGED", head: beta, mergeCommit: beta, base: "beta"}) {
				t.Log("landing established")
			} else {
				t.Error("merged beta head incorrectly not landed against receiving base")
			}
		}
	}
}
func TestRepositoryBasePrimaryReviewFacts(t *testing.T) {
	_, receiver, _ := repositoryBaseFixture(t)
	path := filepath.Join(receiver.Collection, "widget")
	fixtureGit(t, "--git-dir="+filepath.Join(receiver.Workspace, ".bare", "widget.git"), "worktree", "add", "-q", "-b", "topic", path, "origin/beta")
	fixtureFile(t, filepath.Join(path, "topic.txt"), "synthetic topic work\n", 0600)
	fixtureGit(t, "-C", path, "add", "topic.txt")
	fixtureGit(t, "-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "topic")
	// No PR/provider call: synthetic identity only for ReviewRepo's metadata.
	for i := range receiver.Registry.Repos {
		if receiver.Registry.Repos[i].Name == "widget" {
			receiver.Registry.Repos[i].Remote = "https://github.com/example/widget.git"
		}
	}
	bundle, err := receiver.BuildPublicReviewBundle(ReviewBundleOptions{Repo: "widget", Dir: filepath.Join(t.TempDir(), "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.BaseRef != "origin/beta" {
		t.Errorf("primary review base=%s want origin/beta", bundle.Manifest.BaseRef)
	}
	diff, err := os.ReadFile(filepath.Join(bundle.Dir, "diff.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(diff), "beta.txt") {
		t.Error("review patch includes base-line beta work as topic work")
	}
}

func TestRepositoryBaseRelatedAndDownstream(t *testing.T) {
	_, receiver, beta := repositoryBaseFixture(t)
	path := filepath.Join(receiver.Collection, "widget")
	fixtureGit(t, "--git-dir="+filepath.Join(receiver.Workspace, ".bare", "widget.git"), "worktree", "add", "-q", "-b", "topic", path, "origin/beta")
	fixtureFile(t, filepath.Join(receiver.Collection, ".wtc-prs"), "widget 7 topic\n", 0600)
	// A branch already contained in beta must not become new downstream work.
	out := filepath.Join(t.TempDir(), "snapshot")
	made, err := receiver.snapshotReviewRepository(out, "downstream", "widget")
	if err != nil || !made {
		t.Fatalf("snapshot failed: %v %v", made, err)
	}
	if _, err := os.Stat(filepath.Join(out, "downstream", "widget", "new")); err == nil {
		t.Error("downstream beta-contained branch falsely archived as new against main")
	}
	refs, err := os.ReadFile(filepath.Join(out, "downstream", "widget", "REFS"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(refs), "old=origin/main ") {
		t.Error("production snapshot baseline changed")
	}
	if fixtureGit(t, "-C", path, "rev-parse", "HEAD") != beta {
		t.Fatal("fixture head moved")
	}
	fixtureFile(t, filepath.Join(path, "topic.txt"), "synthetic topic\n", 0600)
	fixtureGit(t, "-C", path, "add", "topic.txt")
	fixtureGit(t, "-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "topic")
	patches := filepath.Join(t.TempDir(), "patches")
	count, err := receiver.copyRelatedReviewPatches(patches, "agent-harness", "9")
	if err != nil || count != 1 {
		t.Fatalf("related patch missing: %d %v", count, err)
	}
	patch, err := os.ReadFile(filepath.Join(patches, "related", "widget-pr7.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(patch), "beta.txt") {
		t.Error("related patch includes beta base work")
	}
}

func TestRepositoryBaseNoOverrideControls(t *testing.T) {
	source, receiver, _ := repositoryBaseFixture(t)
	if err := source.AddWorktree("widget", "widget", source.Collection, ""); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Repos {
		if row.Branch != "main" || row.Ahead != 0 || row.Behind != 0 {
			t.Errorf("source isolation failed: %+v", row)
		}
	}
	if err := receiver.AddWorktree("agent-harness", "second", receiver.Collection, ""); err != nil {
		t.Fatal(err)
	}
	if fixtureGit(t, "-C", filepath.Join(receiver.Collection, "second"), "rev-parse", "HEAD") != fixtureGit(t, "--git-dir="+filepath.Join(receiver.Workspace, ".bare", "agent-harness.git"), "rev-parse", "origin/main") {
		t.Error("override crossed repository identity")
	}
	tiers := statusBuildTiers(Repo{DefaultRef: "origin/main"})
	if len(tiers) != 1 || tiers[0].Tier != "prod" || tiers[0].Branch != "main" {
		t.Errorf("production baseline=%+v", tiers)
	}
}
