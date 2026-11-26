package wtc

import (
	"strings"
	"testing"
	"time"
)

// #548: a view that knew more must not fall back to `never` or lose archived
// merges because a later pass could not observe them again. Within one
// verified credential context, prior facts keep their original observation
// time; recheck freshness and reasons stay truthful.

func retention548Row(number, state, freshness, observed, mergedOn string, archived bool) StatusPRRow {
	url := "https://bitbucket.org/example/widget/pull-requests/" + number
	row := StatusPRRow{Repo: "widget", Slug: "example/widget", Number: number, URL: &url, State: state, Title: "PR " + number,
		Freshness: freshness, ObservedAt: observed, Archived: archived}
	if mergedOn != "" {
		row.MergedOn = &mergedOn
	}
	if state == "MERGED" {
		merged := "MERGED"
		row.Merge = &merged
	}
	if freshness == "deferred" {
		row.ObservationReason, row.RetryAt = "shared-budget", time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	}
	if freshness == "unavailable" {
		row.ObservationReason = "refresh-failed"
	}
	return row
}

func retention548Snapshot(auth string, rows ...StatusPRRow) StatusSnapshot {
	return StatusSnapshot{Collection: "fixture", PRs: rows, authContext: auth}
}

func retention548Archived(s StatusSnapshot) int {
	n := 0
	for _, row := range s.PRs {
		if row.Archived {
			n++
		}
	}
	return n
}

func retention548Find(t *testing.T, s StatusSnapshot, number string) StatusPRRow {
	t.Helper()
	for _, row := range s.PRs {
		if row.Number == number {
			return row
		}
	}
	t.Fatalf("row %s missing", number)
	return StatusPRRow{}
}

func TestStatusRetention548PartialPassesNeverRegressKnownFacts(t *testing.T) {
	const auth = "verified-context"
	now := time.Now().UTC()
	t1 := now.Add(-3 * time.Minute).Format(time.RFC3339Nano)
	t2 := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	t3 := now.Add(-time.Minute).Format(time.RFC3339Nano)
	old := "2026-10-01T10:00:00Z"
	never := func(number string) StatusPRRow { return retention548Row(number, "", "deferred", "", "", false) }
	passes := []StatusSnapshot{
		// Startup after an upgrade: nothing observed yet.
		retention548Snapshot(auth, never("1"), never("2"), never("3"), never("4")),
		// Partial: two merges (one with its merge time) and one open PR.
		retention548Snapshot(auth,
			retention548Row("1", "MERGED", "live", t1, old, true),
			retention548Row("2", "MERGED", "live", t1, "", false),
			retention548Row("3", "OPEN", "live", t1, "", false),
			never("4")),
		// Richer: the second merge's time arrives; the last PR is read.
		retention548Snapshot(auth,
			retention548Row("1", "MERGED", "live", t2, old, true),
			retention548Row("2", "MERGED", "live", t2, old, true),
			retention548Row("3", "OPEN", "live", t2, "", false),
			retention548Row("4", "OPEN", "live", t2, "", false)),
		// Deferred, failed and late: no observation of #1, a newer merge read
		// of #2 whose merge time read was deferred, a failed read of #3 and a
		// late completion carrying an older observation of #4.
		retention548Snapshot(auth,
			never("1"),
			retention548Row("2", "MERGED", "live", t3, "", false),
			retention548Row("3", "", "unavailable", "", "", false),
			retention548Row("4", "DRAFT", "live", t1, "", false)),
	}
	var view StatusSnapshot
	richest := 0
	for i, pass := range passes {
		view = StatusPreferNewerObservations(pass, view)
		if i == 0 {
			view = pass
		}
		richest = max(richest, retention548Archived(view))
		if got := retention548Archived(view); got < richest {
			t.Fatalf("pass %d: archived count fell to %d from %d: %+v", i, got, richest, view.PRs)
		}
	}
	one := retention548Find(t, view, "1")
	if one.State != "MERGED" || !one.Archived || one.ObservedAt != t2 {
		t.Fatalf("merged #1 regressed: %+v", one)
	}
	two := retention548Find(t, view, "2")
	if two.State != "MERGED" || !two.Archived || two.MergedOn == nil || *two.MergedOn != old || two.ObservedAt != t3 {
		t.Fatalf("merged #2 lost its merge time or archive: %+v", two)
	}
	three := retention548Find(t, view, "3")
	if three.State != "OPEN" || three.ObservedAt != t2 || three.Freshness != "unavailable" || three.ObservationReason != "refresh-failed" {
		t.Fatalf("open #3 lost its last observation or recheck reason: %+v", three)
	}
	four := retention548Find(t, view, "4")
	if four.State != "OPEN" || four.ObservedAt != t2 {
		t.Fatalf("late completion replaced newer #4: %+v", four)
	}
}

// Another or an unverifiable credential context never inherits prior facts.
// The row says why the earlier observation is not shown instead of looking
// like a PR nobody has seen.
func TestStatusRetention548UnverifiedIdentityExplainsInsteadOfReusing(t *testing.T) {
	t1 := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	current := retention548Snapshot("verified-context", retention548Row("1", "MERGED", "live", t1, "2026-10-01T10:00:00Z", true))
	for _, auth := range []string{"unknown-0123", "other-context"} {
		incoming := retention548Snapshot(auth, retention548Row("1", "", "deferred", "", "", false))
		got := StatusPreferNewerObservations(incoming, current).PRs[0]
		if got.State != "" || got.ObservedAt != "" || got.Archived {
			t.Fatalf("%s inherited prior facts: %+v", auth, got)
		}
		if !got.PriorNotReused {
			t.Fatalf("%s: prior observation dropped silently: %+v", auth, got)
		}
		if detail := StatusPRObservationDetail(got); !strings.Contains(detail, "earlier observation not reused") {
			t.Fatalf("%s: detail does not explain: %s", auth, detail)
		}
	}
}

// The explanation stays while unverifiable passes keep failing to observe
// the PR, and ends once one observes it.
func TestStatusRetention548PriorNotReusedIsStickyUntilObserved(t *testing.T) {
	t1 := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	view := retention548Snapshot("verified-context", retention548Row("1", "MERGED", "live", t1, "2026-10-01T10:00:00Z", true))
	for pass := 0; pass < 3; pass++ {
		view = StatusPreferNewerObservations(retention548Snapshot("unknown-"+string(rune('a'+pass)), retention548Row("1", "", "deferred", "", "", false)), view)
		if !view.PRs[0].PriorNotReused {
			t.Fatalf("pass %d dropped the explanation", pass)
		}
	}
	view = StatusPreferNewerObservations(retention548Snapshot("unknown-z", retention548Row("1", "MERGED", "live", time.Now().UTC().Format(time.RFC3339Nano), "", false)), view)
	if view.PRs[0].PriorNotReused {
		t.Fatal("observed row still says its earlier observation was not reused")
	}
	// Another collection's view never marks this one.
	other := retention548Snapshot("unknown-q", retention548Row("1", "", "deferred", "", "", false))
	other.Collection = "elsewhere"
	if StatusPreferNewerObservations(other, view).PRs[0].PriorNotReused {
		t.Fatal("explanation crossed collections")
	}
}

// A merged PR still on its branch is never archived, even when an earlier
// observation of it was.
func TestStatusRetention548MergeTimeCarryKeepsOnBranchUnarchived(t *testing.T) {
	const auth = "verified-context"
	t1 := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	t2 := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	old := "2026-10-01T10:00:00Z"
	view := retention548Snapshot(auth, retention548Row("1", "MERGED", "live", t1, old, true))
	incoming := retention548Row("1", "MERGED", "live", t2, "", false)
	incoming.OnBranch = true
	got := StatusPreferNewerObservations(retention548Snapshot(auth, incoming), view).PRs[0]
	if got.MergedOn == nil || *got.MergedOn != old {
		t.Fatalf("merge time not carried: %+v", got)
	}
	if got.Archived {
		t.Fatalf("on-branch merge archived: %+v", got)
	}
}
