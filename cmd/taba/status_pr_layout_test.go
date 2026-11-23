package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

// statusPRLayoutWidths spans the narrow (no signals, 7-wide STATE), medium and
// wide branches of statusTUIPRLayout.
var statusPRLayoutWidths = []int{24, 32, 34, 40, 52, 80, 100, 140, 220}

// statusPRLayoutCell returns the plain STATE and TITLE cells of row's line.
func statusPRLayoutCell(t *testing.T, lines []string, width int, row wtc.StatusPRRow) (state, title string) {
	t.Helper()
	l := statusTUIPRLayout(width, row)
	plain := ""
	for _, line := range lines[3:] {
		if strings.HasPrefix(ansi.Strip(line), "#"+row.Number+" ") {
			plain = ansi.Strip(line)
		}
	}
	if plain == "" {
		t.Fatalf("width=%d: no line for #%s", width, row.Number)
	}
	start := l.number + 1 + l.repo + 1
	state = strings.TrimSpace(ansi.Cut(plain, start, start+l.state))
	return state, strings.TrimSpace(ansi.Cut(plain, ansi.StringWidth(plain)-l.title, ansi.StringWidth(plain)))
}

// assertOnePRLinePerRow checks the one-line-per-PR invariant: the PR table is
// heading, header and exactly one line per visible PR, none wider than the pane.
func assertOnePRLinePerRow(t *testing.T, snapshot wtc.StatusSnapshot, width int, styled bool) []string {
	t.Helper()
	var positions []int
	lines := statusTUIPRLines(snapshot, width, false, styled, func(line int, _ wtc.StatusPRRow) {
		positions = append(positions, line)
	})
	if len(lines) != 3+len(snapshot.PRs) || len(positions) != len(snapshot.PRs) {
		t.Fatalf("width=%d styled=%v: %d PRs rendered as %d lines:\n%s", width, styled, len(snapshot.PRs), len(lines), ansi.Strip(strings.Join(lines, "\n")))
	}
	for i, at := range positions {
		if at != 3+i {
			t.Fatalf("width=%d styled=%v: PR %d at line %d, want %d", width, styled, i, at, 3+i)
		}
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > width {
			t.Fatalf("width=%d styled=%v: line wider than pane: %q", width, styled, ansi.Strip(line))
		}
	}
	return lines
}

func TestStatusPRLayoutMergedLandingOneLine(t *testing.T) {
	merged := "MERGED"
	for _, tc := range []struct {
		name    string
		landing *wtc.StatusLanding
		state   string
	}{
		{"no-landing-record", nil, "⚠ catch-up"},
		{"fully-landed", &wtc.StatusLanding{State: "landed"}, "⚠ catch-up"},
		{"follow-up", &wtc.StatusLanding{State: "follow-up"}, "⚠ extra"},
		{"unknown", &wtc.StatusLanding{State: "unknown", Reason: "full landing not established"}, "merged?"},
	} {
		for _, mergeKnown := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/merge-signal=%v", tc.name, mergeKnown), func(t *testing.T) {
				url := "https://example.test/widget/pull/7"
				row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: "MERGED", OnBranch: true, URL: &url,
					Landing: tc.landing, Title: "Synthetic readable title"}
				if mergeKnown {
					row.Merge = &merged
				}
				row.DisplayTitle = "MERGED — " + row.LandingLabel() + "; still on topic  " + row.Title
				next := wtc.StatusPRRow{Repo: "widget", Number: "8", State: "OPEN", Title: "Synthetic next PR"}
				snapshot := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row, next}}
				for _, width := range statusPRLayoutWidths {
					for _, styled := range []bool{false, true} {
						lines := assertOnePRLinePerRow(t, snapshot, width, styled)
						state, title := statusPRLayoutCell(t, lines, width, row)
						l := statusTUIPRLayout(width, row)
						if want := strings.TrimSpace(statusTUIFit(tc.state, l.state)); state != want {
							t.Errorf("width=%d styled=%v: STATE %q, want %q", width, styled, state, tc.state)
						}
						// The PR title leads the title cell (at least a 5-cell prefix
						// where there is room); landing metadata follows only if it fits.
						if l.title >= 6 && !strings.HasPrefix(title, row.Title[:5]) {
							t.Errorf("width=%d styled=%v: title displaced: %q", width, styled, title)
						}
						if strings.Contains(title, "MERGED —") {
							t.Errorf("width=%d styled=%v: metadata precedes title: %q", width, styled, title)
						}
						if width >= 140 && !strings.Contains(title, row.LandingLabel()) {
							t.Errorf("width=%d styled=%v: wide row lost landing label: %q", width, styled, title)
						}
					}
				}
			})
		}
	}
}

func TestStatusPRLayoutLandingStatesStayDistinct(t *testing.T) {
	merged := "MERGED"
	states := map[string]string{}
	for _, landing := range []string{"landed", "follow-up", "unknown"} {
		row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: "MERGED", Merge: &merged, OnBranch: true,
			Landing: &wtc.StatusLanding{State: landing}, Title: "Synthetic"}
		lines := statusTUIPRLines(wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}, 32, false, false)
		state, _ := statusPRLayoutCell(t, lines, 32, row)
		states[landing] = state
	}
	off := wtc.StatusPRRow{Repo: "widget", Number: "7", State: "MERGED", Merge: &merged, Title: "Synthetic"}
	lines := statusTUIPRLines(wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{off}}, 32, false, false)
	offState, _ := statusPRLayoutCell(t, lines, 32, off)
	if states["unknown"] == states["landed"] || states["unknown"] == states["follow-up"] || states["unknown"] == offState ||
		states["follow-up"] == states["landed"] || states["follow-up"] == offState || states["landed"] == offState {
		t.Fatalf("landing states not distinct: %v off-branch=%q", states, offState)
	}
}

func TestStatusPRLayoutHelpKeepsLandingExplanation(t *testing.T) {
	merged := "MERGED"
	for _, landing := range []string{"", "landed", "follow-up", "unknown"} {
		row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: "MERGED", Merge: &merged, OnBranch: true, Title: "Synthetic"}
		if landing != "" {
			row.Landing = &wtc.StatusLanding{State: landing}
		}
		for _, width := range []int{32, 100} {
			model := statusTUIModel{snapshot: wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}, width: width, height: 80, showHelp: true}
			plain := ansi.Strip(strings.Join(model.contentLayout().lines, " "))
			flat := strings.Join(strings.Fields(plain), " ")
			if !strings.Contains(flat, row.LandingLabel()) {
				t.Errorf("width=%d %s: help lost %q", width, landing, row.LandingLabel())
			}
			if landing == "unknown" && !strings.Contains(flat, "merged?") {
				t.Errorf("width=%d: help legend does not explain merged?", width)
			}
		}
	}
}

func TestStatusPRLayoutFreshnessOneLine(t *testing.T) {
	now := time.Now().UTC()
	twoHours := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	for _, tc := range []struct {
		name string
		row  wtc.StatusPRRow
		mark string
	}{
		{"stale", wtc.StatusPRRow{State: "OPEN", Freshness: "stale", ObservedAt: twoHours, ObservationReason: "observation-expired"}, "s:2h"},
		{"deferred", wtc.StatusPRRow{State: "OPEN", Freshness: "deferred", ObservedAt: twoHours, ObservationReason: "shared-budget"}, "d:2h"},
		{"unavailable-failed-retry", wtc.StatusPRRow{State: "OPEN", Freshness: "unavailable", ObservedAt: twoHours, ObservationReason: "rate-limit", RetryAt: "2026-11-16T11:00:00Z"}, "u:2h"},
		{"never", wtc.StatusPRRow{Freshness: "unavailable"}, "never"},
		{"refreshing", wtc.StatusPRRow{State: "OPEN", Freshness: "refreshing", ObservedAt: twoHours}, "r:2h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.row
			row.Repo, row.Number, row.Title = "widget", "7", "Synthetic readable title"
			snapshot := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}
			for _, width := range statusPRLayoutWidths {
				for _, styled := range []bool{false, true} {
					lines := assertOnePRLinePerRow(t, snapshot, width, styled)
					state, title := statusPRLayoutCell(t, lines, width, row)
					if state != tc.mark {
						t.Errorf("width=%d styled=%v: STATE %q want %q", width, styled, state, tc.mark)
					}
					// Below 32 columns the fixed REPO/STATE columns leave the title
					// about one cell; freshness lives in STATE either way.
					if (width >= 32 && !strings.HasPrefix(title, "Synth")) || strings.Contains(title, "observed") || strings.Contains(title, "last open") {
						t.Errorf("width=%d styled=%v: freshness metadata displaced title: %q", width, styled, title)
					}
				}
			}
			detail := wtc.StatusPRObservationDetail(statusTUIPresentationRow(row, now))
			if row.ObservationReason != "" && !strings.Contains(detail, row.ObservationReason) {
				t.Errorf("detail lost reason: %q", detail)
			}
			if row.RetryAt != "" && !strings.Contains(detail, "retry after "+row.RetryAt) {
				t.Errorf("detail lost retry: %q", detail)
			}
			if row.ObservationReason == "" && !strings.Contains(detail, "reason ") {
				t.Errorf("unknown reason not explicit: %q", detail)
			}
		})
	}
}

func TestStatusPRLayoutWideGlyphAndControlTitles(t *testing.T) {
	merged := "MERGED"
	for _, title := range []string{"同步状态表格的宽字符标题测试用例", "🚀🚀🚀 emoji title 🚀🚀🚀", "inject \x1b[31mred\x1b[0m\nsecond line\rcr"} {
		row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: "MERGED", Merge: &merged, OnBranch: true,
			Landing: &wtc.StatusLanding{State: "unknown"}, Title: title}
		row.DisplayTitle = "MERGED — " + row.LandingLabel() + "; still on topic  " + title
		for _, width := range statusPRLayoutWidths {
			for _, styled := range []bool{false, true} {
				lines := assertOnePRLinePerRow(t, wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}, width, styled)
				if plain := ansi.Strip(lines[3]); strings.ContainsAny(plain, "\n\r\x1b") {
					t.Fatalf("width=%d: control characters reached the row: %q", width, plain)
				}
			}
		}
	}
}

func TestStatusPRLayoutScrollSelectionGeometry(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	merged := "MERGED"
	var prs []wtc.StatusPRRow
	for i, landing := range []string{"unknown", "landed", "follow-up", "unknown"} {
		url := fmt.Sprintf("https://example.test/widget/pull/%d", 10+i)
		prs = append(prs, wtc.StatusPRRow{Repo: "widget", Number: fmt.Sprint(10 + i), State: "MERGED", Merge: &merged, OnBranch: true,
			URL: &url, Landing: &wtc.StatusLanding{State: landing}, Title: "Synthetic title with enough words to truncate"})
	}
	for _, width := range []int{32, 40, 80, 140} {
		model := statusTUIModel{snapshot: wtc.StatusSnapshot{PRs: prs}, width: width, height: 40}
		content := model.contentLayout()
		if len(content.rows) != len(prs) {
			t.Fatalf("width=%d: rows=%+v", width, content.rows)
		}
		for i, row := range content.rows {
			if row.end != row.start+1 || (i > 0 && row.start != content.rows[i-1].end) {
				t.Fatalf("width=%d: PR %d not one contiguous line: %+v", width, i, content.rows)
			}
			if got := model.buildClickTarget(1, row.start); got != *prs[i].URL {
				t.Fatalf("width=%d: hit %q want %q", width, got, *prs[i].URL)
			}
		}
		model.height = 6
		model.scroll = content.rows[3].start - model.viewport(content).header
		y := content.rows[3].start - model.viewport(content).scroll
		if got := model.buildClickTarget(1, y); got != *prs[3].URL {
			t.Fatalf("width=%d: scrolled hit %q want %q", width, got, *prs[3].URL)
		}
	}
}
