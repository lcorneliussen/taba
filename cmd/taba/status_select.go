package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// statusSelectionView is the explicit full-output selection view (issue 473).
// Bubble Tea releases the terminal while it runs: the alternate screen and
// mouse reporting are off, so the terminal and any multiplexer select, scroll
// and copy natively. The text is captured when the view opens and is written
// once into the normal screen, where offscreen rows become ordinary
// scrollback. Refresh workers keep running; their results apply on return.
type statusSelectionView struct {
	text   string
	stdin  io.Reader
	stdout io.Writer
	quit   bool
}

type statusSelectionDoneMsg struct {
	quit bool
	err  error
}

func (m statusTUIModel) selectionView(now time.Time) *statusSelectionView {
	width := m.width
	if width <= 0 {
		width = 80
	}
	rule := func(text string) []string {
		return strings.Split(ansi.Wrap(statusTUIStyle(text, statusToneDim), width, ""), "\n")
	}
	lines := rule("── selection view · captured " + now.Format("15:04:05") + " · full output; select and scroll with your terminal ──")
	for _, line := range m.contentLayout().lines {
		lines = append(lines, strings.TrimRight(statusTUIFitANSI(line, width), " "))
	}
	lines = append(lines, rule("── end of selection view · refresh continues · Enter, q or v returns · Ctrl+C quits ──")...)
	return &statusSelectionView{text: "\n" + strings.Join(lines, "\n") + "\n"}
}

func (v *statusSelectionView) SetStdin(r io.Reader)  { v.stdin = r }
func (v *statusSelectionView) SetStdout(w io.Writer) { v.stdout = w }
func (v *statusSelectionView) SetStderr(io.Writer)   {}

// Run writes the captured text while the terminal is still in its ordinary
// line mode, then waits for an explicit return key. Escape sequences (arrows,
// Page keys a terminal forwards) are ignored so scrolling never exits.
func (v *statusSelectionView) Run() error {
	if v.stdout == nil {
		v.stdout = os.Stdout
	}
	// Alternate scroll set by status is off while the terminal is released
	// and back on before Bubble Tea restores the live view.
	if statusAltScrollOwned.Load() {
		_, _ = io.WriteString(v.stdout, statusAltScrollReset)
		defer io.WriteString(v.stdout, statusAltScrollSet) //nolint:errcheck
	}
	if _, err := io.WriteString(v.stdout, v.text); err != nil {
		return err
	}
	if v.stdin == nil {
		return nil
	}
	if file, ok := v.stdin.(interface{ Fd() uintptr }); ok && term.IsTerminal(file.Fd()) {
		if state, err := term.MakeRaw(file.Fd()); err == nil {
			defer term.Restore(file.Fd(), state) //nolint:errcheck
		}
	}
	buffer := make([]byte, 64)
	for {
		n, err := v.stdin.Read(buffer)
		chunk := buffer[:n]
		if strings.ContainsRune(string(chunk), 0x03) {
			v.quit = true
			return nil
		}
		if n > 0 && chunk[0] != 0x1b && strings.ContainsAny(string(chunk), "\r\nqQvV") {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func statusSelectionCommand(view *statusSelectionView) tea.Cmd {
	return tea.Exec(view, func(err error) tea.Msg {
		return statusSelectionDoneMsg{quit: view.quit, err: err}
	})
}
