package wtc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resourceCancellationFixture(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
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
