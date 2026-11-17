package wtc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusResourceCancelledGitDoesNotDispatch(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "dispatched")
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\ntouch \""+marker+"\"\n", 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gitOutputContext(ctx, "status"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cancelled read dispatched")
	}
}

func TestStatusResourceCancelledBaseReadStopsGit(t *testing.T) {
	dir := t.TempDir()
	fixtureFile(t, filepath.Join(dir, ".wtc", "repo-bases.toml"), "schema_version=1\n[repos.widget]\nbase='origin/beta'\nreason='Synthetic comparison'\n", 0600)
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nexec /bin/sleep 10\n", 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newRepositoryBaseSnapshotContext(ctx, dir, Registry{Repos: []Repo{{Name: "widget", DefaultRef: "origin/main"}}})
	if err == nil || ctx.Err() == nil {
		t.Fatalf("blocked read did not cancel: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancelled base read kept its Git child")
	}
}

func TestStatusResourceWatcherCancelledBeforeLock(t *testing.T) {
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	c := watcherCollection(t)
	unlock, err := c.lockStatusWatchers()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	cleanup, err := c.RegisterStatusWatcherContext(ctx, func() {})
	if cleanup != nil {
		cleanup()
		t.Fatal("cancelled waiter registered")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancel waited for lifecycle lock deadline")
	}
	entries, _ := os.ReadDir(filepath.Join(c.Collection, statusWatcherDir))
	if len(entries) != 0 {
		t.Fatal("queued cancellation created a lease")
	}
}

func TestStatusResourceWatcherCleanupDoesNotWaitForRetirementLock(t *testing.T) {
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	c := watcherCollection(t)
	cleanup, err := c.RegisterStatusWatcherContext(context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := c.lockStatusWatchers()
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	cleanup()
	if time.Since(start) > time.Second {
		t.Fatal("optional directory cleanup delayed lease release")
	}
	entries, _ := os.ReadDir(filepath.Join(c.Collection, statusWatcherDir))
	if len(entries) != 0 {
		t.Fatal("lease not removed before optional cleanup")
	}
	cleanup()
}

func TestStatusResourceReadContextRevalidatesRegistryAndScope(t *testing.T) {
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("TABA_CONFIG_ROOT", "")
	c := watcherCollection(t).WithStatusContext(context.Background())
	fixtureFile(t, c.registryPath(), "repos:\n  - name: widget\n    remote: https://example.invalid/widget.git\n    default_ref: origin/main\n", 0600)
	fresh, err := c.StatusReadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Registry.Repos) != 1 || len(c.Registry.Repos) != 0 {
		t.Fatal("refresh reused or mutated old registry")
	}
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection"), "WTC_CONFIG_ROOT="+filepath.Join(t.TempDir(), "unapproved-config")+"\n", 0600)
	if _, err := c.StatusReadContext(context.Background()); err == nil {
		t.Fatal("changed receiving policy was bypassed by an old context")
	}
}
