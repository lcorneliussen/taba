package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func runWTC(t *testing.T, args ...string) error {
	t.Helper()
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = append([]string{"wtc", "--json"}, args...)
	return run()
}

func TestInitThenRetireInPlaceOutsideHerdr(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("WTC_CONFIG_ROOT", "")
	t.Setenv("WTC_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	dir := filepath.Join(root, "task")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := runWTC(t, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".wtc", "config", "repos.yml")); err != nil {
		t.Fatalf("init wrote no local registry: %v", err)
	}
	for _, args := range [][]string{
		{"skills", "render", "--all"},
		{"mcp", "render", "--all"},
		{"env", "setup", "--all"},
		{"catch-up", "--all"},
		{"retire", "other"},
		{"status", "--no-watch", "other"},
		{"add-repo", "--collection", "other", "https://example.invalid/never-contacted.git"},
	} {
		if err := runWTC(t, args...); err == nil || !strings.Contains(err.Error(), "ad-hoc") {
			t.Fatalf("%v reached beyond an ad-hoc collection: %v", args, err)
		}
	}
	// browse refuses --json, so it runs without the flag runWTC adds.
	os.Args = []string{"wtc", "browse", "other"}
	if err := run(); err == nil || !strings.Contains(err.Error(), "ad-hoc") {
		t.Fatalf("browse reached beyond an ad-hoc collection: %v", err)
	}
	c, err := wtc.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openSelectedCollections(c, false, []string{"other"}); err == nil {
		t.Fatal("open of another collection was accepted")
	}
	if names, err := openSelectedCollections(c, false, []string{"task"}); err != nil || len(names) != 1 {
		t.Fatalf("open of the ad-hoc collection itself: %v %v", names, err)
	}
	if _, _, err := c.StatusLiveSnapshot(true, true); err == nil {
		t.Fatal("status --all swept an ad-hoc parent")
	}
	// Inside Herdr the usual handoff applies; outside it retires in place.
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SESSION", "")
	if err := runWTC(t, "retire", "."); err == nil || !strings.Contains(err.Error(), "Herdr pane") {
		t.Fatalf("inside Herdr, self-retire should hand off: %v", err)
	}
	t.Setenv("HERDR_ENV", "")
	if err := runWTC(t, "retire", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("collection folder remains: %v", err)
	}
}
