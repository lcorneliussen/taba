package wtc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusMergedPRCacheOutlivesActiveCache(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n[ -n \"$WTC_TEST_GH_DETAIL\" ] || exit 2\nprintf '%s\\n' \"$WTC_TEST_GH_DETAIL\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_TEST_GH_DETAIL", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	forge, slug, number := "github.com", "example/widget", "7"
	path := statusForgeCachePath(forge, slug, number)
	stale := time.Now().Add(-2 * time.Minute)
	cache := func(raw string) {
		statusWriteForgeCache(forge, slug, number, []byte(raw))
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	cache(`{"number":7,"state":"MERGED","mergedAt":"2026-09-30T12:00:00Z"}`)
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); detail.State != "MERGED" {
		t.Fatalf("stale merged result was needlessly refetched: %+v", detail)
	}
	beforeDay := time.Now().Add(-23 * time.Hour)
	if err := os.Chtimes(path, beforeDay, beforeDay); err != nil {
		t.Fatal(err)
	}
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); detail.State != "MERGED" {
		t.Fatalf("merged result expired before a day: %+v", detail)
	}
	afterDay := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(path, afterDay, afterDay); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_TEST_GH_DETAIL", `{"number":7,"state":"MERGED","title":"Refreshed detail"}`)
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); detail.Title != "Refreshed detail" {
		t.Fatalf("merged detail did not refresh after a day: %+v", detail)
	}
	// Active-cache expiry is a separate PR identity: the preceding confirmed
	// merge remains retained even if a released publisher replaces its detail.
	number = "8"
	path = statusForgeCachePath(forge, slug, number)
	t.Setenv("WTC_TEST_GH_DETAIL", "")
	cache(`{"number":8,"state":"OPEN"}`)
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); detail.State != "UNKNOWN" {
		t.Fatalf("stale active result was trusted: %+v", detail)
	}
	cache(`{"number":8,"state":"MERGED"}`)
	t.Setenv("WTC_FORGE_CACHE_AGE", "1")
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); detail.State != "UNKNOWN" {
		t.Fatalf("explicit short cache age was ignored: %+v", detail)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte(`{"number":8,"state":"MERGED"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); detail.State != "UNKNOWN" {
		t.Fatalf("merged cache followed symlink: %+v", detail)
	}
}

func TestStatusForgeCacheIsPrivateFreshAndDoesNotReadSymlinks(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_CACHE_AGE", "1")
	forge, slug, number := "github.com", "example/widget", "7"
	statusWriteForgeCache(forge, slug, number, []byte(`{"number":7,"state":"OPEN"}`))
	path := statusForgeCachePath(forge, slug, number)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("forge cache permissions: %o", info.Mode().Perm())
	}
	if data, ok := statusReadForgeCache(forge, slug, number); !ok || len(data) == 0 {
		t.Fatal("fresh cache was not read")
	}
	past := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if _, ok := statusReadForgeCache(forge, slug, number); ok {
		t.Fatal("stale forge cache was read")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("sensitive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, ok := statusReadForgeCache(forge, slug, number); ok {
		t.Fatal("symlink cache was read")
	}
}
