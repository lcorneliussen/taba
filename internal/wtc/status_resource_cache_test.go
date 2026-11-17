package wtc

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func resourceCountGit(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	fixtureFile(t, filepath.Join(dir, "git"), "#!/bin/sh\nprintf x >> \"$RESOURCE_GIT_COUNTER\"\nexec \"$RESOURCE_REAL_GIT\" \"$@\"\n", 0700)
	t.Setenv("RESOURCE_GIT_COUNTER", counter)
	t.Setenv("RESOURCE_REAL_GIT", real)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return counter
}
func resourceCalls(path string) int { data, _ := os.ReadFile(path); return len(data) }

func TestStatusResourceDeclarationsReuseAndInvalidate(t *testing.T) {
	c := runtimeFixture(t).WithStatusContext(context.Background())
	counter := resourceCountGit(t)
	first := c.RuntimeEnlisted()
	calls := resourceCalls(counter)
	if len(first) == 0 || calls == 0 {
		t.Fatalf("no real declaration inventory: %+v, %d", first, calls)
	}
	second := c.RuntimeEnlisted()
	if resourceCalls(counter) != calls || !reflect.DeepEqual(first, second) {
		t.Fatal("unchanged declaration read repeated Git inventory")
	}
	// Metadata-preserving content edits still invalidate the advisory cache.
	path := runtimeSharedFragment(filepath.Join(c.Collection, "widget"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n# changed declaration input\n")...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	c.RuntimeEnlisted()
	if resourceCalls(counter) <= calls {
		t.Fatal("changed declaration did not revalidate identity/inventory")
	}
	live := first[0]
	live.State, live.URL = "ready", "http://live.example.test:8080"
	got := c.runtimeWithDeclarations([]RuntimeItem{live})
	if got[0].State != "ready" || got[0].URL != live.URL {
		t.Fatal("cached declaration replaced live service facts")
	}
}

func TestStatusResourceCancelledDeclarationWaitDoesNotDispatch(t *testing.T) {
	c := runtimeFixture(t).WithStatusContext(context.Background())
	counter := resourceCountGit(t)
	<-c.runtimeDeclarationsCache.gate
	defer func() { c.runtimeDeclarationsCache.gate <- struct{}{} }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	c.WithStatusContext(ctx).cachedRuntimeDeclarations()
	if time.Since(start) > time.Second || resourceCalls(counter) != 0 {
		t.Fatal("cancelled coalesced consumer waited or dispatched")
	}
}

func TestStatusResourceSnapshotOutputDoesNotInvalidateDeclarations(t *testing.T) {
	c := runtimeFixture(t).WithStatusContext(context.Background())
	c.RuntimeEnlisted()
	before := c.StatusResourceSignature()
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-status.json"), "{}", 0600)
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-status.md"), "synthetic output", 0600)
	if after := c.StatusResourceSignature(); after != before {
		t.Fatal("observer output invalidated unchanged source inputs")
	}
}

func TestStatusResourceIndexChangesDoNotRediscoverDeclarations(t *testing.T) {
	c := runtimeFixture(t).WithStatusContext(context.Background())
	counter := resourceCountGit(t)
	c.RuntimeEnlisted()
	fixtureGit(t, "-C", filepath.Join(c.Collection, "widget"), "add", "README.md")
	before := resourceCalls(counter)
	c.RuntimeEnlisted()
	if resourceCalls(counter) != before {
		t.Fatal("Git index observation repeated unchanged declaration/identity inventory")
	}
}

func TestStatusResourceRuntimeDatePrecedesSlowDeclarationDelivery(t *testing.T) {
	c := runtimeFixture(t).WithStatusContext(context.Background())
	task := runtimeTask{RuntimeItem: RuntimeItem{Collection: filepath.Base(c.Collection), Repo: "widget", Dir: "widget", Path: "widget/web/main", Kind: "service"}}
	task.Log = filepath.Join(c.runtimeRoot(), "logs", "widget", "web", "main.log")
	manifest := runtimeManifest{Schema: 1, Root: c.runtimeRoot(), Binary: c.Config.Runtime.Binary, Tasks: []runtimeTask{task}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "manifest.json"), string(data), 0600)
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "running"), "", 0600)
	fixtureFile(t, filepath.Join(c.runtimeRoot(), "fake-tasks.json"), `{"tasks":[{"path":"widget/web/main","state":"ready"}]}`, 0600)
	bin := t.TempDir()
	entered := filepath.Join(bin, "entered")
	release := filepath.Join(bin, "release")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\ntouch \"$RESOURCE_ENTERED\"\nwhile [ ! -e \"$RESOURCE_RELEASE\" ]; do /bin/sleep .01; done\nexec \"$RESOURCE_REAL_GIT\" \"$@\"\n", 0700)
	t.Setenv("RESOURCE_ENTERED", entered)
	t.Setenv("RESOURCE_RELEASE", release)
	t.Setenv("RESOURCE_REAL_GIT", real)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	done := make(chan time.Time, 1)
	go func() { _, at := c.RuntimeStatusObserved(); done <- at }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.WriteFile(release, nil, 0600)
			<-done
			t.Fatal("declaration did not enter controlled Git")
		}
		time.Sleep(time.Millisecond)
	}
	barrier := time.Now()
	time.Sleep(80 * time.Millisecond)
	os.WriteFile(release, nil, 0600)
	at := <-done
	if at.After(barrier) {
		t.Fatal("slow declaration delivery renewed older runner observation time")
	}
}
