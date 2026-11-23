package wtc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatusFullRefreshLogsWhatTheListMissed(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "0")
	listPath := filepath.Join(t.TempDir(), "list.json")
	views := filepath.Join(t.TempDir(), "views")
	open := `[{"number":7,"title":"Listed","state":"OPEN","updatedAt":"2026-10-01T00:00:00Z","reviewDecision":"APPROVED","headRefName":"topic","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}]`
	if err := os.WriteFile(listPath, []byte(open), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_TEST_GH_LIST", listPath)
	t.Setenv("WTC_TEST_GH_VIEWS", views)
	script := `#!/bin/sh
case "$2" in
  list) cat "$WTC_TEST_GH_LIST" ;;
  view) printf 'viewed\n' >> "$WTC_TEST_GH_VIEWS"; printf '%s\n' '{"number":7,"state":"OPEN","title":"Listed","reviewDecision":"APPROVED","statusCheckRollup":[{"conclusion":"FAILURE","status":"COMPLETED"}]}' ;;
  *) exit 2 ;;
esac
`
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	slug := "example/widget"
	record := PRRecord{Number: "7"}
	incremental := statusEnrichRecord(record, slug, "github.com")
	if incremental.Checks != "SUCCESS" || statusTestFileLines(t, views) != 0 {
		t.Fatalf("incremental read fetched the pull request: %+v views=%d", incremental, statusTestFileLines(t, views))
	}
	t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
	full := statusEnrichRecord(record, slug, "github.com")
	if full.Checks != "FAILURE" || statusTestFileLines(t, views) != 1 {
		t.Fatalf("full refresh did not replace the list: %+v views=%d", full, statusTestFileLines(t, views))
	}
	logText := statusReadAuditLog(t)
	if !strings.Contains(logText, "incremental miss github.com example/widget #7 stamp checks:SUCCESS->FAILURE") {
		t.Fatalf("miss was not logged: %s", logText)
	}
	if again := statusEnrichRecord(record, slug, "github.com"); again.Checks != "FAILURE" || statusTestFileLines(t, views) != 1 {
		t.Fatalf("full refresh repeated inside the interval: %+v views=%d", again, statusTestFileLines(t, views))
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(statusForgeEntryPath("full", "github.com", slug, "7"), past, past); err != nil {
		t.Fatal(err)
	}
	if agreed := statusEnrichRecord(record, slug, "github.com"); agreed.Checks != "FAILURE" || statusTestFileLines(t, views) != 2 {
		t.Fatalf("aged full refresh did not run: %+v views=%d", agreed, statusTestFileLines(t, views))
	}
	if strings.Count(statusReadAuditLog(t), "incremental miss") != 1 {
		t.Fatalf("an agreeing full refresh was logged as a miss: %s", statusReadAuditLog(t))
	}
}

func TestStatusFullRefreshLogsAnOpenPullRequestMissingFromTheList(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
	views := filepath.Join(t.TempDir(), "views")
	t.Setenv("WTC_TEST_GH_VIEWS", views)
	script := `#!/bin/sh
case "$2" in
  list) printf '[]\n' ;;
  view) printf 'viewed\n' >> "$WTC_TEST_GH_VIEWS"; printf '%s\n' '{"number":7,"state":"OPEN","title":"Still open"}' ;;
  *) exit 2 ;;
esac
`
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	detail := statusEnrichRecord(PRRecord{Number: "7"}, "example/widget", "github.com")
	if detail.State != "OPEN" || !strings.Contains(statusReadAuditLog(t), "list-omitted state:OPEN") {
		t.Fatalf("omitted open pull request was not recorded: %+v log=%s", detail, statusReadAuditLog(t))
	}
}

func TestStatusPipelineFullRefreshLogsWhatThePageMissed(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	hits, targeted := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Query().Get("target.branch") == "main" {
			targeted++
			w.Write([]byte(`{"values":[{"build_number":12,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"ccccccc"}}}]}`))
			return
		}
		w.Write([]byte(`{"values":[{"build_number":11,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`))
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	first, err := statusBBBuild("team/widget", "main")
	if err != nil || first == nil || *first.Build != "12" || *first.Checks != "FAILURE" || hits != 2 || targeted != 1 {
		t.Fatalf("full pipeline read = %+v hits=%d targeted=%d err=%v", first, hits, targeted, err)
	}
	logText := statusReadAuditLog(t)
	if !strings.Contains(logText, "incremental miss bitbucket.org team/widget main page build:11->12 checks:SUCCESS->FAILURE") {
		t.Fatalf("pipeline miss was not logged: %s", logText)
	}
	second, err := statusBBBuild("team/widget", "main")
	if err != nil || second == nil || *second.Build != "12" || hits != 2 || targeted != 1 {
		t.Fatalf("pipeline audit repeated inside the interval: %+v hits=%d targeted=%d err=%v", second, hits, targeted, err)
	}
	t.Setenv("WTC_FORGE_CACHE_AGE", "1")
	past := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(statusForgeEntryPath("pipelines", "bitbucket.org", "team/widget", "recent"), past, past); err != nil {
		t.Fatal(err)
	}
	third, err := statusBBBuild("team/widget", "main")
	if err != nil || third == nil || *third.Build != "12" || *third.Checks != "FAILURE" || hits != 3 || targeted != 1 {
		t.Fatalf("a fresh copy of the same page replaced the audited build: %+v hits=%d targeted=%d err=%v", third, hits, targeted, err)
	}
}

func TestStatusDuePipelineAuditKeepsTheCorrection(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "60")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	var targeted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("target.branch") == "main" {
			targeted.Add(1)
			w.Write([]byte(`{"values":[{"build_number":12,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"ccccccc"}}}]}`))
			return
		}
		w.Write([]byte(`{"values":[{"build_number":11,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`))
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	first, err := statusBBBuild("team/widget", "main")
	if err != nil || first == nil || *first.Build != "12" || targeted.Load() != 1 {
		t.Fatalf("audit = %+v targeted=%d err=%v", first, targeted.Load(), err)
	}
	past := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(statusForgeEntryPath("full", "bitbucket.org", "team/widget", "build:main"), past, past); err != nil {
		t.Fatal(err)
	}
	statusForgeCool("bitbucket.org", time.Minute)
	kept, err := statusBBBuild("team/widget", "main")
	if err != nil || kept == nil || *kept.Build != "12" || *kept.Checks != "FAILURE" || targeted.Load() != 1 {
		t.Fatalf("expired audit fell back to the page: %+v targeted=%d err=%v", kept, targeted.Load(), err)
	}
}

func TestStatusPipelineAuditFetchesOnceAcrossCallers(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "60")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	var targeted atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("target.branch") == "main" {
			targeted.Add(1)
			once.Do(func() { close(started) })
			<-release
			w.Write([]byte(`{"values":[{"build_number":12,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"ccccccc"}}}]}`))
			return
		}
		w.Write([]byte(`{"values":[{"build_number":11,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`))
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	errc := make(chan error, 2)
	for range 2 {
		go func() {
			build, err := statusBBBuild("team/widget", "main")
			if err != nil || build == nil || *build.Build != "12" {
				errc <- fmt.Errorf("build=%v err=%v", build, err)
				return
			}
			errc <- nil
		}()
	}
	<-started
	time.Sleep(50 * time.Millisecond)
	if targeted.Load() != 1 {
		t.Fatalf("targeted audits before release = %d", targeted.Load())
	}
	close(release)
	for range 2 {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	if targeted.Load() != 1 {
		t.Fatalf("targeted audits = %d", targeted.Load())
	}
}

func TestStatusPullRequestAuditFetchesOnceAcrossCallers(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "60")
	dir := t.TempDir()
	views := filepath.Join(dir, "views")
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	t.Setenv("WTC_TEST_VIEWS", views)
	t.Setenv("WTC_TEST_STARTED", started)
	t.Setenv("WTC_TEST_RELEASE", release)
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf 'x\\n' >> \"$WTC_TEST_VIEWS\"\ntouch \"$WTC_TEST_STARTED\"\nwhile [ ! -f \"$WTC_TEST_RELEASE\" ]; do sleep 0.01; done\nprintf '%s\\n' '{\"number\":7,\"state\":\"OPEN\",\"title\":\"Audited\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	record := PRRecord{Number: "7", Title: "Saved"}
	errc := make(chan error, 2)
	for range 2 {
		go func() {
			detail := statusConfirmFullPR(record, "example/widget", "github.com", statusPRDetail{Number: "7", State: "OPEN", Title: "Saved"}, "stamp")
			if detail.Title != "Audited" {
				errc <- fmt.Errorf("detail=%+v", detail)
				return
			}
			errc <- nil
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatal("audit did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if statusTestFileLines(t, views) != 1 {
		t.Fatalf("views = %d", statusTestFileLines(t, views))
	}
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	if statusTestFileLines(t, views) != 1 {
		t.Fatalf("views = %d", statusTestFileLines(t, views))
	}
}

func TestStatusEmptyPipelineAuditStaysEmpty(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "60")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	var targeted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("target.branch") == "main" {
			targeted.Add(1)
			w.Write([]byte(`{"values":[]}`))
			return
		}
		w.Write([]byte(`{"values":[{"build_number":11,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`))
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	first, err := statusBBBuild("team/widget", "main")
	if err != nil || first != nil || targeted.Load() != 1 {
		t.Fatalf("empty audit = %+v targeted=%d err=%v", first, targeted.Load(), err)
	}
	second, err := statusBBBuild("team/widget", "main")
	if err != nil || second != nil || targeted.Load() != 1 {
		t.Fatalf("empty audit was replaced by the page: %+v targeted=%d err=%v", second, targeted.Load(), err)
	}
	past := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(statusForgeEntryPath("full", "bitbucket.org", "team/widget", "build:main"), past, past); err != nil {
		t.Fatal(err)
	}
	statusForgeCool("bitbucket.org", time.Minute)
	kept, err := statusBBBuild("team/widget", "main")
	if err != nil || kept != nil || targeted.Load() != 1 {
		t.Fatalf("failed audit revived the page build: %+v targeted=%d err=%v", kept, targeted.Load(), err)
	}
}

func TestStatusOmittedPullRequestFetchIsShared(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "60")
	dir := t.TempDir()
	views := filepath.Join(dir, "views")
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	t.Setenv("WTC_TEST_VIEWS", views)
	t.Setenv("WTC_TEST_STARTED", started)
	t.Setenv("WTC_TEST_RELEASE", release)
	bin := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  list) printf '%s\n' '[]' ;;
  view) printf 'x\n' >> "$WTC_TEST_VIEWS"; touch "$WTC_TEST_STARTED"; while [ ! -f "$WTC_TEST_RELEASE" ]; do sleep 0.01; done; printf '%s\n' '{"number":7,"state":"MERGED","title":"Audited"}' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	statusWriteForgeCache("github.com", "example/widget", "7", []byte(`{"number":7,"state":"OPEN","title":"Saved"}`))
	record := PRRecord{Number: "7", Title: "Saved"}
	errc := make(chan error, 2)
	for range 2 {
		go func() {
			detail := statusEnrichRecord(record, "example/widget", "github.com")
			if detail.State != "MERGED" || detail.Title != "Audited" {
				errc <- fmt.Errorf("detail=%+v", detail)
				return
			}
			errc <- nil
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatal("omitted fetch did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if statusTestFileLines(t, views) != 1 {
		t.Fatalf("views = %d", statusTestFileLines(t, views))
	}
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	if statusTestFileLines(t, views) != 1 {
		t.Fatalf("views = %d", statusTestFileLines(t, views))
	}
	cached, ok := statusReadCachedDetail(record, "example/widget", "github.com")
	if !ok || cached.State != "MERGED" || cached.Title != "Audited" {
		t.Fatalf("saved detail = %+v ok=%v", cached, ok)
	}
}

func TestStatusListedPullRequestAuditIsNotOverwritten(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_FULL_REFRESH", "60")
	dir := t.TempDir()
	views := filepath.Join(dir, "views")
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	t.Setenv("WTC_TEST_VIEWS", views)
	t.Setenv("WTC_TEST_STARTED", started)
	t.Setenv("WTC_TEST_RELEASE", release)
	bin := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  list) printf '%s\n' '[{"number":7,"state":"OPEN","title":"List","updatedAt":"2026-10-01T00:00:00Z","headRefName":"topic"}]' ;;
  view) printf 'x\n' >> "$WTC_TEST_VIEWS"; touch "$WTC_TEST_STARTED"; while [ ! -f "$WTC_TEST_RELEASE" ]; do sleep 0.01; done; printf '%s\n' '{"number":7,"state":"OPEN","title":"Audited"}' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	publishReady := make(chan struct{})
	var once sync.Once
	releasePublish := func() { once.Do(func() { close(publishReady) }) }
	defer releasePublish()
	var publishers atomic.Int32
	statusBeforeOpenPublish = func() {
		if publishers.Add(1) > 1 {
			<-publishReady
		}
	}
	t.Cleanup(func() { statusBeforeOpenPublish = nil })
	record := PRRecord{Number: "7", Title: "Saved"}
	errc := make(chan error, 2)
	for range 2 {
		go func() {
			detail := statusEnrichRecord(record, "example/widget", "github.com")
			if detail.Title != "Audited" {
				errc <- fmt.Errorf("detail=%+v", detail)
				return
			}
			errc <- nil
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatal("audit did not start")
	}
	arrived := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(arrived) && publishers.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if statusTestFileLines(t, views) != 1 {
		t.Fatalf("views = %d", statusTestFileLines(t, views))
	}
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	saved := time.Now().Add(2 * time.Second)
	for time.Now().Before(saved) {
		cached, ok := statusReadCachedDetail(record, "example/widget", "github.com")
		if ok && cached.Title == "Audited" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	releasePublish()
	for range 2 {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	if statusTestFileLines(t, views) != 1 {
		t.Fatalf("views = %d", statusTestFileLines(t, views))
	}
	cached, ok := statusReadCachedDetail(record, "example/widget", "github.com")
	if !ok || cached.Title != "Audited" {
		t.Fatalf("saved detail = %+v ok=%v", cached, ok)
	}
}

func statusReadAuditLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(statusIncrementalMissLog())
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestStatusOlderListDoesNotReplaceANewerPullRequest(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	slug, forge := "example/widget", "github.com"
	record := PRRecord{Number: "7"}
	audited := []byte(`{"number":7,"state":"OPEN","title":"Listed","statusCheckRollup":[{"conclusion":"FAILURE","status":"COMPLETED"}]}`)
	listed := []byte(`{"number":7,"state":"OPEN","title":"Listed","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}`)
	auditStarted := time.Now()
	statusWriteForgeEntrySince("pr", forge, slug, record.Number, audited, auditStarted)

	older := statusOpenPR{Number: "7", Fingerprint: "old", Raw: listed, Started: auditStarted.Add(-time.Minute)}
	detail, used, _ := statusDetailFromOpenItem(record, slug, forge, older)
	if !used || detail.Checks != "FAILURE" {
		t.Fatalf("older list replaced the audit: used=%v detail=%+v", used, detail)
	}
	if saved, _ := statusReadForgeEntryAny("pr", forge, slug, record.Number); string(saved) != string(audited) {
		t.Fatalf("saved body = %s", saved)
	}

	newer := statusOpenPR{Number: "7", Fingerprint: "new", Raw: listed, Started: auditStarted.Add(time.Second)}
	if detail, _, _ := statusDetailFromOpenItem(record, slug, forge, newer); detail.Checks != "SUCCESS" {
		t.Fatalf("newer list was not published: %+v", detail)
	}
}

func TestStatusOlderListDoesNotReplaceANewerPullRequestAfterALostReceipt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	slug, forge := "example/widget", "github.com"
	record := PRRecord{Number: "7"}
	listed := []byte(`{"number":7,"state":"OPEN","title":"Listed","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}`)
	refreshed := []byte(`{"number":7,"state":"OPEN","title":"Listed","statusCheckRollup":[{"conclusion":"FAILURE","status":"COMPLETED"}]}`)
	order := map[string]int{"": 0, string(listed): 1, string(refreshed): 2}
	listStarted := time.Now()

	// A refresh starts after the list was read, and its receipt cannot be written.
	receipt := statusForgeEntryPath("since", forge, slug, "pr:7")
	if err := os.MkdirAll(receipt, 0700); err != nil {
		t.Fatal(err)
	}
	written := statusWriteForgeEntrySince("pr", forge, slug, record.Number, refreshed, listStarted.Add(time.Second))
	if err := os.Remove(receipt); err != nil {
		t.Fatal(err)
	}
	before, _ := statusReadForgeEntryAny("pr", forge, slug, record.Number)
	if written != (string(before) == string(refreshed)) {
		t.Fatalf("refresh reported written=%v, entry holds %s", written, before)
	}

	listedItem := statusOpenPR{Number: "7", Fingerprint: "listed", Raw: listed, Started: listStarted}
	statusDetailFromOpenItem(record, slug, forge, listedItem)
	if after, _ := statusReadForgeEntryAny("pr", forge, slug, record.Number); order[string(after)] < order[string(before)] {
		t.Fatalf("the list read before the refresh replaced its response: %s", after)
	}
}

func TestStatusOlderPipelinePageDoesNotReplaceANewerBuild(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	slug := "team/widget"
	audited := []byte(`{"values":[{"build_number":13,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`)
	page := []byte(`{"values":[{"build_number":12,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"aaaaaaa"}}}]}`)
	auditStarted := time.Now()
	statusWriteForgeEntrySince("build", "bitbucket.org", slug, "main", audited, auditStarted)

	statusSaveLivePipelinePage(slug, page, auditStarted.Add(-time.Second))
	if saved, _ := statusReadForgeEntryAny("build", "bitbucket.org", slug, "main"); string(saved) != string(audited) {
		t.Fatalf("older page replaced the audited build: %s", saved)
	}
	statusSaveLivePipelinePage(slug, page, auditStarted.Add(time.Second))
	if saved, _ := statusReadForgeEntryAny("build", "bitbucket.org", slug, "main"); string(saved) != string(page) {
		t.Fatalf("newer page was not saved: %s", saved)
	}
}

func TestStatusOlderPipelinePageShowsTheNewerSavedBuild(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	slug := "team/widget"
	page := []byte(`{"values":[{"build_number":12,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"aaaaaaa"}}}]}`)
	audited := []byte(`{"values":[{"build_number":13,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`)
	pageStarted := time.Now()
	statusWriteForgeEntrySince("pipelines", "bitbucket.org", slug, "recent", page, pageStarted)
	statusWriteForgeEntrySince("build", "bitbucket.org", slug, "main", audited, pageStarted.Add(time.Second))
	statusMarkFullRefresh("bitbucket.org", slug, "build:main")

	build, err := statusBBBuild(slug, "main")
	if err != nil || build == nil || *build.Build != "13" {
		t.Fatalf("older page shadowed the saved build: %+v err=%v", build, err)
	}
}
