package main

import (
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestStatus319TUIShowsDeferredObservation(t *testing.T) {
	for _, freshness := range []string{"stale", "deferred", "unavailable"} {
		snapshot := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", State: "DRAFT", Draft: true, Title: "Synthetic", Freshness: freshness, ObservedAt: "2026-11-04T10:01:00Z"}}}
		lines := strings.Join(statusTUIPRLines(snapshot, 180, false, false), "\n")
		detail := wtc.StatusPRObservationDetail(snapshot.PRs[0])
		if !strings.Contains(lines, "Synthetic") || !strings.Contains(detail, freshness) || !strings.Contains(detail, "last draft; observed 2026-11-04T10:01:00Z") {
			t.Fatal(lines)
		}
		if got := statusTUIPRSuffix(&wtc.StatusPRFacts{Number: "7", Freshness: freshness, Draft: true}, false); got != freshness {
			t.Fatalf("repository cell hid %s: %q", freshness, got)
		}
	}
}

func TestStatus319TUIHasNoInventedObservation(t *testing.T) {
	for _, state := range []string{"", "UNKNOWN"} {
		snapshot := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", State: state, Freshness: "unavailable", Title: "Synthetic"}}}
		lines := strings.Join(statusTUIPRLines(snapshot, 180, false, false), "\n")
		detail := wtc.StatusPRObservationDetail(snapshot.PRs[0])
		if !strings.Contains(lines, "never") || !strings.Contains(detail, "not yet observed") || strings.Contains(detail, "observed ;") || strings.Contains(detail, "last unknown") {
			t.Fatal(lines)
		}
	}
}
