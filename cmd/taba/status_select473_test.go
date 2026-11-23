package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Issue 473: pointer input must leave ordinary selection usable. These are
// model-level proofs only; terminal selection acceptance is a separate step.

// status473LinkCell finds the first visible cell with an ordinary TUI target.
func status473LinkCell(t *testing.T, m statusTUIModel) (int, int, string) {
	t.Helper()
	for y := 0; y < m.height; y++ {
		for x := 0; x < m.width; x++ {
			if target := m.buildClickTarget(x, y); target != "" {
				return x, y, target
			}
		}
	}
	t.Fatal("synthetic fixture has no visible link target")
	return 0, 0, ""
}

func status473Mouse(m statusTUIModel, event tea.Msg) (statusTUIModel, tea.Cmd) {
	updated, command := m.Update(event)
	return updated.(statusTUIModel), command
}

func TestStatus473LinkOpensOnReleaseNotPress(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusOverflowModel()
	x, y, _ := status473LinkCell(t, m)
	m, command := status473Mouse(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if command != nil {
		t.Fatal("link opened on button press; a drag starting here would activate it")
	}
	if _, command = status473Mouse(m, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}); command == nil {
		t.Fatal("press and release on the same link cell did not open it")
	}
}

func TestStatus473DragFromLinkNeverOpens(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	base := statusOverflowModel()
	x, y, _ := status473LinkCell(t, base)
	cases := map[string][]tea.Msg{
		"drag then release elsewhere": {tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft},
			tea.MouseMotionMsg{X: x + 4, Y: y, Button: tea.MouseLeft}, tea.MouseReleaseMsg{X: x + 4, Y: y, Button: tea.MouseLeft}},
		"drag away and back": {tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft},
			tea.MouseMotionMsg{X: x, Y: y + 1, Button: tea.MouseLeft}, tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft},
			tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}},
		"release on another row": {tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}, tea.MouseReleaseMsg{X: x, Y: y + 1}},
		"right button":           {tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight}, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseRight}},
		"release without press":  {tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}},
		"wheel between press and release": {tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft},
			tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown}, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}},
	}
	for name, events := range cases {
		m := base
		for _, event := range events {
			var command tea.Cmd
			if m, command = status473Mouse(m, event); command != nil {
				t.Fatalf("%s: pointer sequence opened a link", name)
			}
		}
	}
}

func TestStatus473RefreshHeaderTogglesOnReleaseOnly(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusTUIModel{width: 100, height: 20, refreshing: true}
	at := ansi.StringWidth(strings.Split(m.headerLine(), "refreshing")[0])
	pressed, _ := status473Mouse(m, tea.MouseClickMsg{X: at, Y: 0, Button: tea.MouseLeft})
	if pressed.showLog {
		t.Fatal("refresh log toggled on press; selecting header text would flip the view")
	}
	dragged, _ := status473Mouse(pressed, tea.MouseMotionMsg{X: at + 3, Y: 0, Button: tea.MouseLeft})
	if dragged, _ = status473Mouse(dragged, tea.MouseReleaseMsg{X: at + 3, Y: 0, Button: tea.MouseLeft}); dragged.showLog {
		t.Fatal("dragging across the refresh marker toggled the log")
	}
	if released, _ := status473Mouse(pressed, tea.MouseReleaseMsg{X: at, Y: 0, Button: tea.MouseLeft}); !released.showLog {
		t.Fatal("click on the refresh marker no longer opens the log")
	}
}

func TestStatus473SelectionViewIsDiscoverableAndExplicit(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, m := range []statusTUIModel{statusOverflowModel(), {width: 100, height: 40}} {
		for _, noClick := range []bool{false, true} {
			m.noClick = noClick
			if footer := ansi.Strip(m.View().Content); !strings.Contains(footer, "v select") {
				t.Fatalf("footer does not advertise the selection view (no-click=%v): %q", noClick, footer)
			}
			before := m.refreshID
			updated, command := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
			if command == nil {
				t.Fatalf("v did not open a selection view (no-click=%v)", noClick)
			}
			if after := updated.(statusTUIModel); after.refreshID != before || after.refreshing != m.refreshing {
				t.Fatal("opening the selection view started or changed a refresh")
			}
		}
	}
	help := statusTUIModel{width: 100, height: 60, showHelp: true}
	if text := ansi.Strip(help.View().Content); !strings.Contains(text, "v select/copy") || !strings.Contains(text, "drag selects") ||
		!strings.Contains(text, "--mouse") || !strings.Contains(text, "click-release") {
		t.Fatalf("help does not explain selection and link activation: %s", text)
	}
}
