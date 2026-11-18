package wtc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func resourceCancellationFixture(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	t.Setenv("TABA_FORGE_MIN_INTERVAL", "")
	t.Setenv("WTC_FORGE_FULL_REFRESH", "60")
	t.Setenv("TABA_FORGE_FULL_REFRESH", "60")
}

func TestStatusResourceCanceledSnapshotNeverPublishes(t *testing.T) {
	resourceCancellationFixture(t)
	c := watcherCollection(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.WithStatusContext(ctx).WriteStatusSnapshot(StatusSnapshot{Schema: 1, Collection: filepath.Base(c.Collection)})
	if err == nil {
		t.Error("canceled snapshot publication succeeded")
	}
	for _, name := range []string{".wtc-status.json", ".wtc-status.md", ".last-wtc-status.yml"} {
		if _, err := os.Stat(filepath.Join(c.Collection, name)); !os.IsNotExist(err) {
			t.Errorf("canceled publication created %s", name)
		}
	}
}

func TestStatusResourceCanceledHookNeverDispatches(t *testing.T) {
	resourceCancellationFixture(t)
	c := watcherCollection(t)
	marker := filepath.Join(c.Collection, "dispatched")
	hook := filepath.Join(c.Collection, "hook")
	fixtureFile(t, hook, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'null\\n'\n", 0700)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.WithStatusContext(ctx).statusBuildFromHook(hook, statusBuildRequest{Branch: "main"})
	if err == nil {
		t.Error("canceled hook succeeded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("canceled hook dispatched")
	}
}

func TestStatusResourceCanceledBackgroundNeverDispatches(t *testing.T) {
	resourceCancellationFixture(t)
	bin := t.TempDir()
	marker := filepath.Join(bin, "dispatched")
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\ntouch '"+marker+"'\nprintf '[]\\n'\n", 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	statusRefreshRecord(ctx, StatusRefreshBackground, PRRecord{Number: "7"}, "example/widget", "github.com")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("canceled background collector dispatched")
	}
}

func TestStatusResourceHookDeadlineStopsDirectChild(t *testing.T) {
	resourceCancellationFixture(t)
	c := watcherCollection(t)
	hook := filepath.Join(c.Collection, "hook")
	fixtureFile(t, hook, "#!/bin/sh\nexec /bin/sleep 2\n", 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.WithStatusContext(ctx).statusBuildFromHook(hook, statusBuildRequest{Branch: "main"})
	if err == nil || ctx.Err() == nil {
		t.Fatalf("held hook did not cancel: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("direct hook child outlived caller deadline")
	}
}

func TestStatusResourceSnapshotDeadlineStopsLegacyGit(t *testing.T) {
	resourceCancellationFixture(t)
	c := watcherCollection(t)
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nexec /bin/sleep 2\n", 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.WithStatusContext(ctx).WriteStatusSnapshot(StatusSnapshot{Schema: 1, Collection: filepath.Base(c.Collection), Repos: []StatusRepo{{Dir: "widget", Worktree: c.Collection}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("publication outcome: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("legacy Git outlived publication deadline")
	}
	if _, err := os.Stat(filepath.Join(c.Collection, ".wtc-status.json")); !os.IsNotExist(err) {
		t.Fatal("canceled Git published snapshot")
	}
}

func TestStatusResourceRuntimeReceiverDeadline(t *testing.T) {
	resourceCancellationFixture(t)
	root := realPath(t, t.TempDir())
	source := filepath.Join(root, "source")
	fixtureFile(t, filepath.Join(source, "taba.toml"), "[secrets]\n", 0644)
	fixtureFile(t, filepath.Join(source, ".harness-repos.yml"), "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n", 0644)
	fixtureGit(t, "-C", source, "init", "-q", "-b", "main")
	fixtureGit(t, "-C", source, "add", "-A")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	fixtureGit(t, "clone", "-q", "--bare", source, filepath.Join(root, ".bare/harness.git"))
	fixtureFile(t, filepath.Join(root, ProjectDescriptorFile), "schema_version=1\nmode='harness'\n[harness]\nowner='.bare/harness.git'\nref='main'\n", 0644)
	member := filepath.Join(root, "task")
	fixtureFile(t, filepath.Join(member, CollectionDescriptorFile), "schema_version=1\nproject='..'\n", 0644)
	if _, err := OpenCollection(member); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "runtime-dispatched")
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nexec /bin/sleep 2\n", 0700)
	fixtureFile(t, filepath.Join(bin, "dekit"), "#!/bin/sh\ntouch '"+marker+"'\n", 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runtimeCommandContext(ctx, &runtimeManifest{Root: filepath.Join(member, ".wtc", "runtime"), Binary: filepath.Join(bin, "dekit")}, false, "ls")
	if err == nil || ctx.Err() == nil {
		t.Fatalf("receiver revalidation did not cancel: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("receiver revalidation lost caller deadline")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("canceled receiver dispatched runtime")
	}
}

func TestStatusResourceQueuedCollectorCannotDispatchLater(t *testing.T) {
	for _, lock := range []string{"entry", "budget"} {
		t.Run(lock, func(t *testing.T) {
			resourceCancellationFixture(t)
			path := statusForgeEntryPath("probe", "github.com", "example/widget", "main")
			if lock == "budget" {
				path = statusForgeStatePath("budget", "github.com")
			}
			unlock, err := statusLockCache(path)
			if err != nil {
				t.Fatal(err)
			}
			released := false
			defer func() {
				if !released {
					unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			var calls atomic.Int32
			done := make(chan error, 1)
			go func() {
				_, _, err := statusFetchSharedContext(ctx, "probe", "github.com", "example/widget", "main", func() ([]byte, error) { calls.Add(1); return []byte("response"), nil })
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("queued outcome: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("collector did not leave held lock")
			}
			unlock()
			released = true
			if calls.Load() != 0 {
				t.Fatal("canceled waiter dispatched")
			}
			if _, ok := statusReadForgeEntryAny("probe", "github.com", "example/widget", "main"); ok {
				t.Fatal("canceled waiter persisted")
			}
		})
	}
}

func TestStatusResourceOpenListCancellationBeforePublication(t *testing.T) {
	resourceCancellationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	statusBeforeOpenPublish = cancel
	defer func() { statusBeforeOpenPublish = nil }()
	statusDetailFromOpenItemContext(ctx, PRRecord{Number: "7"}, "example/widget", "github.com", statusOpenPR{Number: "7", Fingerprint: "synthetic", Raw: []byte(`{"number":7,"state":"OPEN"}`), Started: time.Now()})
	for _, kind := range []string{"pr", "stamp"} {
		if _, ok := statusReadForgeEntryAny(kind, "github.com", "example/widget", "7"); ok {
			t.Fatalf("canceled list persisted %s", kind)
		}
	}
}

func TestStatusResourceBitbucketTransportCancellation(t *testing.T) {
	for _, kind := range []string{"list", "build", "audit"} {
		t.Run(kind, func(t *testing.T) {
			resourceCancellationFixture(t)
			t.Setenv("BB_API_USER", "fixture")
			t.Setenv("BB_API_PASS", "synthetic")
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				if !ok || user != "fixture" || pass != "synthetic" {
					t.Error("lost selected auth")
				}
				entered <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			}))
			defer server.Close()
			old := statusBBAPI
			statusBBAPI = server.URL
			defer func() { statusBBAPI = old }()
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			start := time.Now()
			switch kind {
			case "list":
				_, _, _ = statusOpenPRsContext(ctx, "bitbucket.org", "example/widget")
			case "build":
				_, _ = statusBBBuildContext(ctx, "example/widget", "main")
			case "audit":
				statusConfirmFullBuildContext(ctx, "example/widget", "main", nil, "synthetic", "fixture", "synthetic")
			}
			select {
			case <-entered:
			default:
				t.Fatal("did not exercise local HTTP transport")
			}
			if ctx.Err() == nil || time.Since(start) > time.Second {
				t.Fatal("HTTP read did not honor caller cancellation")
			}
			for _, entry := range [][2]string{{"openprs", "open"}, {"pipelines", "recent"}, {"build", "main"}, {"full", "build:main"}} {
				if _, ok := statusReadForgeEntryAny(entry[0], "bitbucket.org", "example/widget", entry[1]); ok {
					t.Fatalf("canceled read published %v", entry)
				}
			}
		})
	}
}

func TestStatusResourceCanceledRecordLockKeepsCommittedMerge(t *testing.T) {
	for _, path := range []string{"listed", "detail"} {
		t.Run(path, func(t *testing.T) {
			resourceCancellationFixture(t)
			record, slug, forge := PRRecord{Number: "7"}, "example/widget", "github.com"
			if !statusWriteForgeEntrySince("pr", forge, slug, "7", []byte(`{"number":7,"state":"MERGED","title":"Confirmed"}`), time.Now()) {
				t.Fatal("merge setup")
			}
			before, _ := statusReadObservedDetail(record, slug, forge, time.Hour)
			unlock, err := statusLockCache(statusForgeEntryPath("pr", forge, slug, "7"))
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			var got statusPRDetail
			if path == "listed" {
				got = statusEnrichListedContext(ctx, record, slug, forge, statusOpenPR{})
			} else {
				got = statusViewPRWithLiveContext(ctx, record, slug, forge, true, false, nil)
			}
			if got.State != "MERGED" || got.ObservedAt != before.ObservedAt || got.Freshness != "unavailable" {
				t.Fatalf("canceled waiter lost confirmed authority: before=%+v after=%+v", before, got)
			}
		})
	}
}

func TestStatusResourceSharedReadSeparatesCommittedMergeFromCancellation(t *testing.T) {
	for _, point := range []string{"merge", "since"} {
		t.Run(point, func(t *testing.T) {
			resourceCancellationFixture(t)
			key := "7"
			if point == "since" {
				key = "pr:7"
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := status424CancelAfterFile{Context: parent, path: statusForgeEntryPath(point, "github.com", "example/widget", key), cancel: cancel}
			_, _, err := statusFetchSharedContext(ctx, "pr", "github.com", "example/widget", "7", func() ([]byte, error) { return []byte(`{"number":7,"state":"MERGED","title":"Confirmed"}`), nil })
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("request outcome concealed cancellation: %v", err)
			}
			got, known := statusReadObservedDetail(PRRecord{Number: "7"}, "example/widget", "github.com", time.Hour)
			if !known || got.State != "MERGED" || got.Title != "Confirmed" {
				t.Fatalf("committed terminal fact lost: %+v", got)
			}
			if statusWriteForgeEntrySince("pr", "github.com", "example/widget", "7", []byte(`{"number":7,"state":"OPEN"}`), time.Now()) {
				t.Fatal("compatibility writer reopened committed merge")
			}
		})
	}
}
