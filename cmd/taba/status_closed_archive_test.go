package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

func TestStatusClosedArchiveCollectorToNativeFrames(t *testing.T) {
	for _, tc := range []struct{ forge, state, segment string }{{"github.com", "CLOSED", "pull"}, {"bitbucket.org", "DECLINED", "pull-requests"}} {
		t.Run(tc.forge, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("WTC_CONFIG_ROOT", "")
			t.Setenv("TABA_CONFIG_ROOT", "")
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
			t.Setenv("TABA_FORGE_MIN_INTERVAL", "")
			t.Setenv("TERM", "xterm-256color")
			root := t.TempDir()
			config := filepath.Join(root, wtc.AdHocConfigDir)
			if err := os.MkdirAll(config, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(config, "repos.yml"), []byte("repos:\n  - name: widget\n    remote: https://"+tc.forge+"/example/widget.git\n    default_ref: origin/main\n"), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := wtc.OpenCollection(root)
			if err != nil {
				t.Fatal(err)
			}
			target := "https://" + tc.forge + "/example/widget/" + tc.segment + "/7"
			if err := os.WriteFile(c.PRFile(), []byte("widget 7 - "+target+" Synthetic closure\n"), 0600); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			raw := fmt.Sprintf(`{"number":7,"id":7,"state":%q,"title":"Synthetic closure","closedAt":"2020-01-01T00:00:00Z","updated_on":"2020-01-01T00:00:00Z"}`, tc.state)
			for _, name := range []string{"gh", "bb"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nif [ \"$2\" = list ]; then printf '[]'; exit 0; fi\nprintf '%s' '"+raw+"'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			snapshot, err := c.StatusForegroundSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.PRs) != 1 || !snapshot.PRs[0].Archived || snapshot.PRs[0].State != tc.state {
				t.Fatalf("collector closure lost: %+v", snapshot)
			}
			snapshot.PRs = append(snapshot.PRs, wtc.StatusPRRow{Repo: "widget", Number: "9", State: "OPEN", Title: "Active", Freshness: "live", ObservedAt: time.Now().UTC().Format(time.RFC3339)})
			for _, width := range []int{34, 80, 120} {
				for _, noColor := range []string{"", "1"} {
					t.Run(fmt.Sprintf("%d/color=%s", width, noColor), func(t *testing.T) {
						t.Setenv("NO_COLOR", noColor)
						hidden, count := statusTUIVisiblePRs(snapshot, false)
						visible, shownCount := statusTUIVisiblePRs(snapshot, true)
						if len(hidden) != 1 || count != 1 || len(visible) != 2 || shownCount != 1 || visible[0].Number != "9" || visible[1].Number != "7" {
							t.Fatal("archive count/order/toggle")
						}
						plainLines := statusTUIPRLines(snapshot, width, true, false)
						if !strings.Contains(strings.Join(plainLines, "\n"), strings.ToLower(tc.state)) {
							t.Fatal("plain terminal state lost")
						}
						lines := statusTUIPRLines(snapshot, width, true, true)
						frame := strings.Join(lines, "\n")
						plain := ansi.Strip(frame)
						if !strings.Contains(plain, strings.ToLower(tc.state)) {
							t.Fatalf("terminal meaning lost: %s", plain)
						}
						for _, line := range lines {
							if ansi.StringWidth(line) > width {
								t.Fatalf("width overflow %d %q", width, line)
							}
						}
						if !strings.Contains(frame, "\x1b]8;;"+target) {
							t.Fatal("archive hyperlink lost")
						}
						strike := regexp.MustCompile(`\x1b\[[0-9;]*9m`).MatchString(frame)
						if strike != (noColor == "") {
							t.Fatalf("strike/color contract: %q", frame)
						}
						if noColor != "" && strings.Contains(frame, "\x1b[") {
							t.Fatal("NO_COLOR retained SGR")
						}
						m := statusTUIModel{snapshot: snapshot, width: width, height: 15, interval: time.Minute, background: 5 * time.Minute, lastRefresh: time.Now(), nextRefresh: time.Now().Add(time.Minute)}
						updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
						m = updated.(statusTUIModel)
						if !m.showArchived || !strings.Contains(ansi.Strip(m.View().Content), strings.ToLower(tc.state)) {
							t.Fatal("native archive key did not reveal closure")
						}
					})
				}
			}
		})
	}
}

func TestStatusClosedArchiveDumbTerminalAndScroll(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	target := "https://bitbucket.org/example/widget/pull-requests/7"
	s := wtc.StatusSnapshot{Collection: "synthetic"}
	for i := 0; i < 30; i++ {
		s.PRs = append(s.PRs, wtc.StatusPRRow{Repo: "widget", Number: fmt.Sprint(i + 7), State: "DECLINED", Title: "Synthetic declined", Archived: true, Freshness: "live", ObservedAt: time.Now().UTC().Format(time.RFC3339), URL: &target})
	}
	for _, width := range []int{34, 120} {
		lines := statusTUIPRLines(s, width, true, true)
		frame := strings.Join(lines, "\n")
		if strings.Contains(frame, "\x1b") || !strings.Contains(frame, "declined") {
			t.Fatal("dumb terminal needs textual state without escapes")
		}
		m := statusTUIModel{snapshot: s, showArchived: true, width: width, height: 12, interval: time.Minute, background: 5 * time.Minute, nextRefresh: time.Now().Add(time.Minute)}
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		if m.effectiveScroll() == 0 {
			t.Fatal("expanded archive did not scroll")
		}
		if strings.Count(m.View().Content, "\n") > m.height {
			t.Fatal("archive broke viewport geometry")
		}
	}
}

func TestStatusClosedArchiveConfirmedLabelSurvivesPresentationExpiry(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	now := time.Now()
	observed := now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	for _, state := range []string{"CLOSED", "DECLINED"} {
		for _, freshness := range []string{"live", "cached", "recorded"} {
			for _, onBranch := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/onBranch=%v", state, freshness, onBranch), func(t *testing.T) {
					target := "https://github.com/example/widget/pull/7"
					row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: state, Title: "Synthetic closure", Freshness: freshness, ObservedAt: observed, OnBranch: onBranch, URL: &target}
					presented := statusTUIPresentationRow(row, now)
					if freshness != "recorded" && presented.Freshness != "stale" {
						t.Fatalf("expired observation must stay stale: %+v", presented)
					}
					if presented.ObservedAt != observed || row.Freshness != freshness || presented.Archived {
						t.Fatal("presentation changed evidence or archive eligibility")
					}
					for _, width := range []int{34, 120} {
						lines := statusTUIPRLines(wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}, width, false, false)
						frame := strings.Join(lines, "\n")
						if !strings.Contains(frame, strings.ToLower(state)) {
							t.Fatalf("width%d confirmed terminal label expired: %s", width, frame)
						}
						if freshness != "recorded" && !strings.Contains(frame, "s:1h") {
							t.Fatalf("width%d expiry marker lost: %s", width, frame)
						}
						for _, line := range lines {
							if ansi.StringWidth(line) > width {
								t.Fatal("expiry broke width")
							}
						}
					}
				})
			}
		}
	}
}

func TestStatusClosedArchiveExpiryDoesNotConfirmUnknownOrReopenedRows(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	for _, state := range []string{"CLOSED", "DECLINED", "OPEN", "UNKNOWN"} {
		for _, freshness := range []string{"stale", "deferred", "unavailable", ""} {
			row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: state, Title: "Synthetic fact", Freshness: freshness, ObservedAt: old}
			// A provider-observed closure stays the last known lifecycle through
			// a stale, deferred or failed recheck; anything else never gains one.
			retained := (state == "CLOSED" || state == "DECLINED") && freshness != ""
			for _, width := range []int{34, 120} {
				frame := strings.Join(statusTUIPRLines(wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}, width, false, false), "\n")
				closed := strings.Contains(frame, "closed") || strings.Contains(frame, "declined")
				if closed != retained || retained && !strings.Contains(frame, strings.ToLower(state)) {
					t.Fatalf("closure presentation (want retained=%v): %+v\n%s", retained, row, frame)
				}
				if mark := wtc.StatusPRFreshnessMark(freshness, old, time.Now()); retained && !strings.Contains(frame, mark+" ") {
					t.Fatalf("retained closure hides freshness %q:\n%s", mark, frame)
				}
			}
		}
	}
	for _, observed := range []string{"", "malformed", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)} {
		row := wtc.StatusPRRow{Repo: "widget", Number: "7", State: "CLOSED", Title: "Synthetic fact", Freshness: "live", ObservedAt: observed}
		frame := strings.Join(statusTUIPRLines(wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}, 120, false, false), "\n")
		if strings.Contains(frame, "closed") || !strings.Contains(frame, wtc.StatusPRFreshnessMark("unavailable", observed, time.Now())) {
			t.Fatalf("invalid observation became authoritative: %s", frame)
		}
	}
}
