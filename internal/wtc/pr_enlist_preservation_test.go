package wtc

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEnlistPreservesExistingMetadata(t *testing.T) {
	for _, key := range []string{"widget", "ext.widget"} {
		t.Run(key, func(t *testing.T) {
			c, _ := enlistedIdentityFixture(t, true)
			merge, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
			markers := "# merged-pr " + key + " 7 2026-11-01T12:00:00Z SUCCESS\n" +
				"# merged-commit " + key + " 7 " + merge + " main\n" +
				"# merged-head " + key + " 7 " + merge + " " + head + "\n"
			other := "other 9 other/topic https://github.com/example/other/pull/9 Other title\n"
			url := "https://github.com/example/widget/pull/7/files"
			fixtureFile(t, c.PRFile(), key+" 7 published/topic "+url+" Existing title\n"+markers+other, 0644)
			got, err := c.EnlistPR(PRRecord{Repo: "widget", Number: "7"})
			if err != nil {
				t.Fatal(err)
			}
			want := PRRecord{Repo: key, Number: "7", Branch: "topic", URL: url, Title: "Existing title",
				MergedOn: "2026-11-01T12:00:00Z", FinalChecks: "SUCCESS", MergeCommit: merge, MergeBase: "main", MergeHead: head}
			if got != want {
				t.Fatalf("re-enlist = %+v, want %+v", got, want)
			}
			records, err := c.ListPRs()
			if err != nil || len(records) != 2 || records[1] != got {
				t.Fatalf("returned and persisted records disagree: %+v, %v", records, err)
			}
			data, err := os.ReadFile(c.PRFile())
			if err != nil || !strings.Contains(string(data), markers) || !strings.Contains(string(data), other) {
				t.Fatalf("merge evidence or unrelated row changed: %s, %v", data, err)
			}
		})
	}
}

func TestEnlistPreservesURLOnlyReference(t *testing.T) {
	c := fixture(t)
	want := PRRecord{Repo: "reference", Number: "7", URL: "https://github.com/example/reference/pull/7", Title: "Reference title"}
	if _, err := c.EnlistPR(want); err != nil {
		t.Fatal(err)
	}
	got, err := c.EnlistPR(PRRecord{Repo: "reference", Number: "7"})
	if err != nil || got != want {
		t.Fatalf("reference identity lost: %+v, %v", got, err)
	}
}

func TestEnlistDefaultsAndExplicitReplacements(t *testing.T) {
	c, checkout := enlistedIdentityFixture(t, true)
	got, err := c.EnlistPR(PRRecord{Repo: "widget", Number: "7"})
	if err != nil || got.URL != "https://github.com/example/widget/pull/7" || got.Title != "" || got.Branch != "topic" {
		t.Fatalf("new enlistment defaults changed: %+v, %v", got, err)
	}
	want := PRRecord{Repo: "widget", Number: "7", Branch: "replacement/topic", URL: "https://github.com/example/widget/pull/7/files", Title: "Replacement title"}
	got, err = c.EnlistPR(want)
	if err != nil || got != want {
		t.Fatalf("explicit replacement ignored: %+v, %v", got, err)
	}
	fixtureGit(t, "-C", checkout, "checkout", "--detach", "-q")
	want.Branch = ""
	got, err = c.EnlistPR(PRRecord{Repo: "widget", Number: "7"})
	if err != nil || got != want {
		t.Fatalf("detached branch default changed or metadata lost: %+v, %v", got, err)
	}
	records, err := c.ListPRs()
	if err != nil || !reflect.DeepEqual(records, []PRRecord{got}) {
		t.Fatalf("returned and persisted records disagree: %+v, %v", records, err)
	}
}

func TestEnlistNativeRetryPreservesTitle(t *testing.T) {
	c, checkout := enlistedIdentityFixture(t, true)
	receipt := PRRecord{URL: "https://github.com/example/widget/pull/7", Branch: "published/topic"}
	if _, err := c.EnlistGatewayPublication(checkout, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnlistPR(PRRecord{Repo: "widget", Number: "7", Branch: "published/topic", Title: "Title added before retry"}); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, "-C", checkout, "checkout", "-qb", "later/topic")
	got, err := c.EnlistGatewayPublication(checkout, receipt)
	want := PRRecord{Repo: "widget", Number: "7", URL: receipt.URL, Branch: receipt.Branch, Title: "Title added before retry"}
	if err != nil || got != want {
		t.Fatalf("native retry lost metadata: %+v, %v", got, err)
	}
	records, err := c.ListPRs()
	if err != nil || !reflect.DeepEqual(records, []PRRecord{got}) {
		t.Fatalf("native return and registry disagree: %+v, %v", records, err)
	}
}

func TestEnlistPreservesMetadataWrittenWhileWaitingForLock(t *testing.T) {
	c, _ := enlistedIdentityFixture(t, true)
	fixtureFile(t, c.PRFile(), "widget 7 old/topic https://github.com/example/widget/pull/7 Old title\n", 0644)
	lock, err := os.OpenFile(filepath.Join(c.Collection, ".wtc-prs.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	type result struct {
		record PRRecord
		err    error
	}
	started, done := make(chan struct{}), make(chan result, 1)
	go func() {
		close(started)
		r, err := c.EnlistPR(PRRecord{Repo: "widget", Number: "7", Branch: "published/topic"})
		done <- result{r, err}
	}()
	<-started
	select {
	case r := <-done:
		t.Fatalf("enlistment bypassed registry lock: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	// An older client can update the row while holding the same registry lock.
	url := "https://github.com/example/widget/pull/7/files"
	fixtureFile(t, c.PRFile(), "widget 7 old/topic "+url+" Latest title\n", 0644)
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		want := PRRecord{Repo: "widget", Number: "7", Branch: "published/topic", URL: url, Title: "Latest title"}
		if r.err != nil || r.record != want {
			t.Fatalf("enlistment used stale metadata: %+v, %v", r.record, r.err)
		}
		records, err := c.ListPRs()
		if err != nil || !reflect.DeepEqual(records, []PRRecord{want}) {
			t.Fatalf("latest metadata not persisted: %+v, %v", records, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enlistment did not finish after unlock")
	}
}
