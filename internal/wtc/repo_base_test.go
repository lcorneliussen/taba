package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRepositoryBaseStoreAndIdentity(t *testing.T) {
	c := newWorkspaceFixture(t)
	c.Registry.Repos[1].Aliases = []string{"legacy"}
	got, err := c.SetRepositoryBase("legacy", "beta", "Staging work")
	if err != nil || got.Repository != "widget" || got.EffectiveRef != "origin/beta" || got.Source != "collection" {
		t.Fatalf("set: %+v %v", got, err)
	}
	raw, err := os.ReadFile(filepath.Join(c.Collection, ".wtc", "repo-bases.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy") {
		t.Fatal("alias persisted instead of canonical identity")
	}
	if got, err = c.SetRepositoryBase("widget", "", ""); err != nil || got.Source != "registry" || got.EffectiveRef != "origin/main" {
		t.Fatalf("clear: %+v %v", got, err)
	}
	c.Registry.Repos[0].Aliases = []string{"legacy"}
	if _, err := c.RepositoryBase("legacy"); err == nil {
		t.Fatal("ambiguous alias accepted")
	}
	if _, err := c.SetRepositoryBase("../widget", "beta", "reason"); err == nil {
		t.Fatal("path selector accepted")
	}
}
func TestRepositoryBaseRejectsInvalidState(t *testing.T) {
	for _, input := range []string{
		"schema_version = 2\n", "schema_version = 1\nunknown = true\n",
		"schema_version = 1\n[repos.widget]\nbase='beta'\nreason=''\n",
		"schema_version = 1\n[repos.widget]\nbase='beta'\nreason='reason'\n[repos.legacy]\nbase='main'\nreason='other'\n",
	} {
		t.Run(input, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			c.Registry.Repos[1].Aliases = []string{"legacy"}
			fixtureFile(t, filepath.Join(c.Collection, ".wtc", "repo-bases.toml"), input, 0600)
			if _, err := c.RepositoryBase("widget"); err == nil {
				t.Fatal("malformed state masked")
			}
			if _, err := c.SetRepositoryBase("widget", "", ""); err == nil {
				t.Fatal("malformed state overwritten")
			}
		})
	}
	for _, ref := range []string{"-bad", "@{-1}", "refs/tags/release", "beta^{commit}", "beta~1", "beta..x", "beta x", "origin/", "beta\nmain"} {
		if _, err := normalizeRepoBase(ref); err == nil {
			t.Errorf("unsafe ref accepted: %q", ref)
		}
	}
	for _, reason := range []string{"", " \t ", "control\x1btext", strings.Repeat("x", 1025)} {
		if validateBaseReason(reason) == nil {
			t.Errorf("unsafe reason accepted: %q", reason)
		}
	}
}
func TestRepositoryBaseNoFollow(t *testing.T) {
	for _, kind := range []string{"namespace", "file", "lock", "receiver", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "keep")
			fixtureFile(t, sentinel, "untouched", 0600)
			switch kind {
			case "namespace":
				if err := os.Symlink(outside, filepath.Join(c.Collection, ".wtc")); err != nil {
					t.Fatal(err)
				}
			case "receiver":
				link := filepath.Join(t.TempDir(), "receiver")
				if err := os.Symlink(c.Collection, link); err != nil {
					t.Fatal(err)
				}
				c.Collection = link
			default:
				if err := os.MkdirAll(filepath.Join(c.Collection, ".wtc"), 0700); err != nil {
					t.Fatal(err)
				}
				name := "repo-bases.toml"
				if kind == "lock" {
					name = "repo-bases.lock"
				}
				path := filepath.Join(c.Collection, ".wtc", name)
				if kind == "fifo" {
					if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
						t.Fatalf("mkfifo: %s %v", out, err)
					}
				} else if err := os.Symlink(sentinel, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.SetRepositoryBase("widget", "beta", "reason"); err == nil {
				t.Fatal("unsafe path accepted")
			}
			body, _ := os.ReadFile(sentinel)
			if string(body) != "untouched" {
				t.Fatal("linked target modified")
			}
		})
	}
}
func TestRepositoryBaseConcurrentUpdates(t *testing.T) {
	c := newWorkspaceFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, name := range []string{"widget", "agent-harness"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := c.SetRepositoryBase(name, "beta", "parallel intent")
			errs <- err
		}(name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"widget", "agent-harness"} {
		base, err := c.RepositoryBase(name)
		if err != nil || base.EffectiveRef != "origin/beta" {
			t.Fatalf("lost update: %+v %v", base, err)
		}
	}
}
func TestRepositoryBaseNewAndPreserveCheckout(t *testing.T) {
	source, receiver, _ := repositoryBaseFixture(t)
	result, err := source.NewCollection(NewOptions{Slug: "beta-work", Repos: []string{"widget"}, RepoBases: map[string]string{"widget": "beta"}, BaseReason: "Staging work"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := OpenCollection(result.Collection)
	if err != nil {
		t.Fatal(err)
	}
	if base, err := created.RepositoryBase("widget"); err != nil || base.EffectiveRef != "origin/beta" {
		t.Fatalf("new base: %+v %v", base, err)
	}
	if fixtureGit(t, "-C", created.WorktreeDir("widget"), "rev-parse", "HEAD") != fixtureGit(t, "-C", created.WorktreeDir("widget"), "rev-parse", "origin/beta") {
		t.Fatal("new worktree did not use explicit base")
	}
	if err := source.AddWorktree("widget", "widget", source.Collection, "unpublished"); err != nil {
		t.Fatal(err)
	}
	path := source.WorktreeDir("widget")
	fixtureFile(t, filepath.Join(path, "local"), "work", 0600)
	fixtureGit(t, "-C", path, "add", "local")
	fixtureGit(t, "-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "unpublished")
	fixtureFile(t, filepath.Join(path, "dirty"), "keep", 0600)
	before := fixtureGit(t, "-C", path, "rev-parse", "HEAD")
	dirty := fixtureGit(t, "-C", path, "status", "--porcelain")
	if _, err := source.SetRepositoryBase("widget", "beta", "Change future base only"); err != nil {
		t.Fatal(err)
	}
	if fixtureGit(t, "-C", path, "rev-parse", "HEAD") != before || fixtureGit(t, "-C", path, "status", "--porcelain") != dirty {
		t.Fatal("setter moved dirty/unpublished work")
	}
	if _, err := receiver.SetRepositoryBase("widget", "missing", "Unavailable branch"); err != nil {
		t.Fatal(err)
	}
	if err := receiver.AddWorktree("widget", "widget", receiver.Collection, ""); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unavailable override fell back: %v", err)
	}
}
func TestRepositoryBaseActualPRTargetIsSeparate(t *testing.T) {
	_, receiver, _ := repositoryBaseFixture(t)
	if err := receiver.AddWorktree("widget", "widget", receiver.Collection, "topic"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := receiver.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range snapshot.Repos {
		if row.Repo == "widget" {
			statusPRBase(&snapshot, i, "main")
			if snapshot.Repos[i].ComparisonRef != "origin/main" || snapshot.Repos[i].DevelopmentBase.EffectiveRef != "origin/beta" {
				t.Fatal("observed PR base overwrote configured intent")
			}
		}
	}
	ref, err := catchUpRefForPR(receiver.WorktreeDir("widget"), "origin/beta", catchUpPR{state: "OPEN", base: "main"})
	if err != nil || ref != "origin/main" {
		t.Fatalf("actual PR target protection lost: %s %v", ref, err)
	}
	base, err := receiver.RepositoryBase("widget")
	if err != nil || base.EffectiveRef != "origin/beta" {
		t.Fatal("PR target persisted as override")
	}
	if !strings.Contains(snapshot.Markdown(), "Synthetic staging work") || !strings.Contains(snapshot.Markdown(), "PR comparison") {
		t.Fatal("Markdown hides base/reason/disagreement")
	}
	data, _ := json.Marshal(snapshot)
	if !strings.Contains(string(data), "development_base") {
		t.Fatal("missing JSON base facts")
	}
}

func TestRepositoryBaseReviewPrivacyAndLiteralEnv(t *testing.T) {
	_, receiver, _ := repositoryBaseFixture(t)
	if err := receiver.AddWorktree("widget", "widget", receiver.Collection, ""); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	reason := "PRIVATE_REASON_CANARY ' $(touch " + marker + ") `touch " + marker + "` $HOME " + string(rune(92))
	if _, err := receiver.SetRepositoryBase("widget", "beta", reason); err != nil {
		t.Fatal(err)
	}
	for i := range receiver.Registry.Repos {
		if receiver.Registry.Repos[i].Name == "widget" {
			receiver.Registry.Repos[i].Remote = "https://github.com/example/widget.git"
			receiver.Registry.Repos[i].ProductionRef = "PRIVATE_PRODUCTION_CANARY"
			receiver.Registry.Repos[i].ReleaseRef = "PRIVATE_RELEASE_CANARY"
		}
	}
	// Explicit public diff target supplies public context; private deployment
	// policy and human text must remain absent from every emitted artifact.
	bundle, err := receiver.BuildPublicReviewBundle(ReviewBundleOptions{Repo: "widget", Base: "origin/beta", Dir: filepath.Join(t.TempDir(), "public")})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.DevelopmentBase == nil || !bundle.Manifest.DevelopmentBase.ReasonRedacted {
		t.Fatal("public base fact not redacted")
	}
	err = filepath.WalkDir(bundle.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, canary := range []string{"PRIVATE_REASON_CANARY", "PRIVATE_PRODUCTION_CANARY", "PRIVATE_RELEASE_CANARY"} {
				if strings.Contains(string(body), canary) {
					t.Errorf("public artifact leaked %s: %s", canary, filepath.Base(p))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	private, err := receiver.BuildReviewBundle(ReviewBundleOptions{Repo: "widget", Private: true, NoCatchUp: true, Dir: filepath.Join(t.TempDir(), "private")})
	if err != nil {
		t.Fatal(err)
	}
	if private.Manifest.DevelopmentBase == nil || private.Manifest.DevelopmentBase.Reason != reason {
		t.Fatal("private review lost reason")
	}
	env := filepath.Join(private.Dir, "manifest.env")
	output, err := exec.Command("sh", "-c", `. "$1"; printf '%s' "$DEVELOPMENT_BASE_REASON"`, "sh", env).CombinedOutput()
	if err != nil {
		t.Fatalf("source env: %s %v", output, err)
	}
	if string(output) != reason {
		t.Fatalf("reason was not literal: %q", output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("sourcing manifest executed reason text")
	}
}

func TestRepositoryBaseMergedReturnToBetaPreservesDirtyWork(t *testing.T) {
	_, receiver, _ := repositoryBaseFixture(t)
	if err := receiver.AddWorktree("widget", "widget", receiver.Collection, "topic"); err != nil {
		t.Fatal(err)
	}
	path := receiver.WorktreeDir("widget")
	fixtureFile(t, filepath.Join(path, "topic.txt"), "landed topic\n", 0600)
	fixtureGit(t, "-C", path, "add", "topic.txt")
	fixtureGit(t, "-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "topic")
	head := fixtureGit(t, "-C", path, "rev-parse", "HEAD")
	// Simulate an actual merge into beta in the shared synthetic origin.
	src := filepath.Join(receiver.Workspace, "source-widget")
	fixtureGit(t, "-C", src, "fetch", filepath.Join(receiver.Workspace, ".bare", "widget.git"), "topic")
	fixtureGit(t, "-C", src, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "merge", "--no-ff", "--no-edit", "FETCH_HEAD")
	fixtureGit(t, "--git-dir="+filepath.Join(receiver.Workspace, ".bare", "widget.git"), "fetch", "origin", "+refs/heads/*:refs/remotes/origin/*")
	fixtureFile(t, filepath.Join(path, "dirty.txt"), "keep local work\n", 0600)
	_, targets, err := receiver.CatchUpInventory(CatchUpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, target := range targets {
		if target.repo == "widget" {
			found = true
			ref, err := receiver.catchUpDefaultRef(target)
			if err != nil {
				t.Fatal(err)
			}
			tip := fixtureGit(t, "-C", path, "rev-parse", ref)
			outcome, reason := receiver.catchUpReconcile(target, ref, tip, catchUpPR{state: "MERGED", head: head, mergeCommit: tip, base: "beta", branch: "topic"}, CatchUpOptions{})
			if outcome != "updated" {
				t.Fatalf("return to beta: %s %s", outcome, reason)
			}
			if fixtureGit(t, "-C", path, "rev-parse", "HEAD") != tip || fixtureGit(t, "-C", path, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
				t.Fatal("did not detach at beta")
			}
			body, _ := os.ReadFile(filepath.Join(path, "dirty.txt"))
			if string(body) != "keep local work\n" {
				t.Fatal("dirty work lost on merged return")
			}
		}
	}
	if !found {
		t.Fatal("target missing")
	}
}

func TestRepositoryBaseBuildProductionRemainsRegistryOwned(t *testing.T) {
	_, receiver, _ := repositoryBaseFixture(t)
	if err := receiver.AddWorktree("widget", "widget", receiver.Collection, ""); err != nil {
		t.Fatal(err)
	}
	hook := `#!/bin/sh
payload=$(cat)
case "$payload" in
 *'"tier":"prod"'*) printf '%s\n' '{"branch":"main","checks":"SUCCESS"}';;
 *'"branch":"beta"'*) printf '%s\n' '{"branch":"beta","checks":"SUCCESS"}';;
 *) printf '%s\n' 'null';;
esac
`
	fixtureFile(t, filepath.Join(receiver.Harness, "hooks", "wtc", "status.build.sh"), hook, 0700)
	snapshot, err := receiver.StatusLocalSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.statusBuildFacts(&snapshot); err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Repos {
		if row.Repo == "widget" {
			if row.Tip == nil || row.Tip.Branch != "beta" || row.Prod == nil || row.Prod.Branch != "main" {
				t.Fatalf("wrong build tiers: tip=%+v prod=%+v", row.Tip, row.Prod)
			}
		}
	}
	repo, _ := receiver.Repository("widget")
	if repo.DefaultRef != "origin/main" || repo.ProductionRef != "" || repo.ReleaseRef != "" {
		t.Fatal("registry deployment policy mutated")
	}
}

func TestRepositoryBaseConcurrentClearKeepsOtherIntent(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.SetRepositoryBase("widget", "beta", "Original intent"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() { <-start; _, err := c.SetRepositoryBase("widget", "", ""); errs <- err }()
	go func() {
		<-start
		_, err := c.SetRepositoryBase("agent-harness", "beta", "Separate intent")
		errs <- err
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	a, err := c.RepositoryBase("widget")
	if err != nil || a.Source != "registry" {
		t.Fatalf("clear lost: %+v %v", a, err)
	}
	b, err := c.RepositoryBase("agent-harness")
	if err != nil || b.Source != "collection" {
		t.Fatalf("separate update lost: %+v %v", b, err)
	}
}

func TestRepositoryBaseCreationUsesReceivingRegistryIdentity(t *testing.T) {
	source, receiver, beta := repositoryBaseFixture(t)
	registry := filepath.Join(receiver.Harness, ".harness-repos.yml")
	body, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), "- name: widget\n", "- name: widget\n    aliases: [receiver-only]\n", 1))
	if err := os.WriteFile(registry, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.AddWorktree("receiver-only", "widget", receiver.Collection, ""); err != nil {
		t.Fatal(err)
	}
	if fixtureGit(t, "-C", receiver.WorktreeDir("widget"), "rev-parse", "HEAD") != beta {
		t.Fatal("receiving identity used caller's base")
	}
}

func TestRepositoryBaseBrowseFallbackDoesNotPermitUnknownOverrides(t *testing.T) {
	c := newWorkspaceFixture(t)
	path := filepath.Join(c.Collection, "ext.widget")
	fixtureGit(t, "init", "-q", "-b", "main", path)
	if _, err := c.RepositoryBase("ext.widget"); err == nil {
		t.Fatal("ordinary show accepted unknown identity")
	}
	base, err := c.RepositoryBase("ext.widget", true)
	if err != nil || base.EffectiveRef != "origin/HEAD" || base.Source != "fallback" {
		t.Fatalf("legacy unmanaged browse fallback: %+v %v", base, err)
	}
	if _, err := c.SetRepositoryBase("ext.widget", "beta", "unregistered"); err == nil {
		t.Fatal("unmanaged mode permitted an override")
	}
	if _, err := c.RepositoryBase("missing", true); err == nil {
		t.Fatal("unmanaged mode accepted missing worktree")
	}
	fixtureFile(t, filepath.Join(c.Collection, ".wtc", "repo-bases.toml"), "schema_version=1\n[repos.unknown]\nbase='beta'\nreason=''\n", 0600)
	if _, err := c.RepositoryBase("ext.widget", true); err == nil {
		t.Fatal("unmanaged fallback masked malformed receiving state")
	}
}

func TestRepositoryBaseInventoryIsolatesBrokenReceiver(t *testing.T) {
	source, receiver, _ := repositoryBaseFixture(t)
	fixtureFile(t, filepath.Join(receiver.Collection, ".wtc", "repo-bases.toml"), "schema_version = not-valid-toml\n", 0600)
	report, targets, err := source.CatchUpInventory(CatchUpOptions{Collections: []string{filepath.Base(source.Collection), filepath.Base(receiver.Collection)}, DryRun: true})
	if err != nil {
		t.Fatalf("broken receiver aborted healthy inventory: %v", err)
	}
	if report.ExitStatus != 1 || len(targets) == 0 {
		t.Fatalf("missing partial inventory: %+v targets=%d", report, len(targets))
	}
	failed := false
	for _, row := range report.Outcomes {
		if row.Collection == filepath.Base(receiver.Collection) && row.Outcome == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Fatal("broken receiver not identified")
	}
	for _, target := range targets {
		if target.collection != filepath.Base(source.Collection) {
			t.Fatal("broken receiver remained eligible for mutation")
		}
	}
	// Single-receiver callers include runtime renderers that consume the error,
	// not report outcomes. Never let malformed state look like an empty task set.
	if _, single, err := receiver.CatchUpInventory(CatchUpOptions{DryRun: true}); err == nil || len(single) != 0 {
		t.Fatal("broken single receiver did not fail closed")
	}

}

func TestRepositoryBaseOrphanRecovery(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.SetRepositoryBase("widget", "beta", "Retain staging intent"); err != nil {
		t.Fatal(err)
	}
	// A receiving harness update removes the old identity without an alias.
	c.Registry.Repos = c.Registry.Repos[:1]
	if _, err := c.RepositoryBase("agent-harness"); err != nil {
		t.Fatalf("orphan blocked unrelated repository: %v", err)
	}
	if _, err := c.RepositoryBase("widget"); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("orphan show lacks recovery instruction: %v", err)
	}
	if _, err := c.SetRepositoryBase("widget", "main", "Unregistered"); err == nil {
		t.Fatal("set admitted orphan identity")
	}
	if _, err := c.SetRepositoryBase("agent-harness", "beta", "Separate intent"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(c.Collection, ".wtc", "repo-bases.toml"))
	if !strings.Contains(string(body), "Retain staging intent") {
		t.Fatal("unrelated set deleted orphan")
	}
}

func TestRepositoryBaseExactOrphanClearAndConcurrentPreservation(t *testing.T) {
	for _, renamed := range []bool{false, true} {
		t.Run(fmt.Sprint(renamed), func(t *testing.T) {
			c := newWorkspaceFixture(t)
			if _, err := c.SetRepositoryBase("widget", "beta", "Old intent"); err != nil {
				t.Fatal(err)
			}
			if renamed {
				c.Registry.Repos[1].Name = "renamed"
				c.Registry.Repos[1].Aliases = nil
			} else {
				c.Registry.Repos = c.Registry.Repos[:1]
			}
			start := make(chan struct{})
			errs := make(chan error, 2)
			var outcome RepositoryBaseClear
			go func() { <-start; var err error; outcome, err = c.ClearRepositoryBase("widget"); errs <- err }()
			go func() {
				<-start
				_, err := c.SetRepositoryBase("agent-harness", "beta", "Independent intent")
				errs <- err
			}()
			close(start)
			for range 2 {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
			if !outcome.Cleared || !outcome.Orphaned || outcome.ExactKey != "widget" || outcome.Resolution != nil {
				t.Fatalf("fabricated orphan resolution: %+v", outcome)
			}
			if _, err := c.ClearRepositoryBase("widget"); err == nil {
				t.Fatal("unknown clear without stored key succeeded")
			}
			base, err := c.RepositoryBase("agent-harness")
			if err != nil || base.EffectiveRef != "origin/beta" {
				t.Fatalf("concurrent update lost: %+v %v", base, err)
			}
		})
	}
}

func TestRepositoryBaseKeepsExistingUnderscoreIdentity(t *testing.T) {
	c := newWorkspaceFixture(t)
	c.Registry.Repos[1].Name = "_widget"
	base, err := c.SetRepositoryBase("_widget", "beta", "Existing valid identity")
	if err != nil || base.Repository != "_widget" {
		t.Fatalf("existing identity rejected: %+v %v", base, err)
	}
}
