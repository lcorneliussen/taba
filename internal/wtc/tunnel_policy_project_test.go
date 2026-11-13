package wtc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func tunnelProjectMember(t *testing.T, root, name string) *Context {
	t.Helper()
	member := filepath.Join(root, name)
	fixtureFile(t, filepath.Join(member, CollectionDescriptorFile), "schema_version=1\nproject='..'\n", 0644)
	c, err := OpenCollection(member)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tunnelFixtureLockHeld(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
}

func TestTunnelProjectPolicySerializesTwoCollectionOwners(t *testing.T) {
	p := localProject(t)
	a, b := tunnelProjectMember(t, p.Root, "one"), tunnelProjectMember(t, p.Root, "two")
	entry := testTunnelDefinition().Entry.Policy
	if err := a.SetTunnelPolicy("widget", "web", "seed", entry, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.Root, ".config", "tunnels.toml")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, err := lockDevFile(ctx, filepath.Join(filepath.Dir(path), ".tunnels-policy.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(unlock) }
	var wg sync.WaitGroup
	done := make(chan error, 2)
	defer func() { release(); wg.Wait() }()
	no := false
	for _, call := range []func() error{func() error { return a.SetTunnelPolicy("widget", "web", "first", entry, true) }, func() error {
		return b.SetTunnelPolicy("widget", "web", "second", tunnelPolicyEntry{Enabled: &no}, true)
	}} {
		wg.Add(1)
		go func(run func() error) { defer wg.Done(); done <- run() }(call)
	}
	// Observe both actual collection admission locks while the canonical
	// project lock is held. A writer bypassing it completes early and fails;
	// no probabilistic simultaneous-read timing or production test hook.
	deadline := time.Now().Add(3 * time.Second)
	for !(tunnelFixtureLockHeld(filepath.Join(a.Collection, ".wtc-runtime.lock")) && tunnelFixtureLockHeld(filepath.Join(b.Collection, ".wtc-runtime.lock"))) {
		select {
		case err := <-done:
			t.Fatalf("project writer bypassed shared lock: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("writers did not reach bounded admission")
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, err := readTunnelPolicyFile(path)
	if err != nil || len(current.Entries) != 1 {
		t.Fatal("project changed before shared lock release", err)
	}
	release()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	current, err = readTunnelPolicyFile(path)
	if err != nil || len(current.Entries) != 3 || !current.Entries[tunnelKey{"widget", "web", "second"}].disabled() {
		t.Fatal("concurrent project edit/tombstone lost", current, err)
	}
	override := tunnelPolicyEntry{Provider: "devtunnel", Mode: "persistent", Access: "account"}
	if err = a.SetTunnelPolicy("widget", "web", "first", override, false); err != nil {
		t.Fatal(err)
	}
	_, localPath, _ := a.tunnelPolicyPaths()
	local, err := readTunnelPolicyFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := effectiveTunnelPolicy(current, local, map[string]bool{"widget/web": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range merged {
		if e.Key.Name == "first" && (e.Policy.Provider != "devtunnel" || e.Scope != "collection") {
			t.Fatal("collection replacement lost", e)
		}
		if e.Key.Name == "second" && !e.Disabled {
			t.Fatal("project tombstone lost")
		}
	}
	if _, err = os.Stat(filepath.Join(b.Collection, ".config", "tunnels.toml")); !os.IsNotExist(err) {
		t.Fatal("one collection's override leaked to peer")
	}
}

func TestTunnelRemoteHarnessProjectOwnsLocalDesiredPolicy(t *testing.T) {
	root := realPath(t, t.TempDir())
	source := filepath.Join(root, "source")
	fixtureFile(t, filepath.Join(source, "taba.toml"), "# Immutable synthetic harness policy\n", 0644)
	fixtureFile(t, filepath.Join(source, ".harness-repos.yml"), "repos:\n  - name: widget\n    remote: https://github.com/example/widget.git\n    default_ref: origin/main\n", 0644)
	fixtureGit(t, "-C", source, "init", "-q", "-b", "main")
	fixtureGit(t, "-C", source, "add", "-A")
	fixtureGit(t, "-C", source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic harness")
	owner := filepath.Join(root, ".bare/harness.git")
	fixtureGit(t, "clone", "-q", "--bare", source, owner)
	fixtureFile(t, filepath.Join(root, ProjectDescriptorFile), "schema_version=1\nmode='harness'\n[harness]\nowner='.bare/harness.git'\nref='main'\n", 0644)
	c := tunnelProjectMember(t, root, "task")
	if c.Project == nil || c.Project.Source.Owner == "" {
		t.Fatal("fixture did not select immutable harness")
	}
	before := fixtureGit(t, "--git-dir="+owner, "rev-parse", "main")
	entry := testTunnelDefinition().Entry.Policy
	if err := c.SetTunnelPolicy("widget", "web", "preview", entry, true); err != nil {
		t.Fatal("immutable harness incorrectly prohibited local project policy", err)
	}
	policy, err := readTunnelPolicyFile(filepath.Join(root, ".config/tunnels.toml"))
	if err != nil || len(policy.Entries) != 1 {
		t.Fatal(err)
	}
	if after := fixtureGit(t, "--git-dir="+owner, "rev-parse", "main"); after != before {
		t.Fatal("harness snapshot ref changed")
	}
	if got := fixtureGit(t, "--git-dir="+owner, "ls-tree", "-r", "main", "--", ".config/tunnels.toml"); got != "" {
		t.Fatal("policy was written into harness snapshot")
	}
	if _, err = os.Stat(filepath.Join(source, ".config/tunnels.toml")); !os.IsNotExist(err) {
		t.Fatal("harness worktree changed")
	}
	// An immutable source reference without an actual collection/receiver is
	// not a project write grant and must refuse before creating any lock.
	invalid := &Context{Collection: root, Project: c.Project}
	if err = invalid.SetTunnelPolicy("widget", "web", "other", entry, true); err == nil {
		t.Fatal("snapshot-only context acquired write authority")
	}
	if _, err = os.Stat(filepath.Join(root, ".wtc-runtime.lock")); !os.IsNotExist(err) {
		t.Fatal("snapshot-only refusal wrote a collection lock")
	}
}

func TestTunnelCollectionSelectionPreservesInheritedDefinitionAndRegistrationConsent(t *testing.T) {
	p := localProject(t)
	c := tunnelProjectMember(t, p.Root, "task")
	entry := testTunnelDefinition().Entry.Policy
	if err := c.SetTunnelPolicy("widget", "web", "preview", entry, true); err != nil {
		t.Fatal(err)
	}
	plan := &runtimeManifest{Tasks: []runtimeTask{{RuntimeItem: RuntimeItem{Repo: "widget", Dir: "ext.widget", Group: "ext.widget/web", Kind: "service"}}}}
	entries, err := c.effectiveTunnels(plan)
	if err != nil {
		t.Fatal(err)
	}
	d := testTunnelDefinition()
	d.Entry = entries[0]
	before, err := d.digest()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.setTunnelStart(testTunnelKey(), false); err != nil {
		t.Fatal(err)
	}
	entries, err = c.effectiveTunnels(plan)
	if err != nil || len(entries) != 1 || entries[0].Selected == nil || *entries[0].Selected {
		t.Fatal("off selection missing", entries, err)
	}
	d.Entry = entries[0]
	after, err := d.digest()
	if err != nil || after != before || entries[0].Scope != "project" {
		t.Fatal("stop selection replaced inherited registration definition/approval", err)
	}
	_, local, _ := c.tunnelPolicyPaths()
	policy, err := readTunnelPolicyFile(local)
	if err != nil || len(policy.Entries) != 0 || len(policy.Starts) != 1 {
		t.Fatal("selection became a definition override", err)
	}
}
