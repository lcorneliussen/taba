package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusEnrichRecordsKeepsIdentityAcrossOutOfOrderReplies(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	bin := t.TempDir()
	state := t.TempDir()
	t.Setenv("WTC_TEST_STATE", state)
	for _, name := range []string{"active", "max"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte("0"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	release := filepath.Join(state, "release")
	defer os.WriteFile(release, []byte("ok"), 0644)
	// The lock is a noclobber create (O_EXCL), not mkdir: the uutils mkdir on
	// Ubuntu 26.04 lets two racing processes both succeed.
	script := `#!/bin/sh
if [ "$2" = "list" ]; then
  printf '[]\n'
  exit 0
fi
state="$WTC_TEST_STATE"
while ! (set -C; : > "$state/lock") 2>/dev/null; do sleep 0.01; done
active=$(cat "$state/active")
active=$((active + 1))
printf '%s' "$active" > "$state/active.$$.tmp"
mv "$state/active.$$.tmp" "$state/active"
maximum=$(cat "$state/max")
if [ "$active" -gt "$maximum" ]; then
  printf '%s' "$active" > "$state/max.$$.tmp"
  mv "$state/max.$$.tmp" "$state/max"
fi
rm -f "$state/lock"
while [ ! -f "$state/release" ]; do sleep 0.01; done
while ! (set -C; : > "$state/lock") 2>/dev/null; do sleep 0.01; done
active=$(cat "$state/active")
printf '%s' "$((active - 1))" > "$state/active.$$.tmp"
mv "$state/active.$$.tmp" "$state/active"
rm -f "$state/lock"
printf '{"number":%s,"state":"OPEN","title":"Synthetic %s"}\n' "$3" "$3"
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var progress []string
	c := &Context{Registry: Registry{Repos: []Repo{{Name: "widget", Remote: "https://github.com/example/widget.git"}}},
		StatusProgress: func(message string) { progress = append(progress, message) }}
	records := make([]PRRecord, 6)
	for i := range records {
		records[i] = PRRecord{Repo: "widget", Number: fmt.Sprint(i + 1)}
	}
	type result struct {
		details []statusPRDetail
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		details, err := c.statusEnrichRecords(records, nil)
		finished <- result{details: details, err: err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(state, "active"))
		if err == nil && string(data) == "4" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	maximum, err := os.ReadFile(filepath.Join(state, "max"))
	if err != nil || string(maximum) != "4" {
		t.Fatalf("expected four overlapping calls and no fifth before release; max=%q err=%v", maximum, err)
	}
	if err := os.WriteFile(release, []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	var got result
	select {
	case got = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded forge calls did not finish")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	for i, detail := range got.details {
		want := fmt.Sprintf("Synthetic %d", i+1)
		if detail.Number != records[i].Number || detail.Title != want {
			t.Fatalf("detail %d crossed records: %+v", i, detail)
		}
	}
	if len(progress) != len(records)+1 || !strings.Contains(progress[len(progress)-1], "6/6") {
		t.Fatalf("missing completion progress: %v", progress)
	}
}

func TestMergedPRRegistryStopsRefreshingFinalFacts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("WTC_TEST_GH_CALLS", calls)
	script := `#!/bin/sh
printf 'called\n' >> "$WTC_TEST_GH_CALLS"
printf '%s\n' '{"number":7,"state":"MERGED","title":"Finished change","mergedAt":"2026-09-30T12:00:00Z","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}'
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := fixture(t)
	c.Registry.Repos = []Repo{{Name: "widget", Remote: "https://github.com/example/widget.git"}}
	if _, err := c.EnlistPR(PRRecord{Repo: "widget", Number: "7", Branch: "topic", Title: "Old title"}); err != nil {
		t.Fatal(err)
	}
	records, err := c.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	details, err := c.statusEnrichRecords(records, nil)
	if err != nil || details[0].State != "MERGED" || details[0].Checks != "SUCCESS" {
		t.Fatalf("initial merged detail: %+v, %v", details, err)
	}
	if count, err := c.recordMergedPRs(records, details); err != nil || count != 1 {
		t.Fatalf("record final merge: count=%d err=%v", count, err)
	}
	data, err := os.ReadFile(c.PRFile())
	if err != nil || !strings.Contains(string(data), "widget 7 topic https://github.com/example/widget/pull/7 Finished change") ||
		!strings.Contains(string(data), "# merged-pr widget 7 2026-09-30T12:00:00Z SUCCESS") {
		t.Fatalf("shell-readable registry lost final facts: %s %v", data, err)
	}
	if err := os.Remove(statusForgeCachePath("github.com", "example/widget", "7")); err != nil {
		t.Fatal(err)
	}
	var progress []string
	c.StatusProgress = func(message string) { progress = append(progress, message) }
	records, err = c.ListPRs()
	if err != nil || records[0].MergedOn != "2026-09-30T12:00:00Z" {
		t.Fatalf("merge annotation not read: %+v %v", records, err)
	}
	details, err = c.statusEnrichRecords(records, nil)
	if err != nil || details[0].State != "MERGED" || details[0].Title != "Finished change" || details[0].Checks != "SUCCESS" {
		t.Fatalf("recorded merge facts lost: %+v %v", details, err)
	}
	if len(progress) != 1 || progress[0] != "Using 1 recorded merges" {
		t.Fatalf("recorded merge was counted as a live check: %v", progress)
	}
	callData, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(callData), "called") != 2 {
		t.Fatalf("merged PR was fetched again: %s %v", callData, err)
	}
	if err := c.UnlistPR("widget", "7"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(c.PRFile())
	if err != nil || strings.Contains(string(data), "merged-pr widget 7") {
		t.Fatalf("unlist left orphaned merge annotation: %s %v", data, err)
	}
}

func TestStatusForgePreviewRecordsMergesOnlyWhenEnabled(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	c := newWorkspaceFixture(t)
	other, err := c.NewCollection(NewOptions{Slug: "other", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	otherContext, err := OpenCollection(other.Collection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherContext.EnlistPR(PRRecord{Repo: "widget", Number: "7", Branch: "topic"}); err != nil {
		t.Fatal(err)
	}
	otherRegistry := filepath.Join(other.Collection, "harness", ".harness-repos.yml")
	registryData, err := os.ReadFile(otherRegistry)
	if err != nil {
		t.Fatal(err)
	}
	updatedRegistry := strings.Replace(string(registryData), c.Registry.Repos[1].Remote, "https://github.com/example/widget.git", 1)
	if err := os.WriteFile(otherRegistry, []byte(updatedRegistry), 0644); err != nil {
		t.Fatal(err)
	}
	beforeOther, err := os.ReadFile(otherContext.PRFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(otherContext.PRFile() + ".lock"); err != nil {
		t.Fatal(err)
	}
	c.Registry.Repos = []Repo{{Name: "agent-harness", Remote: "https://github.com/example/harness.git"}}
	if _, err := c.EnlistPR(PRRecord{Repo: "agent-harness", Number: "7", Branch: "topic"}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' '{"number":7,"state":"MERGED","title":"Finished change","mergedAt":"2026-09-30T12:00:00Z","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}'
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := c.statusForgePreview(false, false); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(c.PRFile()); err != nil || strings.Contains(string(data), "\n# merged-pr ") {
		t.Fatalf("preview without merge recording changed registry: %s %v", data, err)
	}
	if _, _, err := c.StatusLiveSnapshot(true, true); err != nil {
		t.Fatal(err)
	}
	if afterOther, err := os.ReadFile(otherContext.PRFile()); err != nil || string(afterOther) != string(beforeOther) {
		t.Fatalf("workspace sweep changed another collection's registry: %s %v", afterOther, err)
	}
	if _, err := c.StatusForgePreview(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(c.PRFile()); err != nil || !strings.Contains(string(data), "# merged-pr agent-harness 7 2026-09-30T12:00:00Z SUCCESS") {
		t.Fatalf("scoped preview did not record final merge: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(c.Workspace, "other", ".wtc-prs.lock")); !os.IsNotExist(err) {
		t.Fatalf("preview wrote another collection's registry lock: %v", err)
	}
}

func TestMergedPRRegistryWaitsForSettledChecks(t *testing.T) {
	c := fixture(t)
	if _, err := c.EnlistPR(PRRecord{Repo: "widget", Number: "8", Branch: "topic"}); err != nil {
		t.Fatal(err)
	}
	records, err := c.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	details := []statusPRDetail{{Number: "8", State: "MERGED", MergedOn: "2026-09-30T12:00:00Z", Checks: "PENDING"}}
	if count, err := c.recordMergedPRs(records, details); err != nil || count != 0 {
		t.Fatalf("pending check was frozen: %d %v", count, err)
	}
	records, err = c.ListPRs()
	if err != nil || records[0].MergedOn != "" {
		t.Fatalf("pending PR stopped being live: %+v %v", records, err)
	}
	detail, err := statusGHDetail([]byte(`{"number":8,"state":"MERGED","mergedAt":"2026-09-30T12:00:00Z","statusCheckRollup":[{"conclusion":"FAILURE","status":"COMPLETED"},{"status":"IN_PROGRESS"}]}`), records[0])
	if err != nil || detail.Checks != "FAILURE" || !detail.ChecksUnsettled {
		t.Fatalf("failure with running checks lost pending marker: %+v %v", detail, err)
	}
	if count, err := c.recordMergedPRs(records, []statusPRDetail{detail}); err != nil || count != 0 {
		t.Fatalf("failure with running checks was frozen: %d %v", count, err)
	}
	fallback, err := statusGHDetail([]byte(`{"number":8,"state":"MERGED","updatedAt":"2026-09-30T15:00:00Z","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}`), records[0])
	if err != nil || fallback.MergedOn != "" {
		t.Fatalf("mutable updatedAt used as merge timestamp: %+v %v", fallback, err)
	}
	if count, err := c.recordMergedPRs(records, []statusPRDetail{fallback}); err != nil || count != 0 {
		t.Fatalf("mutable updatedAt was frozen as merge time: %d %v", count, err)
	}
}

func TestUnsettledMergedCacheRefreshesAfterLiveAge(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("WTC_TEST_GH_CALLS", calls)
	script := `#!/bin/sh
printf 'called\n' >> "$WTC_TEST_GH_CALLS"
printf '%s\n' '{"number":7,"state":"MERGED","mergedAt":"2026-09-30T12:00:00Z","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}'
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	forge, slug, number := "github.com", "example/widget", "7"
	statusWriteForgeCache(forge, slug, number, []byte(`{"number":7,"state":"MERGED","mergedAt":"2026-09-30T12:00:00Z","statusCheckRollup":[{"status":"IN_PROGRESS"}]}`))
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(statusForgeCachePath(forge, slug, number), old, old); err != nil {
		t.Fatal(err)
	}
	detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge)
	if detail.Checks != "SUCCESS" || detail.ChecksUnsettled {
		t.Fatalf("unsettled merged cache hid fresh forge result: %+v", detail)
	}
	data, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(data), "called") != 2 {
		t.Fatalf("unsettled merge was not refreshed: %s %v", data, err)
	}
	statusWriteForgeCache(forge, slug, number, []byte(`{"number":7,"state":"MERGED","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}`))
	if err := os.Chtimes(statusForgeCachePath(forge, slug, number), old, old); err != nil {
		t.Fatal(err)
	}
	detail = statusEnrichRecord(PRRecord{Repo: "widget", Number: number}, slug, forge)
	data, err = os.ReadFile(calls)
	if err != nil || detail.MergedOn != "2026-09-30T12:00:00Z" || strings.Count(string(data), "called") != 4 {
		t.Fatalf("untimed merged cache hid real merge time: %+v %s %v", detail, data, err)
	}
}

func TestBitbucketMergeActivitySuppliesAuthoritativeTime(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	statusHideBitbucketLogin(t)
	bin := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  view) printf '%s\n' '{"id":9,"state":"MERGED","updated_on":"2026-10-01T15:00:00Z","merge_commit":{"date":"2026-09-29T12:00:00Z"}}' ;;
  activity) printf '%s\n' '{"activities":[{"update":{"state":"MERGED","date":"2026-09-30T12:00:00.500000+00:00"}},{"update":{"state":"OPEN","date":"2026-09-29T12:00:00Z"}}]}' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: "9"}, "example/widget", "bitbucket.org")
	if detail.State != "MERGED" || detail.MergedOn != "2026-09-30T12:00:00Z" {
		t.Fatalf("Bitbucket merge event was not used: %+v", detail)
	}
}

func TestMergedPRRegistryRejectsMalformedFactsAndKeepsFinalAnnotationOnReenlist(t *testing.T) {
	c := fixture(t)
	registry := "widget 7 topic https://github.com/example/widget/pull/7 Previous title\n" +
		"# merged-pr widget 7 invalid SUCCESS\n" +
		"# merged-pr widget 7 2026-09-30T12:00:00Z BOGUS\n" +
		"# merged-pr widget 7 2026-09-30T12:00:00Z FAILURE\n"
	if err := os.WriteFile(c.PRFile(), []byte(registry), 0644); err != nil {
		t.Fatal(err)
	}
	records, err := c.ListPRs()
	if err != nil || len(records) != 1 || records[0].FinalChecks != "FAILURE" {
		t.Fatalf("valid final annotation was not read: %+v %v", records, err)
	}
	if _, err := c.EnlistPR(PRRecord{Repo: "widget", Number: "7", Branch: "corrected", URL: "https://github.com/example/widget/pull/7", Title: "Final title"}); err != nil {
		t.Fatal(err)
	}
	records, err = c.ListPRs()
	if err != nil || len(records) != 1 || records[0].Branch != "corrected" || records[0].MergedOn != "2026-09-30T12:00:00Z" {
		t.Fatalf("re-enlist lost final annotation: %+v %v", records, err)
	}
	if _, err := c.EnlistPR(PRRecord{Repo: "widget", Number: "8", Branch: "next", URL: "https://github.com/example/widget/pull/8"}); err != nil {
		t.Fatal(err)
	}
	records, err = c.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	count, err := c.recordMergedPRs(records, []statusPRDetail{{Number: "7", State: "MERGED", Checks: "FAILURE"},
		{Number: "8", State: "MERGED", MergedOn: "2026-09-30T13:00:00Z", Checks: "BOGUS"}})
	if err != nil || count != 0 {
		t.Fatalf("unknown checks were frozen: %d %v", count, err)
	}
	records, err = c.ListPRs()
	if err != nil || records[1].MergedOn != "" {
		t.Fatalf("unknown check state was recorded: %+v %v", records, err)
	}
}

func TestStatusGHDetailSeparatesChecksReviewAndMerge(t *testing.T) {
	raw := []byte(`{"number":7,"state":"OPEN","title":"Change widget","isDraft":false,"reviewDecision":"REVIEW_REQUIRED","mergeStateStatus":"BEHIND","reviewRequests":[{"login":"reviewer"}],"statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"},{"status":"IN_PROGRESS"}]}`)
	d, err := statusGHDetail(raw, PRRecord{Number: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Number != "7" || d.State != "OPEN" || d.Checks != "PENDING" || d.Merge != "BEHIND" || d.Review != "waiting" || d.Title != "Change widget" {
		t.Fatalf("wrong GitHub detail: %+v", d)
	}
	failed := []byte(`{"number":7,"state":"OPEN","statusCheckRollup":[{"conclusion":"FAILURE","status":"COMPLETED"},{"status":"IN_PROGRESS"}]}`)
	d, err = statusGHDetail(failed, PRRecord{Title: "fallback"})
	if err != nil || d.Checks != "FAILURE" || d.Review != "noreviewers" || d.Title != "fallback" {
		t.Fatalf("failed check or reviewer state lost: %+v %v", d, err)
	}
	pendingState := []byte(`{"number":7,"state":"OPEN","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"},{"state":"PENDING"}]}`)
	d, err = statusGHDetail(pendingState, PRRecord{})
	if err != nil || d.Checks != "PENDING" {
		t.Fatalf("pending state hidden by passing check: %+v %v", d, err)
	}
	stale := []byte(`{"number":7,"state":"MERGED","mergedAt":"2026-09-30T12:00:00Z","statusCheckRollup":[{"conclusion":"STALE","status":"COMPLETED"}]}`)
	d, err = statusGHDetail(stale, PRRecord{})
	if err != nil || d.Checks != "FAILURE" || d.ChecksUnsettled {
		t.Fatalf("stale terminal conclusion was not failed: %+v %v", d, err)
	}
	unknown := []byte(`{"number":7,"state":"MERGED","mergedAt":"2026-09-30T12:00:00Z","statusCheckRollup":[{"conclusion":"MYSTERY","status":"COMPLETED"}]}`)
	d, err = statusGHDetail(unknown, PRRecord{})
	if err != nil || d.Checks != "PENDING" || !d.ChecksUnsettled {
		t.Fatalf("unknown conclusion treated as settled: %+v %v", d, err)
	}
}

func TestStatusGHConflictIndependentOfChecksDraftAndLocalState(t *testing.T) {
	for _, tc := range []struct {
		name, state, mergeable, mergeState, want string
		draft                                    bool
	}{
		{"green checks", "OPEN", "CONFLICTING", "DIRTY", "CONFLICTING", false},
		{"draft label", "OPEN", "CONFLICTING", "DRAFT", "CONFLICTING", true},
		{"still computing", "OPEN", "UNKNOWN", "UNKNOWN", "UNKNOWN", false},
		{"blocked by policy", "OPEN", "MERGEABLE", "BLOCKED", "BLOCKED", false},
		{"legacy cached detail", "OPEN", "", "DIRTY", "DIRTY", false},
		{"merged overrides stale conflict", "MERGED", "CONFLICTING", "DIRTY", "MERGED", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"number":7,"state":%q,"isDraft":%t,"mergeable":%q,"mergeStateStatus":%q,"statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}`, tc.state, tc.draft, tc.mergeable, tc.mergeState))
			detail, err := statusGHDetail(raw, PRRecord{})
			if err != nil || detail.Merge != tc.want || detail.Checks != "SUCCESS" {
				t.Fatalf("merge conflict lost or check status changed: %+v %v", detail, err)
			}
			if tc.draft && detail.State != "DRAFT" {
				t.Fatalf("draft fact lost: %+v", detail)
			}
		})
	}
}

func TestStatusDraftMergedAndUnknownFacts(t *testing.T) {
	draft, err := statusGHDetail([]byte(`{"number":8,"state":"OPEN","isDraft":true}`), PRRecord{})
	if err != nil || draft.State != "DRAFT" || draft.Checks != "draft" || draft.Review != "none" {
		t.Fatalf("wrong draft: %+v %v", draft, err)
	}
	merged, err := statusGHDetail([]byte(`{"number":8,"state":"MERGED","mergedAt":"2026-09-25T12:00:00Z"}`), PRRecord{})
	if err != nil || merged.Merge != "MERGED" || merged.Review != "merged" || merged.MergedOn == "" {
		t.Fatalf("wrong merged facts: %+v %v", merged, err)
	}
	if _, err := statusGHDetail([]byte(`{}`), PRRecord{}); err == nil {
		t.Fatal("missing forge identity must not become a healthy PR")
	}
	bb, err := statusBBDetail([]byte(`{"id":9,"state":"OPEN","participants":[{"approved":true}]}`), PRRecord{Title: "fallback"})
	if err != nil || bb.Number != "9" || bb.Review != "approved" || bb.Merge != "UNKNOWN" || bb.Title != "fallback" {
		t.Fatalf("wrong Bitbucket facts: %+v %v", bb, err)
	}
	bbMerged, err := statusBBDetail([]byte(`{"id":9,"state":"MERGED","updated_on":"2026-09-30T12:00:00Z","merged_on":"2026-09-20T12:00:00Z"}`), PRRecord{})
	if err != nil || bbMerged.MergedOn != "2026-09-20T12:00:00Z" {
		t.Fatalf("merge time replaced by last update: %+v %v", bbMerged, err)
	}
	bbFallback, err := statusBBDetail([]byte(`{"id":9,"state":"MERGED","merge_commit":{"date":"2026-09-19T12:00:00Z"}}`), PRRecord{})
	if err != nil || bbFallback.MergedOn != "" {
		t.Fatalf("merge commit date used as merge event time: %+v %v", bbFallback, err)
	}
}

func TestStatusArchiveCountsWeekdayHours(t *testing.T) {
	friday := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	monday := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	wednesday := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if got := statusWeekdayHours(friday, monday); got != 24 {
		t.Fatalf("weekend counted: %v", got)
	}
	if statusArchived(friday.Format(time.RFC3339), monday) || !statusArchived(friday.Format(time.RFC3339), wednesday) {
		t.Fatal("archive window ignored weekday hours")
	}
	t.Setenv("WTC_PR_ARCHIVE_HOURS", "12")
	if !statusArchived(friday.Format(time.RFC3339), monday) {
		t.Fatal("configured weekday archive window ignored")
	}
}

func TestStatusEnlistedSnapshotPreservesUnknownForgeState(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	if _, err := c.EnlistPR(PRRecord{Repo: "agent-harness", Number: "12", Branch: "topic", Title: "Synthetic change"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.StatusForgePreview()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PRs) != 1 || snapshot.PRs[0].State != "UNKNOWN" || snapshot.PRs[0].Merge == nil || *snapshot.PRs[0].Merge != "UNKNOWN" || snapshot.PRs[0].Title != "Synthetic change" {
		t.Fatalf("unknown forge state was not preserved: %+v", snapshot.PRs)
	}
	for _, repo := range snapshot.Repos {
		if repo.Dir == "harness" && repo.PR != nil {
			t.Fatalf("unknown forge facts were claimed as a live PR: %+v", repo.PR)
		}
	}
}
