package main

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"taba.sh/taba/internal/wtc"
	"testing"
	"time"
)

func TestStatus426TitleAndFreshnessStayOnOneRow(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, freshness := range []string{"stale", "deferred", "unavailable", "refreshing"} {
		for _, width := range []int{34, 40, 52, 80, 100} {
			for _, styled := range []bool{false, true} {
				row := wtc.StatusPRRow{Repo: "widget", Number: "7", Title: "Synthetic title remains visible", State: "OPEN", Freshness: freshness, ObservedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)}
				lines := statusTUIPRLines(wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{row}}, width, false, styled)
				if len(lines) != 4 || !strings.Contains(ansi.Strip(lines[3]), "Synt") || !strings.Contains(ansi.Strip(lines[3]), ":1h") {
					t.Fatalf("width%d: %q", width, lines)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatalf("overflow%d: %q", width, line)
					}
				}
			}
		}
	}
}

func TestStatus424ManualRefreshCoalescesBehindCollectorAndIgnoresBackoff(t *testing.T) {
	m := statusTUIModel{context: &wtc.Context{}, refreshing: true, focused: false, nextRefresh: time.Now().Add(time.Hour)}
	for i := 0; i < 100; i++ {
		next, cmd := m.Update(tea.KeyPressMsg{Code: 'r'})
		m = next.(statusTUIModel)
		if cmd != nil {
			t.Fatal("parallel refresh launched")
		}
	}
	if !m.pendingForeground {
		t.Fatal("explicit refresh lost")
	}
	next, cmd := m.Update(statusLoadedMsg{snapshot: wtc.StatusSnapshot{}, at: time.Now()})
	m = next.(statusTUIModel)
	if cmd == nil || !m.foreground || !m.refreshing || m.pendingForeground || m.refreshCancel == nil {
		t.Fatal("queued foreground did not start immediately")
	}
	m.refreshCancel()
	for i := 0; i < 10; i++ {
		next, cmd = m.Update(tea.KeyPressMsg{Code: 'r'})
		m = next.(statusTUIModel)
		if cmd != nil || m.pendingForeground {
			t.Fatal("foreground key burst enqueued duplicate")
		}
	}
}

func TestStatus424QuitCancelsForegroundAndStaleCompletionCannotWin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := statusTUIModel{refreshID: 2, foreground: true, refreshing: true, refreshCancel: cancel}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'q'})
	_ = next
	if ctx.Err() == nil || cmd == nil {
		t.Fatal("quit did not cancel foreground")
	}
	next, _ = m.Update(statusLoadedMsg{requestID: 1, snapshot: wtc.StatusSnapshot{Collection: "old"}, at: time.Now()})
	if next.(statusTUIModel).snapshot.Collection == "old" {
		t.Fatal("stale foreground result won")
	}
}

func TestStatus424ForegroundDeferredFeedbackIsNotSuccess(t *testing.T) {
	m := statusTUIModel{width: 100, height: 25}
	m.startRefresh()
	next, _ := m.Update(statusLoadedMsg{intent: wtc.StatusRefreshForeground, snapshot: wtc.StatusSnapshot{PRs: []wtc.StatusPRRow{{Repo: "widget", Number: "7", Freshness: "deferred", ObservationReason: "provider-cooldown", RetryAt: "2026-11-16T10:00:00Z"}}}, at: time.Now()})
	log := strings.Join(next.(statusTUIModel).progressLog, "\n")
	if !strings.Contains(log, "1 deferred") || !strings.Contains(log, "provider-cooldown") || !strings.Contains(log, "retry after") || strings.Contains(log, "Refresh finished") {
		t.Fatal(log)
	}
}
