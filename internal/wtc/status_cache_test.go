package wtc

import (
	"context"
	"fmt"
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
	for _, kind := range []string{"pr", "merge"} {
		if err := os.Chtimes(statusForgeEntryPath(kind, forge, slug, number), beforeDay, beforeDay); err != nil {
			t.Fatal(err)
		}
	}
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); detail.State != "MERGED" {
		t.Fatalf("merged result expired before a day: %+v", detail)
	}
	afterDay := time.Now().Add(-25 * time.Hour)
	for _, kind := range []string{"pr", "merge"} {
		if err := os.Chtimes(statusForgeEntryPath(kind, forge, slug, number), afterDay, afterDay); err != nil {
			t.Fatal(err)
		}
	}
	// A merge is final: past a day it is still shown, and not read again.
	t.Setenv("WTC_TEST_GH_DETAIL", `{"number":7,"state":"MERGED","title":"Refreshed detail"}`)
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); detail.State != "MERGED" || detail.Title == "Refreshed detail" {
		t.Fatalf("merged PR read again after a day: %+v", detail)
	}
	// Use a separate PR identity for the unrelated active-cache expiry checks.
	number = "8"
	path = statusForgeCachePath(forge, slug, number)
	t.Setenv("WTC_TEST_GH_DETAIL", "")
	cache(`{"number":8,"state":"OPEN"}`)
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); !statusObservationUnavailable(detail.Freshness) {
		t.Fatalf("stale active result was trusted: %+v", detail)
	}
	cache(`{"number":8,"state":"MERGED"}`)
	t.Setenv("WTC_FORGE_CACHE_AGE", "1")
	if detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge); !statusObservationUnavailable(detail.Freshness) {
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

// One shared open-PR list per repository serves every pane for its forge's
// age: 90 s on GitHub, 180 s on Bitbucket, or the explicit override for both.
// A PR row from that list stays current, also on screen, for as long.
func TestStatusOpenListAgeIsPerForge(t *testing.T) {
	const slug = "example/widget"
	for _, tc := range []struct {
		forge, override string
		age             time.Duration
		reread          bool
	}{
		{"github.com", "", 120 * time.Second, true},
		{"bitbucket.org", "", 120 * time.Second, false},
		{"bitbucket.org", "", 180 * time.Second, true},
		{"bitbucket.org", "90", 120 * time.Second, true},
	} {
		t.Run(fmt.Sprintf("%s/%q/%s", tc.forge, tc.override, tc.age), func(t *testing.T) {
			calls := foreground424Fixture(t, "exit 99\n")
			t.Setenv("WTC_FORGE_CACHE_AGE", tc.override)
			observed := time.Now().Add(-tc.age)
			item := []byte(`{"id":7,"number":7,"state":"OPEN","title":"Listed"}`)
			list, _ := statusEncodeOpenPRs([]statusOpenPR{{Number: "7", Raw: item, Fingerprint: "listed"}}, false, observed)
			statusWriteForgeEntrySinceObserved("openprs", tc.forge, slug, "open", list, observed, observed)
			reads := 0
			_, live, _, err := statusFetchSharedAdmissionContext(context.Background(), "openprs", tc.forge, slug, "open", func() ([]byte, error) {
				reads++
				return list, nil
			})
			if err != nil || live != tc.reread || reads == 1 != tc.reread || foreground424Count(calls) != 0 {
				t.Fatalf("list %s old: live=%v reads=%d err=%v", tc.age, live, reads, err)
			}
			url := map[string]string{"github.com": "https://github.com/" + slug + "/pull/7", "bitbucket.org": "https://bitbucket.org/" + slug + "/pull-requests/7"}[tc.forge]
			want := "cached"
			if tc.reread {
				want = "stale"
			}
			if got := StatusPRPresentationFreshness("cached", observed.UTC().Format(time.RFC3339Nano), url, time.Now()); got != want {
				t.Fatalf("on screen: %q, want %q", got, want)
			}
		})
	}
}

// A Bitbucket PR row from a list within its 180 s age is current, not stale,
// and costs no provider read.
func TestStatusBitbucketListedRowStaysCurrentForListAge(t *testing.T) {
	const forge, slug = "bitbucket.org", "example/widget"
	calls := foreground424Fixture(t, "exit 99\n")
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	observed := time.Now().Add(-120 * time.Second)
	item := []byte(`{"id":7,"state":"OPEN","title":"Listed"}`)
	list, _ := statusEncodeOpenPRs([]statusOpenPR{{Number: "7", Raw: item, Fingerprint: "listed"}}, false, observed)
	statusWriteForgeEntrySinceObserved("openprs", forge, slug, "open", list, observed, observed)
	got := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
	if got.State != "OPEN" || got.Freshness != "cached" || foreground424Count(calls) != 0 {
		t.Fatalf("listed row %+v after %d provider calls", got, foreground424Count(calls))
	}
}
