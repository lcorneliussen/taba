package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/wtc"
)

func status473Run(t *testing.T, view *statusSelectionView, input string) string {
	t.Helper()
	var out bytes.Buffer
	view.SetStdin(strings.NewReader(input))
	view.SetStdout(&out)
	if err := view.Run(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestStatus473SelectionViewWritesFullOutputWithoutCapture(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	m := statusOverflowModel()
	visible := ansi.Strip(m.View().Content)
	if strings.Contains(visible, "repo-35") || strings.Contains(visible, "Synthetic change 19") {
		t.Fatal("fixture no longer overflows the viewport")
	}
	captured := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	out := status473Run(t, m.selectionView(captured), "\r")
	plain := ansi.Strip(out)
	for _, want := range []string{"repo-00", "repo-35", "Synthetic change 0", "Synthetic change 19", "captured 03:04:05", "Enter, q or v returns"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("selection view is missing %q:\n%s", want, plain)
		}
	}
	for _, control := range []string{"\x1b[?1000", "\x1b[?1002", "\x1b[?1003", "\x1b[?1006", "\x1b[?1049", "\x1b[2J", "\x1b[H"} {
		if strings.Contains(out, control) {
			t.Fatalf("selection view emits screen or mouse control %q", control)
		}
	}
	if !strings.Contains(out, "\x1b]8;;https://example.test/pull/119") {
		t.Fatal("selection view dropped native hyperlinks for offscreen rows")
	}
	for _, line := range strings.Split(out, "\n") {
		if ansi.StringWidth(line) > m.width {
			t.Fatalf("selection line exceeds %d columns: %q", m.width, line)
		}
	}
}

func TestStatus473SelectionViewNoColorDumbAndWideText(t *testing.T) {
	names := []string{"仕様-レビュー", "café-e\u0301", "👩‍💻-team", "🇳🇴-flag"}
	m := statusTUIModel{width: 24, height: 6, snapshot: wtc.StatusSnapshot{Collection: "wide"}}
	for _, name := range names {
		m.snapshot.Repos = append(m.snapshot.Repos, wtc.StatusRepo{Collection: "wide", Repo: name, Dir: name, BranchDisplay: "main"})
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	out := status473Run(t, m.selectionView(time.Now()), "q")
	if strings.Contains(out, "\x1b[3") || strings.Contains(out, "\x1b[9") || strings.Contains(out, "\x1b[2m") {
		t.Fatal("NO_COLOR selection view contains color")
	}
	for _, line := range strings.Split(out, "\n") {
		if ansi.StringWidth(line) > m.width {
			t.Fatalf("wide text overflows %d columns: %q", m.width, line)
		}
	}
	for _, name := range names[:2] {
		if !strings.Contains(out, name[:3]) {
			t.Fatalf("wide name %q missing from selection text", name)
		}
	}
	t.Setenv("TERM", "dumb")
	if out = status473Run(t, m.selectionView(time.Now()), "q"); strings.Contains(out, "\x1b") {
		t.Fatalf("dumb terminal selection view contains controls: %q", out)
	}
}

func TestStatus473SelectionViewReturnKeys(t *testing.T) {
	m := statusTUIModel{width: 60, height: 10}
	for _, input := range []string{"\r", "\n", "q", "v", "x\r", "\x1b[A\x1b[6~", ""} {
		view := m.selectionView(time.Now())
		status473Run(t, view, input)
		if view.quit {
			t.Fatalf("%q requested quit", input)
		}
	}
	for _, input := range []string{"\x03", "\x1b[5~", "\x1b"} {
		view := m.selectionView(time.Now())
		var out bytes.Buffer
		reader := &status473Chunks{chunks: []string{input, "\x03"}}
		view.SetStdin(reader)
		view.SetStdout(&out)
		if err := view.Run(); err != nil || !view.quit || reader.read != 1+btoi(input != "\x03") {
			t.Fatalf("%q: escape sequences must not return, Ctrl+C must quit (quit=%v reads=%d)", input, view.quit, reader.read)
		}
	}
	quitting, command := m.Update(statusSelectionDoneMsg{quit: true})
	if command == nil || quitting == nil {
		t.Fatal("Ctrl+C in the selection view did not quit status")
	}
	failed, _ := m.Update(statusSelectionDoneMsg{err: fmt.Errorf("synthetic terminal error")})
	if !strings.Contains(failed.(statusTUIModel).errorText, "synthetic terminal error") {
		t.Fatal("selection view failure was hidden")
	}
}

type status473Chunks struct {
	chunks []string
	read   int
}

func (r *status473Chunks) Read(p []byte) (int, error) {
	if r.read >= len(r.chunks) {
		return 0, fmt.Errorf("read past synthetic input")
	}
	r.read++
	return copy(p, r.chunks[r.read-1]), nil
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestStatus473RefreshDuringSelectionKeepsCaptureAndObservations(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusTUIModel{width: 80, height: 20, snapshot: wtc.StatusSnapshot{Collection: "fixture",
		Repos: []wtc.StatusRepo{{Collection: "fixture", Repo: "before", Dir: "before", BranchDisplay: "main"}}}}
	m.startRefresh()
	view := m.selectionView(time.Now())
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(statusTUIModel)
	observed := time.Now().Add(-90 * time.Second)
	updated, _ = m.Update(statusLoadedMsg{snapshot: wtc.StatusSnapshot{Collection: "fixture", GeneratedAt: observed.Format(time.RFC3339Nano),
		Repos: []wtc.StatusRepo{{Collection: "fixture", Repo: "after", Dir: "after", BranchDisplay: "main"}}}, at: time.Now()})
	m = updated.(statusTUIModel)
	out := ansi.Strip(status473Run(t, view, "\r"))
	if !strings.Contains(out, "before") || strings.Contains(out, "after") {
		t.Fatalf("refresh rewrote captured selection text:\n%s", out)
	}
	updated, _ = m.Update(statusSelectionDoneMsg{})
	m = updated.(statusTUIModel)
	if !strings.Contains(ansi.Strip(m.View().Content), "after") || !m.lastRefresh.Equal(observed) {
		t.Fatalf("refresh result or its observation time was not kept across the selection view (last=%v)", m.lastRefresh)
	}
}

func TestStatus473MouseCaptureFlagsStayBounded(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	short := statusTUIModel{width: 100, height: 40, snapshot: wtc.StatusSnapshot{Collection: "plain"}}
	short.mouseCapture = true
	if mode := short.View().MouseMode; mode != tea.MouseModeNone {
		t.Fatalf("--mouse view without links, overflow or hover captures the mouse: %v", mode)
	}
	overflow := statusOverflowModel()
	overflow.noClick, overflow.mouseCapture = true, true
	if mode := overflow.View().MouseMode; mode != tea.MouseModeCellMotion {
		t.Fatalf("overflow view must keep wheel input without hover motion: %v", mode)
	}
}

func TestStatus473RedrawUnderHeldPressNeverOpensAnotherRow(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	shared := "https://example.test/pull/shared"
	rows := func(first, second string) wtc.StatusSnapshot {
		return wtc.StatusSnapshot{Collection: "fixture", PRs: []wtc.StatusPRRow{
			{Repo: "widget", Number: first, Title: "Synthetic " + first, URL: &shared},
			{Repo: "widget", Number: second, Title: "Synthetic " + second, URL: &shared}}}
	}
	m := statusTUIModel{width: 100, height: 20, snapshot: rows("7", "8")}
	x, y, _ := status473LinkCell(t, m)
	pressed, _ := status473Mouse(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	redrawn, _ := status473Mouse(pressed, statusLoadedMsg{snapshot: rows("8", "7"), at: time.Now()})
	if row, _ := redrawn.visibleRow(x, y); row.key == "" || row.key == mustRowKey(t, pressed, x, y) {
		t.Fatal("fixture redraw did not move another row under the pointer")
	}
	if _, command := status473Mouse(redrawn, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}); command != nil {
		t.Fatal("release after a redraw opened a different logical row with the same target")
	}
	if _, command := status473Mouse(pressed, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}); command == nil {
		t.Fatal("unchanged row did not open on release")
	}
}

func mustRowKey(t *testing.T, m statusTUIModel, x, y int) string {
	t.Helper()
	row, ok := m.visibleRow(x, y)
	if !ok {
		t.Fatal("no row under pointer")
	}
	return row.key
}

func TestStatus473ReturningStartsNoWork(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusOverflowModel()
	m.startRefresh()
	before := m.refreshID
	updated, command := m.Update(statusSelectionDoneMsg{})
	if command != nil || updated.(statusTUIModel).refreshID != before || !updated.(statusTUIModel).refreshing {
		t.Fatal("returning from the selection view replayed an action or started another collector")
	}
}
