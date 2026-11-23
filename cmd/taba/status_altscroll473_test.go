package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Issue 473: selection-first mouse policy and the alternate-scroll lifecycle.
// Model and byte-stream proofs only; whether a host translates the wheel or
// keeps a drag selection during redraws needs a real terminal.

func status473ResetAltScroll(t *testing.T) {
	t.Helper()
	statusAltScrollOwned.Store(false)
	output, options := statusTerminalOutput, statusProgramOptions
	t.Cleanup(func() {
		statusAltScrollOwned.Store(false)
		statusTerminalOutput, statusProgramOptions = output, options
	})
}

func TestStatus473DefaultLeavesTheMouseToTheTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	m := statusOverflowModel()
	m.refreshing = true
	if mode := m.View().MouseMode; mode != tea.MouseModeNone {
		t.Fatalf("default view requests mouse reporting: %v", mode)
	}
	if !strings.Contains(m.View().Content, "\x1b]8;;https://") {
		t.Fatal("default view lost OSC 8 hyperlinks")
	}
	m.mouseCapture = true
	if mode := m.View().MouseMode; mode != tea.MouseModeCellMotion {
		t.Fatalf("--mouse no longer captures the wheel: %v", mode)
	}
	// Translated wheel input arrives as cursor keys: one row per key.
	m.mouseCapture, m.refreshing = false, false
	before := m.effectiveScroll()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if step := updated.(statusTUIModel).effectiveScroll() - before; step != 1 {
		t.Fatalf("cursor-key step = %d, want 1", step)
	}
}

func TestStatus473AltScrollQueryOnlyWithoutCapture(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	query := statusTUIModel{}.altScrollQuery()
	if query == nil {
		t.Fatal("default view does not query the alternate-scroll mode")
	}
	if raw, ok := query().(tea.RawMsg); !ok || raw.Msg != ansi.RequestMode(statusAltScrollMode) || raw.Msg != "\x1b[?1007$p" {
		t.Fatalf("query is not DECRQM for mode 1007: %#v", query())
	}
	if (statusTUIModel{mouseCapture: true}).altScrollQuery() != nil {
		t.Fatal("--mouse queries alternate scroll")
	}
	t.Setenv("TERM", "dumb")
	if (statusTUIModel{}).altScrollQuery() != nil {
		t.Fatal("TERM=dumb queries alternate scroll")
	}
}

func TestStatus473AltScrollChangesOnlyAReportedResetMode(t *testing.T) {
	for _, value := range []ansi.ModeSetting{ansi.ModeNotRecognized, ansi.ModeSet, ansi.ModePermanentlySet, ansi.ModePermanentlyReset} {
		status473ResetAltScroll(t)
		if command := statusAltScrollReport(value); command != nil || statusAltScrollOwned.Load() {
			t.Fatalf("report %v changed the terminal's alternate-scroll mode", value)
		}
	}
	status473ResetAltScroll(t)
	m := statusTUIModel{width: 80, height: 10}
	updated, command := m.Update(tea.ModeReportMsg{Mode: statusAltScrollMode, Value: ansi.ModeReset})
	if command == nil || command().(tea.RawMsg).Msg != statusAltScrollSet || !statusAltScrollOwned.Load() {
		t.Fatal("a reported reset mode was not set and owned")
	}
	if _, again := updated.Update(tea.ModeReportMsg{Mode: statusAltScrollMode, Value: ansi.ModeReset}); again != nil {
		t.Fatal("a repeated report set the mode twice")
	}
	if _, other := m.Update(tea.ModeReportMsg{Mode: ansi.DECMode(2004), Value: ansi.ModeReset}); other != nil {
		t.Fatal("another mode's report changed alternate scroll")
	}
}

type status473Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *status473Buffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *status473Buffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// status473Program queries like the live view, handles the reply with the
// same function, quits on q and panics on p.
type status473Program struct{}

func (status473Program) Init() tea.Cmd { return statusTUIModel{}.altScrollQuery() }
func (m status473Program) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.ModeReportMsg:
		if msg.Mode == statusAltScrollMode {
			return m, statusAltScrollReport(msg.Value)
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "p":
			panic("synthetic status panic")
		}
	}
	return m, nil
}
func (status473Program) View() tea.View {
	view := tea.NewView("synthetic")
	view.AltScreen = true
	return view
}

func status473RunProgram(t *testing.T, input string, cancelAfter time.Duration) string {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	status473ResetAltScroll(t)
	reader, writer := io.Pipe()
	go func() {
		for _, part := range strings.Split(input, "|") {
			time.Sleep(120 * time.Millisecond)
			_, _ = writer.Write([]byte(part))
		}
	}()
	out := &status473Buffer{}
	statusTerminalOutput = out
	statusProgramOptions = []tea.ProgramOption{tea.WithInput(reader), tea.WithOutput(out), tea.WithWindowSize(80, 24)}
	ctx, cancel := context.WithCancel(context.Background())
	if cancelAfter > 0 {
		time.AfterFunc(cancelAfter, cancel)
	}
	_, _ = runStatusProgram(status473Program{}, ctx, cancel)
	if statusAltScrollOwned.Load() {
		t.Fatal("alternate scroll still owned after the program returned")
	}
	return out.String()
}

func status473AssertRestored(t *testing.T, path, out string) {
	t.Helper()
	set, reset := strings.Index(out, statusAltScrollSet), strings.LastIndex(out, statusAltScrollReset)
	if !strings.Contains(out, "\x1b[?1007$p") || set < 0 || reset < set {
		t.Fatalf("%s: expected query, set, then reset (set=%d reset=%d)", path, set, reset)
	}
	if strings.LastIndex(out, statusAltScrollSet) > reset {
		t.Fatalf("%s: alternate scroll set again after the final reset", path)
	}
	if exit := strings.LastIndex(out, "\x1b[?1049l"); exit > reset {
		t.Fatalf("%s: reset written before the program left the alternate screen", path)
	}
}

func TestStatus473AltScrollRestoredOnEveryProgramReturn(t *testing.T) {
	reply := "\x1b[?1007;2$y" // DECRPM: mode 1007 reset
	status473AssertRestored(t, "quit", status473RunProgram(t, reply+"|q", 0))
	status473AssertRestored(t, "cancel", status473RunProgram(t, reply, 600*time.Millisecond))
	status473AssertRestored(t, "panic", status473RunProgram(t, reply+"|p", 0))
	for name, input := range map[string]string{"already set": "\x1b[?1007;1$y|q", "unrecognized": "\x1b[?1007;0$y|q", "no reply": "q"} {
		out := status473RunProgram(t, input, 0)
		if strings.Contains(out, statusAltScrollSet) || strings.Contains(out, statusAltScrollReset) {
			t.Fatalf("%s: status changed a mode it does not own: %q", name, out)
		}
	}
}

func TestStatus473SelectionViewSuspendsOwnedAltScroll(t *testing.T) {
	m := statusTUIModel{width: 60, height: 10}
	for _, owned := range []bool{true, false} {
		status473ResetAltScroll(t)
		statusAltScrollOwned.Store(owned)
		out := status473Run(t, m.selectionView(time.Now()), "\r")
		hasReset, hasSet := strings.HasPrefix(out, statusAltScrollReset), strings.HasSuffix(out, statusAltScrollSet)
		if owned != hasReset || owned != hasSet {
			t.Fatalf("owned=%v: selection view reset-first=%v set-last=%v", owned, hasReset, hasSet)
		}
	}
}

func TestStatus473DevHandoverResetsOwnedAltScrollBeforeExec(t *testing.T) {
	status473ResetAltScroll(t)
	out := &status473Buffer{}
	statusTerminalOutput = out
	statusAltScrollOwned.Store(true)
	restart := statusRestartDev
	t.Cleanup(func() { statusRestartDev = restart })
	called := false
	statusRestartDev = func(collection, binary, generation string, _ statusTUIPresentation) error {
		called = true
		if statusAltScrollOwned.Load() || out.String() != statusAltScrollReset {
			t.Fatalf("exec handover ran with alternate scroll still owned (written %q)", out.String())
		}
		return nil
	}
	m := statusTUIModel{devRestart: &statusDevRestart{collection: "synthetic", binary: "/synthetic/taba", generation: "g1"}}
	if err := statusDevHandover(m); err != nil || !called {
		t.Fatalf("handover did not reach the restart (called=%v err=%v)", called, err)
	}
}
