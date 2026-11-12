package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

func TestStatusSquashTableAndDenseNotes(t *testing.T) {
	for _, state := range []string{"landed", "follow-up", "unknown"} {
		snapshot := wtc.StatusSnapshot{Collection: "sample", Repos: []wtc.StatusRepo{{Dir: "widget", Branch: "topic", BranchDisplay: "topic [gone]", Ahead: 3, AheadRef: "origin/main", AheadBasis: "development-tip", UpstreamState: "gone", Landing: &wtc.StatusLanding{State: state}}}, PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", State: "MERGED", OnBranch: true, Landing: &wtc.StatusLanding{State: state}, DisplayTitle: state + "; synthetic change"}}}
		table := statusTable(snapshot, false, 140)
		want := map[string]string{"landed": "catch-up", "follow-up": "follow-up", "unknown": "unknown"}[state]
		if !strings.Contains(table, want) || !strings.Contains(table, "upstream gone") || !strings.Contains(table, "↑3 vs origin/main") {
			t.Fatalf("%s table:\n%s", state, table)
		}
		dense := strings.Join(statusSyncNotes(snapshot, 48), "\n")
		flat := strings.ReplaceAll(dense, "\n", " ")
		if !strings.Contains(flat, "upstream gone") || !strings.Contains(flat, "vs origin/main") || strings.Contains(flat, "fully landed residuals") != (state == "landed") {
			t.Fatalf("dense observation lost: %s", dense)
		}
		markdown := snapshot.Markdown()
		if !strings.Contains(markdown, "vs origin/main") || !strings.Contains(markdown, snapshot.PRs[0].LandingLabel()) {
			t.Fatalf("markdown: %s", markdown)
		}
	}
}

func TestStatusSquashStateCellFitsDenseLayouts(t *testing.T) {
	for _, width := range []int{32, 40, 80, 140} {
		for _, state := range []string{"follow-up", "unknown"} {
			snapshot := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", State: "MERGED", OnBranch: true, Landing: &wtc.StatusLanding{State: state}, DisplayTitle: "Synthetic change"}}}
			lines := statusTUIPRLines(snapshot, width, false, false)
			layout := statusTUIPRLayout(width)
			start := layout.number + 1 + layout.repo + 1
			cell := strings.TrimSpace(ansi.Cut(lines[3], start, start+layout.state))
			want := map[string]string{"follow-up": "⚠ extra", "unknown": "unknown"}[state]
			if cell != want {
				t.Fatalf("width=%d state=%s cell=%q want=%q", width, state, cell, want)
			}
		}
	}
}
