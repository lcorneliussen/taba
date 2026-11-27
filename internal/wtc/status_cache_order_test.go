package wtc

import (
	"os"
	"testing"
	"time"
)

// These replay the write orders that tools/formal/forge-cache-order checks:
// a list or page read before a newer response never replaces it.

func TestStatusOlderListDoesNotReplaceANewerPullRequest(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	slug, forge := "example/widget", "github.com"
	record := PRRecord{Number: "7"}
	newer := []byte(`{"number":7,"state":"OPEN","title":"Listed","statusCheckRollup":[{"conclusion":"FAILURE","status":"COMPLETED"}]}`)
	listed := []byte(`{"number":7,"state":"OPEN","title":"Listed","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}`)
	newerStarted := time.Now()
	statusWriteForgeEntrySince("pr", forge, slug, record.Number, newer, newerStarted)

	older := statusOpenPR{Number: "7", Fingerprint: "old", Raw: listed, Started: newerStarted.Add(-time.Minute)}
	detail, used, _ := statusDetailFromOpenItem(record, slug, forge, older)
	if !used || detail.Checks != "FAILURE" {
		t.Fatalf("older list replaced the newer response: used=%v detail=%+v", used, detail)
	}
	if saved, _ := statusReadForgeEntryAny("pr", forge, slug, record.Number); string(saved) != string(newer) {
		t.Fatalf("saved body = %s", saved)
	}

	newerList := statusOpenPR{Number: "7", Fingerprint: "new", Raw: listed, Started: newerStarted.Add(time.Second)}
	if detail, _, _ := statusDetailFromOpenItem(record, slug, forge, newerList); detail.Checks != "SUCCESS" {
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
	newer := []byte(`{"values":[{"build_number":13,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`)
	page := []byte(`{"values":[{"build_number":12,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"aaaaaaa"}}}]}`)
	newerStarted := time.Now()
	statusWriteForgeEntrySince("build", "bitbucket.org", slug, "main", newer, newerStarted)

	statusSaveLivePipelinePage(slug, page, newerStarted.Add(-time.Second))
	if saved, _ := statusReadForgeEntryAny("build", "bitbucket.org", slug, "main"); string(saved) != string(newer) {
		t.Fatalf("older page replaced the newer build: %s", saved)
	}
	statusSaveLivePipelinePage(slug, page, newerStarted.Add(time.Second))
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
	newer := []byte(`{"values":[{"build_number":13,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"ref_name":"main","selector":{"type":"branches"},"commit":{"hash":"bbbbbbb"}}}]}`)
	pageStarted := time.Now()
	statusWriteForgeEntrySince("pipelines", "bitbucket.org", slug, "recent", page, pageStarted)
	statusWriteForgeEntrySince("build", "bitbucket.org", slug, "main", newer, pageStarted.Add(time.Second))

	build, err := statusBBBuild(slug, "main")
	if err != nil || build == nil || *build.Build != "13" {
		t.Fatalf("older page shadowed the saved build: %+v err=%v", build, err)
	}
}
