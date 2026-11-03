package wtc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	unsetTabaEnvironment()
	statusTurnOffSharedForgeBudgetAndAudit()
	os.Exit(m.Run())
}

// A collection shell carries generated TABA_ variables, and readers prefer
// them; tests that set only a WTC_ name must not read the developer's values.
func unsetTabaEnvironment() {
	for _, setting := range os.Environ() {
		if name, _, _ := strings.Cut(setting, "="); strings.HasPrefix(name, "TABA_") {
			os.Unsetenv(name)
		}
	}
}

func statusTurnOffSharedForgeBudgetAndAudit() {
	os.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	os.Setenv("WTC_FORGE_FULL_REFRESH", "0")
}

func TestStatusRateLimitedRecognizesGitHubAndBitbucket(t *testing.T) {
	limited := []string{
		"Bitbucket pipelines API returned 429 Too Many Requests: Rate limit for this resource has been exceeded",
		"gh pr view 7: exit status 1: HTTP 403: API rate limit exceeded for user",
		"gh api: HTTP 429: You have exceeded a secondary rate limit. Retry-After: 30",
		"bb pr list: Too Many Requests",
	}
	for _, message := range limited {
		if !statusRateLimited(errorString(message)) {
			t.Fatalf("not treated as a rate limit: %s", message)
		}
	}
	if got := statusRetryAfter(errorString("HTTP 429: secondary rate limit Retry-After: 30")); got != 30*time.Second {
		t.Fatalf("retry after = %s", got)
	}
	if statusRateLimited(errorString("gh pr view 7: exit status 1: pull request not found")) ||
		statusRateLimited(errorString("gh pr view 429 --repo example/widget: exit status 1: pull request not found")) ||
		statusRateLimited(errorString("gh pr list --head topic-429-wip: exit status 1: branch not found")) {
		t.Fatal("a 429 inside the command arguments was treated as a rate limit")
	}
}

func TestStatusForgeCoolingServesStaleDetail(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	forge, slug, number := "github.com", "example/widget", "7"
	statusWriteForgeCache(forge, slug, number, []byte(`{"number":7,"state":"OPEN","title":"Cached"}`))
	past := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(statusForgeCachePath(forge, slug, number), past, past); err != nil {
		t.Fatal(err)
	}
	statusForgeCool(forge, time.Minute)
	detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge)
	if detail.State != "OPEN" || detail.Title != "Cached" {
		t.Fatalf("stale detail was dropped while the forge was cooling: %+v", detail)
	}
}

func TestStatusForgeBudgetStopsTheSecondCall(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	calls := 0
	fetch := func() ([]byte, error) {
		calls++
		return []byte(`{"ok":true}`), nil
	}
	if _, err := statusSingleFlight("probe", "bitbucket.org", "example/widget", "one", fetch); err != nil {
		t.Fatal(err)
	}
	if _, err := statusSingleFlight("probe", "bitbucket.org", "example/widget", "two", fetch); err != errForgeBudget {
		t.Fatalf("second call = %v, calls %d", err, calls)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestStatusBitbucketBuildIsCachedAndARateLimitKeepsIt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_CACHE_AGE", "1")
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits > 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("Rate limit for this resource has been exceeded"))
			return
		}
		w.Write([]byte(`{"values":[{"build_number":11,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`))
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	first, err := statusBBBuild("team/widget", "main")
	if err != nil || first == nil || *first.Build != "11" {
		t.Fatalf("first build = %+v, %v", first, err)
	}
	second, err := statusBBBuild("team/widget", "main")
	if err != nil || second == nil || *second.Build != "11" || hits != 1 {
		t.Fatalf("cached build = %+v, %v, hits %d", second, err, hits)
	}
	past := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(statusForgeEntryPath("pipelines", "bitbucket.org", "team/widget", "recent"), past, past); err != nil {
		t.Fatal(err)
	}
	kept, err := statusBBBuild("team/widget", "main")
	if err != nil || kept == nil || *kept.Build != "11" {
		t.Fatalf("rate limit dropped the saved build: %+v, %v", kept, err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d", hits)
	}
	if _, err := statusBBBuild("team/widget", "other"); err != errForgeCooling {
		t.Fatalf("cooling did not stop the next repository: %v", err)
	}
	if hits != 2 {
		t.Fatalf("cooling still called Bitbucket, hits %d", hits)
	}
}

func TestStatusIdenticalLiveResponseStaysFresh(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_CACHE_AGE", "1")
	calls := 0
	fetch := func() ([]byte, error) {
		calls++
		return []byte(`{"ok":true}`), nil
	}
	if _, err := statusSingleFlight("probe", "github.com", "example/widget", "same", fetch); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Second)
	path := statusForgeEntryPath("probe", "github.com", "example/widget", "same")
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := statusSingleFlight("probe", "github.com", "example/widget", "same", fetch); err != nil || calls != 2 {
		t.Fatalf("expired identical body was not refetched: calls=%d err=%v", calls, err)
	}
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > time.Second {
		t.Fatalf("identical live response left the cache expired: %v", err)
	}
	if _, err := statusSingleFlight("probe", "github.com", "example/widget", "same", fetch); err != nil || calls != 2 {
		t.Fatalf("fresh identical entry was fetched again: calls=%d err=%v", calls, err)
	}
}

func TestStatusBuildBudgetIsNotAnErrorCell(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	statusForgeCool("github.com", time.Minute)
	build := (&Context{}).statusBuild("", statusBuildRequest{Forge: "github.com", Slug: "example/widget", Branch: "main"})
	if build != nil {
		t.Fatalf("a skipped build was shown as an error: %+v", build)
	}
}

func TestStatusPullRequestBudgetIsPending(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	statusForgeCool("github.com", time.Minute)
	detail := statusEnrichRecord(PRRecord{Number: "7", Title: "Waiting"}, "example/widget", "github.com")
	if detail.State == "UNKNOWN" || detail.Checks != "PENDING" || detail.Title != "Waiting" {
		t.Fatalf("a skipped pull request was shown as unknown: %+v", detail)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
