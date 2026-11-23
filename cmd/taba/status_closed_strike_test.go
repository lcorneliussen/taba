package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

var statusStrikeSGR = regexp.MustCompile(`\x1b\[[0-9;]*;9m`)

// statusClosedStrikeRow renders one synthetic row next to an active control and
// returns that row's raw line and its plain STATE cell.
func statusClosedStrikeRow(t *testing.T, row wtc.StatusPRRow, width int) (string, string) {
	t.Helper()
	active := wtc.StatusPRRow{Repo: "widget", Number: "9", State: "OPEN", Title: "Synthetic active", Freshness: "live",
		ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	snapshot := wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{active, row}}
	lines := statusTUIPRLines(snapshot, width, true, true)
	if len(lines) != 5 {
		t.Fatalf("width=%d: want two one-line PR rows:\n%s", width, ansi.Strip(strings.Join(lines, "\n")))
	}
	var found, other string
	for _, line := range lines[3:] {
		if ansi.StringWidth(line) > width {
			t.Fatalf("width=%d: row wider than pane: %q", width, ansi.Strip(line))
		}
		if strings.HasPrefix(ansi.Strip(line), "#"+row.Number+" ") {
			found = line
		} else {
			other = line
		}
	}
	if found == "" {
		t.Fatalf("width=%d: row #%s missing", width, row.Number)
	}
	if statusStrikeSGR.MatchString(other) {
		t.Fatalf("width=%d: active control row is struck through: %q", width, other)
	}
	l := statusTUIPRLayout(width, row, active)
	start := l.number + 1 + l.repo + 1
	return found, strings.TrimSpace(ansi.Cut(ansi.Strip(found), start, start+l.state))
}

// A confirmed unmerged closure is styled as closed at once, before archive
// eligibility; archive visibility remains a separate fact.
func TestStatusClosedStrikeBeforeArchive(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	observed := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	for _, forge := range []struct{ state, url string }{
		{"CLOSED", "https://github.com/example/widget/pull/7"},
		{"DECLINED", "https://bitbucket.org/example/widget/pull-requests/7"},
	} {
		for _, archived := range []bool{false, true} {
			for _, draft := range []bool{false, true} {
				for _, freshness := range []string{"live", "cached", "recorded"} {
					name := fmt.Sprintf("%s/archived=%v/draft=%v/%s", forge.state, archived, draft, freshness)
					t.Run(name, func(t *testing.T) {
						url := forge.url
						row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: forge.state, Title: "Synthetic closure",
							Archived: archived, Draft: draft, Freshness: freshness, ObservedAt: observed, URL: &url}
						for _, width := range []int{34, 80, 120} {
							t.Setenv("NO_COLOR", "")
							line, state := statusClosedStrikeRow(t, row, width)
							if want := strings.ToLower(forge.state); state != want {
								t.Errorf("width=%d: STATE %q, want %q (closure outranks draft)", width, state, want)
							}
							if !statusStrikeSGR.MatchString(line) {
								t.Errorf("width=%d: confirmed closure not struck through: %q", width, line)
							}
							if !strings.Contains(line, "\x1b]8;;"+url) {
								t.Errorf("width=%d: hyperlink lost", width)
							}
							t.Setenv("NO_COLOR", "1")
							plainLine, plainState := statusClosedStrikeRow(t, row, width)
							if strings.Contains(strings.ReplaceAll(plainLine, "\x1b]8;;", ""), "\x1b[") || plainState != strings.ToLower(forge.state) {
								t.Errorf("width=%d NO_COLOR: want explicit label without SGR, got %q / %q", width, plainState, plainLine)
							}
						}
						// Archive visibility keeps its own policy.
						hidden, count := statusTUIVisiblePRs(wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}, false)
						if archived != (len(hidden) == 0 && count == 1) {
							t.Errorf("archive toggle changed: hidden=%d count=%d", len(hidden), count)
						}
					})
				}
			}
		}
	}
}

// Observations that do not confirm a closure never gain closure styling.
func TestStatusClosedStrikeControls(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	observed := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	merged := "MERGED"
	for _, tc := range []struct {
		name string
		row  wtc.StatusPRRow
	}{
		{"open", wtc.StatusPRRow{State: "OPEN", Freshness: "live"}},
		{"open-draft", wtc.StatusPRRow{State: "OPEN", Draft: true, Freshness: "live"}},
		{"reopened", wtc.StatusPRRow{State: "OPEN", Freshness: "live", ObservationReason: "provider-confirmed"}},
		{"unknown", wtc.StatusPRRow{State: "UNKNOWN", Freshness: "live"}},
		{"merged", wtc.StatusPRRow{State: "MERGED", Merge: &merged, Freshness: "live"}},
		{"closed-stale", wtc.StatusPRRow{State: "CLOSED", Freshness: "stale"}},
		{"declined-deferred", wtc.StatusPRRow{State: "DECLINED", Freshness: "deferred"}},
		{"closed-unavailable", wtc.StatusPRRow{State: "CLOSED", Freshness: "unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := "https://github.com/example/widget/pull/7"
			row := tc.row
			row.Repo, row.Number, row.Title, row.ObservedAt, row.URL = "widget", "7", "Synthetic control", observed, &url
			for _, width := range []int{34, 120} {
				line, state := statusClosedStrikeRow(t, row, width)
				if statusStrikeSGR.MatchString(line) {
					t.Errorf("width=%d: unconfirmed closure struck through (STATE %q): %q", width, state, line)
				}
				if state == "closed" || state == "declined" {
					t.Errorf("width=%d: unconfirmed observation labelled %q", width, state)
				}
			}
		})
	}
}

func TestStatusClosedStrikeDumbTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	url := "https://bitbucket.org/example/widget/pull-requests/7"
	row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: "DECLINED", Title: "Synthetic declined", Freshness: "live",
		ObservedAt: time.Now().UTC().Format(time.RFC3339), URL: &url}
	for _, width := range []int{34, 120} {
		line, state := statusClosedStrikeRow(t, row, width)
		if strings.Contains(line, "\x1b") || state != "declined" {
			t.Fatalf("width=%d: dumb terminal needs the textual state without escapes: %q", width, line)
		}
	}
}
