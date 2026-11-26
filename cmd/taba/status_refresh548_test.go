package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

// #548 through the production delivery path: periodic and explicit results
// arrive as statusLoadedMsg, in any order, and the painted rows must not
// regress. Rows nobody observed yet say they wait, and the view schedules its
// own next pass for them instead of waiting for a keypress.

func refresh548Row(number, state, freshness, observed, mergedOn string, archived bool) wtc.StatusPRRow {
	url := "https://bitbucket.org/example/widget/pull-requests/" + number
	row := wtc.StatusPRRow{Repo: "widget", Slug: "example/widget", Number: number, URL: &url, State: state, Title: "Synthetic " + number,
		DisplayTitle: "Synthetic " + number, Freshness: freshness, ObservedAt: observed, Archived: archived}
	if mergedOn != "" {
		row.MergedOn = &mergedOn
	}
	if state == "MERGED" {
		merged := "MERGED"
		row.Merge = &merged
	}
	if freshness == "deferred" {
		row.ObservationReason = "shared-budget"
	}
	return row
}

var refresh548ArchivedHint = regexp.MustCompile(`archived \((\d+)\)|(\d+) archived PR`)

func refresh548Paint(t *testing.T, m statusTUIModel) (string, int) {
	t.Helper()
	plain := ansi.Strip(strings.Join(statusTUIPRLines(m.snapshot, 120, false, false), "\n"))
	archived := 0
	if match := refresh548ArchivedHint.FindStringSubmatch(plain); match != nil {
		value := match[1]
		if value == "" {
			value = match[2]
		}
		archived, _ = strconv.Atoi(value)
	}
	return plain, archived
}

func TestStatus548DeliveredPassesNeverRegressAndWaitingRowsRetry(t *testing.T) {
	now := time.Now().UTC()
	t1 := now.Add(-3 * time.Minute).Format(time.RFC3339Nano)
	t2 := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	old := "2026-10-01T10:00:00Z"
	waiting := func(number string) wtc.StatusPRRow { return refresh548Row(number, "", "deferred", "", "", false) }
	retry := now.Add(45 * time.Second)
	waitingWithRetry := waiting("4")
	waitingWithRetry.RetryAt = retry.Format(time.RFC3339Nano)
	passes := []statusLoadedMsg{
		{snapshot: wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{waiting("1"), waiting("2"), waiting("3"), waitingWithRetry}}},
		{intent: wtc.StatusRefreshForeground, snapshot: wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{
			refresh548Row("1", "MERGED", "live", t1, old, true), refresh548Row("2", "MERGED", "live", t1, old, true),
			refresh548Row("3", "OPEN", "live", t1, "", false), waitingWithRetry}}},
		{snapshot: wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{
			refresh548Row("1", "MERGED", "live", t2, old, true), refresh548Row("2", "MERGED", "live", t2, "", false),
			refresh548Row("3", "OPEN", "live", t2, "", false), refresh548Row("4", "OPEN", "live", t2, "", false)}}},
		// A deferred automatic pass and a late explicit completion.
		{snapshot: wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{waiting("1"), waiting("2"), waiting("3"), waiting("4")}}},
		{intent: wtc.StatusRefreshForeground, snapshot: wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{
			waiting("1"), refresh548Row("2", "MERGED", "live", t1, "", false), waiting("3"), refresh548Row("4", "DRAFT", "live", t1, "", false)}}},
	}
	m := statusTUIModel{width: 120, height: 40, interval: 30 * time.Second, background: 5 * time.Minute}
	richest := 0
	for i, pass := range passes {
		pass.at = now.Add(time.Duration(i) * time.Second)
		next, _ := m.Update(pass)
		m = next.(statusTUIModel)
		plain, archived := refresh548Paint(t, m)
		richest = max(richest, archived)
		if archived < richest {
			t.Fatalf("pass %d: archived hint fell to %d from %d:\n%s", i, archived, richest, plain)
		}
		waits := false
		for _, row := range m.snapshot.PRs {
			if row.ObservedAt == "" && row.Freshness == "deferred" {
				waits = true
			}
		}
		// An unfocused view refreshes every five minutes, but not while a
		// row waits for its first observation.
		switch {
		case i == 0 && !m.nextRefresh.Equal(pass.at.Add(30*time.Second)):
			t.Fatalf("pass 0: next refresh %s, want the 30 s floor", m.nextRefresh.Sub(pass.at))
		case i == 1 && !m.nextRefresh.Equal(retry):
			t.Fatalf("pass 1: next refresh %s, want the waiting row's retry %s", m.nextRefresh.Sub(pass.at), retry.Sub(pass.at))
		case waits && i != 0 && m.nextRefresh.After(pass.at.Add(time.Minute)):
			t.Fatalf("pass %d: waiting rows left for %s", i, m.nextRefresh.Sub(pass.at))
		case !waits && !m.nextRefresh.Equal(pass.at.Add(5*time.Minute)):
			t.Fatalf("pass %d: nothing waits, next refresh %s", i, m.nextRefresh.Sub(pass.at))
		}
		if i == 0 && (!strings.Contains(plain, "wait") || strings.Contains(plain, "never")) {
			t.Fatalf("waiting rows not shown as waiting:\n%s", plain)
		}
	}
	plain, archived := refresh548Paint(t, m)
	if archived != 2 || strings.Contains(plain, "never") || strings.Contains(plain, "wait") {
		t.Fatalf("final view regressed (archived %d):\n%s", archived, plain)
	}
	for _, row := range m.snapshot.PRs {
		if row.State == "" || row.ObservedAt == "" {
			t.Fatalf("row %s lost its observation: %+v", row.Number, row)
		}
	}
}
