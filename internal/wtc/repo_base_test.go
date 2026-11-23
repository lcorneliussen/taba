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

	"taba.sh/taba/internal/gitref"
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
		"schema_version = 1\n[repos.widget]\nbase='beta'\nreason='reason'\n[repos.widget]\nbase='main'\nreason='other'\n",
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
	ref, err := catchUpRefForPR(receiver.WorktreeDir("widget"), gitref.RemoteBranch("origin", "beta"), catchUpPR{state: "OPEN", base: "main"})
	if err != nil || ref != gitref.RemoteBranch("origin", "main") {
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
			tip := fixtureGit(t, "-C", path, "rev-parse", ref.String())
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

func TestRepositoryBaseNewDoesNotReportOrphanIntentAsApplied(t *testing.T) {
	for _, selector := range []string{"renamed", "widget"} {
		t.Run(selector, func(t *testing.T) {
			source, _, _ := repositoryBaseFixture(t)
			source.Registry.Repos[1].Name = "renamed"
			source.Registry.Repos[1].Aliases = []string{"widget"}
			_, err := source.NewCollection(NewOptions{Slug: "inconsistent-base", Repos: []string{"widget"}, RepoBases: map[string]string{selector: "beta"}, BaseReason: "Staging work"})
			if err == nil || !strings.Contains(err.Error(), "receiving registry") {
				t.Fatalf("new orphan intent reported as applied: %v", err)
			}
		})
	}
}

func TestRepositoryBaseBrowseRetainedOrphanUsesUnmanagedFallback(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.SetRepositoryBase("widget", "beta", "Removed repository intent"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Collection, "widget")
	fixtureGit(t, "init", "-q", "-b", "main", path)
	c.Registry.Repos = c.Registry.Repos[:1]
	file := filepath.Join(c.Collection, ".wtc", "repo-bases.toml")
	before, _ := os.ReadFile(file)
	if _, err := c.RepositoryBase("widget"); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("ordinary orphan show must retain recovery refusal: %v", err)
	}
	base, err := c.RepositoryBase("widget", true)
	if err != nil || base.EffectiveRef != "origin/HEAD" || base.Source != "fallback" || base.Reason != "" {
		t.Errorf("retained orphan broke explicit unmanaged browse: %+v %v", base, err)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("browse rewrote retained intent")
	}
	fixtureFile(t, file, "schema_version=1\n[repos.widget]\nbase='beta'\nreason=''\n", 0600)
	if _, err := c.RepositoryBase("widget", true); err == nil {
		t.Fatal("unmanaged orphan fallback ignored corrupt state")
	}
}

func TestRepositoryBaseOrphanDoesNotReassociateThroughReusedAlias(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.SetRepositoryBase("widget", "beta", "Old canonical owner"); err != nil {
		t.Fatal(err)
	}
	c.Registry.Repos[1].Name = "replacement"
	c.Registry.Repos[1].Remote = "https://example.invalid/replacement.git"
	c.Registry.Repos[1].Aliases = []string{"widget"}
	base, err := c.RepositoryBase("replacement")
	if err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Errorf("alias-linked retained intent did not refuse implicit reassociation/fallback: %+v %v", base, err)
	}
	if _, err := c.SetRepositoryBase("agent-harness", "beta", "Independent update"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(c.Collection, ".wtc", "repo-bases.toml")
	body, _ := os.ReadFile(file)
	if !strings.Contains(string(body), "[repos.widget]") || strings.Contains(string(body), "[repos.replacement]") {
		t.Errorf("unrelated update reassociated orphan ownership: %s", body)
	}
	// Explicit selection of the new alias owner stores a separate canonical key.
	if base, err := c.SetRepositoryBase("widget", "gamma", "Explicit new owner"); err != nil || base.Repository != "replacement" {
		t.Fatalf("explicit alias selection: %+v %v", base, err)
	}
	if _, err := ResolveRepositoryBase(c.Collection, c.Registry, "widget"); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("canonical core followed alias: %v", err)
	}
	path := filepath.Join(c.Collection, "widget")
	fixtureGit(t, "init", "-q", "-b", "main", path)
	base, err = c.RepositoryBase("widget", true)
	if err != nil || base.EffectiveRef != "origin/HEAD" || base.Source != "fallback" || base.Reason != "" {
		t.Fatalf("browse inferred alias ownership: %+v %v", base, err)
	}
	outcome, err := c.ClearRepositoryBase("widget")
	if err != nil || !outcome.Orphaned || outcome.ExactKey != "widget" || outcome.Resolution != nil {
		t.Errorf("exact orphan clear followed reused alias: %+v %v", outcome, err)
	}
	base, err = c.RepositoryBase("replacement")
	if err != nil || base.EffectiveRef != "origin/gamma" || base.Reason != "Explicit new owner" {
		t.Fatalf("orphan cleanup removed current canonical intent: %+v %v", base, err)
	}
}

func TestRepositoryBaseCanonicalGuardsSurviveAliasRetention(t *testing.T) {
	for _, kind := range []string{"ambiguous aliases", "duplicate canonical"} {
		t.Run(kind, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			if _, err := c.SetRepositoryBase("widget", "beta", "Existing intent"); err != nil {
				t.Fatal(err)
			}
			other := c.Registry.Repos[1]
			if kind == "ambiguous aliases" {
				c.Registry.Repos[1].Name = "replacement"
				c.Registry.Repos[1].Aliases = []string{"widget"}
				other.Name, other.Aliases = "another", []string{"widget"}
			}
			c.Registry.Repos = append(c.Registry.Repos, other)
			path := filepath.Join(c.Collection, "widget")
			fixtureGit(t, "init", "-q", "-b", "main", path)
			if _, err := c.RepositoryBase("widget", true); err == nil {
				t.Fatal("unmanaged mode relaxed ambiguous identity guard")
			}
			if _, err := c.ClearRepositoryBase("widget"); err == nil {
				t.Fatal("exact cleanup relaxed ambiguous identity guard")
			}
		})
	}
}

func TestRepositoryBaseBrowseKeepsConfiguredHarnessIdentity(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.SetRepositoryBase("agent-harness", "beta", "Harness development"); err != nil {
		t.Fatal(err)
	}
	base, err := c.RepositoryBase("harness", true)
	if err != nil || base.Repository != "agent-harness" || base.EffectiveRef != "origin/beta" || base.Source != "collection" {
		t.Fatalf("configured harness role lost: %+v %v", base, err)
	}
}

func TestRepositoryBaseAliasRetainedIntentNeedsDiagnostic(t *testing.T) {
	c := newWorkspaceFixture(t)
	if err := c.AddWorktree("widget", "widget", c.Collection, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetRepositoryBase("widget", "beta", "Existing collection intent"); err != nil {
		t.Fatal(err)
	}
	c.Registry.Repos[1].Name = "replacement"
	c.Registry.Repos[1].Aliases = []string{"widget"}
	registryFile := filepath.Join(c.Harness, ".harness-repos.yml")
	body, err := os.ReadFile(registryFile)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, registryFile, strings.Replace(string(body), "- name: widget\n", "- name: replacement\n    aliases: [widget]\n", 1), 0600)
	for _, selector := range []string{"widget", "replacement"} {
		base, err := c.RepositoryBase(selector)
		if err == nil || !strings.Contains(err.Error(), "clear widget") {
			t.Errorf("retained alias intent looks like ordinary fallback for %s: %+v %v", selector, base, err)
		}
	}
	if err := c.AddWorktree("replacement", "added", c.Collection, ""); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Errorf("worktree creation silently selected registry default: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(c.Collection, "added")); !os.IsNotExist(err) {
		t.Errorf("refused worktree creation changed the destination: %v", err)
	}
	if _, _, err := c.CatchUpInventory(CatchUpOptions{DryRun: true}); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Errorf("catch-up silently selected registry default: %v", err)
	}
}

func TestRepositoryBaseAliasClearRefusesBeforeMutation(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.SetRepositoryBase("widget", "beta", "Retained old intent"); err != nil {
		t.Fatal(err)
	}
	c.Registry.Repos[1].Name = "replacement"
	c.Registry.Repos[1].Aliases = []string{"widget"}
	if base, err := c.RepositoryBase("agent-harness"); err != nil || base.Source != "registry" {
		t.Fatalf("related-intent refusal blocked an unrelated repository: %+v %v", base, err)
	}
	if base, err := c.SetRepositoryBase("replacement", "gamma", "Explicit canonical recovery"); err != nil || base.EffectiveRef != "origin/gamma" {
		t.Fatalf("explicit recovery depended on refused implicit resolution: %+v %v", base, err)
	}
	file := filepath.Join(c.Collection, ".wtc", "repo-bases.toml")
	before, _ := os.ReadFile(file)
	if _, err := c.ClearRepositoryBase("replacement"); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("canonical clear exposed retained alias intent: %v", err)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("refused canonical clear mutated persistence")
	}
	if _, err := c.SetRepositoryBase("replacement", "", ""); err == nil || !strings.Contains(err.Error(), "clear widget") {
		t.Fatalf("empty-ref setter bypassed clear guard: %v", err)
	}
	after, _ = os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("refused empty-ref setter mutated persistence")
	}
	if outcome, err := c.ClearRepositoryBase("widget"); err != nil || !outcome.Orphaned || outcome.Resolution != nil {
		t.Fatalf("exact orphan recovery failed: %+v %v", outcome, err)
	}
	outcome, err := c.ClearRepositoryBase("replacement")
	if err != nil || outcome.Orphaned || outcome.Resolution == nil || outcome.Resolution.Source != "registry" || outcome.Resolution.EffectiveRef != "origin/main" {
		t.Fatalf("canonical clear after recovery has no real resolution: %+v %v", outcome, err)
	}
}

func TestRepositoryBaseExplicitSetValidatesCanonicalInput(t *testing.T) {
	for _, kind := range []string{"unsafe canonical", "ambiguous canonical"} {
		t.Run(kind, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			c.Registry.Repos[1].Aliases = []string{"selected"}
			if kind == "unsafe canonical" {
				c.Registry.Repos[1].Name = "../outside"
			} else {
				c.Registry.Repos[1].Name = "replacement"
				c.Registry.Repos[0].Aliases = []string{"replacement"}
			}
			if _, err := c.SetRepositoryBase("selected", "beta", "Explicit choice"); err == nil {
				t.Fatal("explicit setter bypassed canonical identity validation")
			}
			if _, err := os.Lstat(filepath.Join(c.Collection, ".wtc", "repo-bases.toml")); !os.IsNotExist(err) {
				t.Fatalf("invalid canonical selection persisted state: %v", err)
			}
		})
	}
}

func TestRepositoryBaseExistingBranchDoesNotNeedAvailableBase(t *testing.T) {
	for _, kind := range []string{"local", "origin"} {
		t.Run(kind, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			bare := filepath.Join(c.Workspace, ".bare", "widget.git")
			ref := "refs/heads/existing-topic"
			if kind == "origin" {
				ref = "refs/remotes/origin/existing-topic"
			}
			fixtureGit(t, "--git-dir="+bare, "update-ref", ref, "refs/remotes/origin/main")
			if kind == "origin" {
				// ensureBare fetches: advertise the branch from the isolated source too.
				source := fixtureGit(t, "--git-dir="+bare, "remote", "get-url", "origin")
				fixtureGit(t, "-C", source, "branch", "existing-topic")
			}
			if _, err := c.SetRepositoryBase("widget", "not-yet-published", "Future default only"); err != nil {
				t.Fatal(err)
			}
			if err := c.AddWorktree("widget", "existing", c.Collection, "existing-topic"); err != nil {
				t.Fatalf("available explicit %s branch blocked by unused base: %v", kind, err)
			}
			if err := c.AddWorktree("widget", "detached", c.Collection, ""); err == nil || !strings.Contains(err.Error(), "unavailable") {
				t.Errorf("detached checkout did not require available base: %v", err)
			}
			if err := c.AddWorktree("widget", "new", c.Collection, "new-topic"); err == nil || !strings.Contains(err.Error(), "unavailable") {
				t.Errorf("new branch did not require available base: %v", err)
			}
			stateFile := filepath.Join(c.Collection, ".wtc", "repo-bases.toml")
			valid, err := os.ReadFile(stateFile)
			if err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, stateFile, "schema_version=2\n", 0600)
			if err := c.AddWorktree("widget", "corrupt", c.Collection, "existing-topic"); err == nil || !strings.Contains(err.Error(), "schema") {
				t.Errorf("explicit branch did not preserve malformed-state refusal: %v", err)
			}
			for i, body := range []string{
				"schema_version=1\n[repos.widget]\nbase='beta^{commit}'\nreason='unsafe ref'\n",
				"schema_version=1\n[repos.widget]\nbase='beta'\nreason=''\n",
			} {
				fixtureFile(t, stateFile, body, 0600)
				if err := c.AddWorktree("widget", fmt.Sprintf("invalid-state-%d", i), c.Collection, "existing-topic"); err == nil || !strings.Contains(err.Error(), "repository bases") {
					t.Errorf("explicit branch bypassed unsafe state: %v", err)
				}
			}
			if err := os.Remove(stateFile); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "state")
			fixtureFile(t, outside, string(valid), 0600)
			if err := os.Symlink(outside, stateFile); err != nil {
				t.Fatal(err)
			}
			if err := c.AddWorktree("widget", "linked-state", c.Collection, "existing-topic"); err == nil || !strings.Contains(err.Error(), "repository bases") {
				t.Errorf("explicit branch followed symlinked state: %v", err)
			}
			if err := os.Remove(stateFile); err != nil {
				t.Fatal(err)
			}
			fixtureFile(t, stateFile, string(valid), 0600)
			if err := c.AddWorktree("widget", "unsafe-branch", c.Collection, "bad~branch"); err == nil || !strings.Contains(err.Error(), "invalid branch") {
				t.Errorf("explicit branch bypassed branch validation: %v", err)
			}
			c.Registry.Repos[0].Aliases = []string{"widget"}
			if err := c.AddWorktree("widget", "ambiguous", c.Collection, "existing-topic"); err == nil || !strings.Contains(err.Error(), "exactly one") {
				t.Errorf("explicit branch bypassed ambiguous identity: %v", err)
			}
			c.Registry.Repos[0].Aliases = nil
			c.Registry.Repos[1].Name = "../unsafe"
			c.Registry.Repos[1].Aliases = []string{"widget"}
			if err := c.AddWorktree("widget", "unsafe-identity", c.Collection, "existing-topic"); err == nil || !strings.Contains(err.Error(), "invalid repository selector") {
				t.Errorf("explicit branch bypassed unsafe canonical identity: %v", err)
			}
		})
	}
}

func TestRepositoryBaseInventoryValidationIsLinearAndFresh(t *testing.T) {
	c := newWorkspaceFixture(t)
	gadget := c.Registry.Repos[1]
	gadget.Name = "gadget"
	c.Registry.Repos = append(c.Registry.Repos, gadget)
	registryFile := filepath.Join(c.Harness, ".harness-repos.yml")
	body, err := os.ReadFile(registryFile)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, registryFile, string(body)+fmt.Sprintf("  - name: gadget\n    remote: %q\n    default_ref: origin/main\n", gadget.Remote), 0600)
	for _, name := range []string{"widget", "gadget"} {
		if err := c.AddWorktree(name, name, c.Collection, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"agent-harness", "widget", "gadget"} {
		if _, err := c.SetRepositoryBase(name, "beta", "Inventory fixture"); err != nil {
			t.Fatal(err)
		}
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "validations")
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nif [ \"$1\" = check-ref-format ]; then printf 'validate\\n' >> \"$BASE_TEST_GIT_LOG\"; exit 0; fi\nexec \"$BASE_TEST_REAL_GIT\" \"$@\"\n", 0755)
	t.Setenv("BASE_TEST_REAL_GIT", realGit)
	t.Setenv("BASE_TEST_GIT_LOG", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, ref := range []string{"beta", "gamma"} {
		if ref == "gamma" {
			file := filepath.Join(c.Collection, ".wtc", "repo-bases.toml")
			body, _ := os.ReadFile(file)
			fixtureFile(t, file, strings.ReplaceAll(string(body), "origin/beta", "origin/gamma"), 0600)
		}
		fixtureFile(t, log, "", 0600)
		_, targets, err := c.CatchUpInventory(CatchUpOptions{DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		trace, _ := os.ReadFile(log)
		calls := strings.Count(string(trace), "validate\n")
		t.Logf("3 repositories x 3 stored overrides: %d check-ref-format calls for one %s inventory", calls, ref)
		if calls > 3 {
			t.Errorf("store validation repeated per lookup: %d calls, want at most 3", calls)
		}
		if len(targets) != 3 {
			t.Fatalf("fixture targets: %d", len(targets))
		}
		for _, target := range targets {
			if target.ref != gitref.RemoteBranch("origin", ref) {
				t.Errorf("new operation hid receiving edits: %s", target.ref)
			}
		}
	}
}

func TestRepositoryBaseSnapshotIsOperationLocalAndImmutable(t *testing.T) {
	c := newWorkspaceFixture(t)
	c.Registry.Repos[1].Aliases = []string{"legacy"}
	if _, err := c.SetRepositoryBase("widget", "beta", "Original snapshot"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := newRepositoryBaseSnapshot(c.Collection, c.Registry)
	if err != nil {
		t.Fatal(err)
	}
	c.Registry.Repos[1].DefaultRef = "origin/next"
	c.Registry.Repos[1].Aliases[0] = "different"
	file := filepath.Join(c.Collection, ".wtc", "repo-bases.toml")
	fixtureFile(t, file, "schema_version=1\n[repos.widget]\nbase='gamma'\nreason='New receiving edit'\n", 0600)
	old, err := snapshot.resolve("widget", false)
	if err != nil || old.EffectiveRef != "origin/beta" || old.RegistryRef != "origin/main" || old.Reason != "Original snapshot" {
		t.Fatalf("operation snapshot changed: %+v %v", old, err)
	}
	fresh, err := c.RepositoryBase("widget")
	if err != nil || fresh.EffectiveRef != "origin/gamma" || fresh.RegistryRef != "origin/next" {
		t.Fatalf("fresh operation hid receiving edits: %+v %v", fresh, err)
	}
	// Alias guards also use the operation's copied identity data.
	c.Registry.Repos[1].Aliases[0] = "legacy"
	fixtureFile(t, file, "schema_version=1\n[repos.legacy]\nbase='beta'\nreason='Retained intent'\n", 0600)
	snapshot, err = newRepositoryBaseSnapshot(c.Collection, c.Registry)
	if err != nil {
		t.Fatal(err)
	}
	c.Registry.Repos[1].Aliases[0] = "different"
	if _, err := snapshot.resolve("widget", false); err == nil || !strings.Contains(err.Error(), "clear legacy") {
		t.Fatalf("snapshot alias identity was mutable: %v", err)
	}
	fixtureFile(t, file, "schema_version=2\n", 0600)
	if _, err := c.RepositoryBase("widget"); err == nil {
		t.Fatal("next operation reused snapshot over corrupt receiving edit")
	}
}
