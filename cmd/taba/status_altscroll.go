package main

import (
	"io"
	"os"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Alternate scroll (DEC private mode 1007) asks the terminal to send the
// wheel as cursor keys while the alternate screen is active. Without --mouse
// the live view requests no mouse reporting, so this is how the wheel can
// still scroll it. Bubble Tea has no field for the mode; status queries it
// (DECRQM) and changes it only when the terminal reports it reset, so a
// terminal that already translates the wheel, or does not answer, keeps its
// own setting. A mode status set is reset when the program returns.
const (
	statusAltScrollMode  = ansi.DECMode(1007)
	statusAltScrollSet   = "\x1b[?1007h"
	statusAltScrollReset = "\x1b[?1007l"
)

var (
	statusAltScrollOwned atomic.Bool
	statusTerminalOutput io.Writer           = os.Stdout
	statusProgramOptions []tea.ProgramOption // tests: input/output
)

func (m statusTUIModel) altScrollQuery() tea.Cmd {
	if m.mouseCapture || os.Getenv("TERM") == "dumb" {
		return nil
	}
	return tea.Raw(ansi.RequestMode(statusAltScrollMode))
}

// statusAltScrollReport acts on the terminal's report: only a mode reported
// reset is set (and owned); set, permanent, unrecognized or no reply leave
// the terminal untouched.
func statusAltScrollReport(value ansi.ModeSetting) tea.Cmd {
	if value != ansi.ModeReset || statusAltScrollOwned.Swap(true) {
		return nil
	}
	return tea.Raw(statusAltScrollSet)
}

func statusAltScrollRestore() {
	if statusAltScrollOwned.Swap(false) {
		_, _ = io.WriteString(statusTerminalOutput, statusAltScrollReset)
	}
}
