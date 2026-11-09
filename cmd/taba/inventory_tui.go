package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

// The inventories carry names and metadata only. Keep the terminal model on
// those fields too: neither selected-row details nor narrow layouts may show
// file contents or environment values.
type inventoryTUIRow struct {
	name, scope, state, detail, extra string
	attention                         bool
}

type inventoryTUIModel struct {
	title, summary, heading string
	rows                    []inventoryTUIRow
	width, height           int
	selected, top           int
}

func shortInventoryScope(scope string) string {
	switch {
	case strings.HasPrefix(scope, "all collections with this repository"):
		return "shared"
	case strings.HasPrefix(scope, "all collections"):
		return "machine"
	case strings.HasPrefix(scope, "this collection"):
		return "local"
	default:
		return scope
	}
}

func secretInventoryTUI(c *wtc.Context, result wtc.SecretInventory) error {
	rows := make([]inventoryTUIRow, 0, len(result.Files))
	for _, file := range result.Files {
		state := file.State
		if file.Production {
			state += " · prod"
		}
		ignored := "unknown"
		if file.Ignored != nil {
			ignored = "no"
			if *file.Ignored {
				ignored = "yes"
			}
		}
		detail := "state: " + state + " · scope: " + file.Scope
		extra := fmt.Sprintf("target: %s · gitignored: %s", file.Target, ignored)
		rows = append(rows, inventoryTUIRow{
			name: file.Path, scope: shortInventoryScope(file.Scope), state: state, detail: detail, extra: extra,
			attention: file.State == "not-ignored" || file.State == "linked-not-ignored" ||
				file.State == "unsafe-parent" || file.State == "different-link" || file.State == "other-target",
		})
	}
	return runInventoryTUI(inventoryTUIModel{
		title:   branding.Text("{{command}} secrets · ") + filepath.Base(c.Collection),
		heading: "PATH",
		summary: fmt.Sprintf("%d file paths · shared means available to collections with that repository", len(rows)),
		rows:    rows,
	})
}

func envInventoryTUI(c *wtc.Context, result wtc.EnvInventory) error {
	rows := make([]inventoryTUIRow, 0, len(result.Files)+len(result.Variables))
	for _, file := range result.Files {
		state := "present"
		if !file.Exists {
			state = "absent"
		}
		rows = append(rows, inventoryTUIRow{
			name: "file  " + file.Path, scope: shortInventoryScope(file.Scope), state: state,
			detail: "state: " + state + " · source: " + file.Path, extra: "scope: " + file.Scope,
		})
	}
	for _, variable := range result.Variables {
		state := ""
		detail := "source: " + variable.Source
		if variable.Overrides {
			state = "override"
			detail = "override · " + detail
		}
		rows = append(rows, inventoryTUIRow{
			name: variable.Name, scope: shortInventoryScope(variable.Scope), state: state,
			detail: detail, extra: "scope: " + variable.Scope,
		})
	}
	return runInventoryTUI(inventoryTUIModel{
		title:   branding.Text("{{command}} env · ") + filepath.Base(c.Collection),
		heading: "FILE / KEY",
		summary: fmt.Sprintf("%d source files · %d variable names · values hidden", len(result.Files), len(result.Variables)),
		rows:    rows,
	})
}

func runInventoryTUI(model inventoryTUIModel) error {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("inventory TUI requires a terminal")
	}
	_, err := tea.NewProgram(model).Run()
	return err
}

func (m inventoryTUIModel) Init() tea.Cmd { return nil }

func (m inventoryTUIModel) bodyHeight() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	if height < 8 {
		return max(1, height-4)
	}
	return max(1, height-7)
}

func (m *inventoryTUIModel) keepSelectionVisible() {
	if len(m.rows) == 0 {
		m.selected, m.top = 0, 0
		return
	}
	m.selected = max(0, min(m.selected, len(m.rows)-1))
	m.top = min(m.top, max(0, len(m.rows)-m.bodyHeight()))
	if m.selected < m.top {
		m.top = m.selected
	}
	if m.selected >= m.top+m.bodyHeight() {
		m.top = m.selected - m.bodyHeight() + 1
	}
	m.top = min(m.top, max(0, len(m.rows)-m.bodyHeight()))
}

func (m inventoryTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.keepSelectionVisible()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "down", "j":
			m.selected++
		case "up", "k":
			m.selected--
		case "pgdown", "ctrl+d":
			m.selected += m.bodyHeight()
		case "pgup", "ctrl+u":
			m.selected -= m.bodyHeight()
		case "home", "g":
			m.selected = 0
		case "end", "G":
			m.selected = len(m.rows) - 1
		}
		m.keepSelectionVisible()
	}
	return m, nil
}

func inventoryTUIRowLine(row inventoryTUIRow, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "› "
	} else if row.attention {
		marker = "! "
	}
	if width < 32 {
		return statusTUIFit(marker+statusTUISafe(row.name), width)
	}
	scopeWidth, stateWidth := 8, 17
	if width < 57 {
		scopeWidth, stateWidth = 0, 15
	}
	nameWidth := max(1, width-2-stateWidth-2)
	if scopeWidth > 0 {
		nameWidth -= scopeWidth + 2
	}
	line := marker + statusTUIFit(statusTUISafe(row.name), nameWidth) + "  "
	if scopeWidth > 0 {
		line += statusTUIFit(statusTUISafe(row.scope), scopeWidth) + "  "
	}
	line += statusTUIFit(statusTUISafe(row.state), stateWidth)
	return statusTUIFit(line, width)
}

func (m inventoryTUIModel) View() tea.View {
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	fit := func(value string) string { return statusTUIFitANSI(value, width) }
	lines := []string{
		fit(statusTUIStyle(statusTUISafe(m.title), statusToneHeading)),
		fit(statusTUIStyle(statusTUISafe(m.summary), statusToneDim)),
	}
	header := "  " + m.heading
	if width >= 57 {
		header = statusTUIFit(header, width-29) + "  SCOPE     STATE"
	} else if width >= 32 {
		header = statusTUIFit(header, width-17) + "  STATE"
	}
	lines = append(lines, fit(statusTUIStyle(header, statusToneLabel)))
	end := min(len(m.rows), m.top+m.bodyHeight())
	for index := m.top; index < end; index++ {
		row := m.rows[index]
		line := inventoryTUIRowLine(row, index == m.selected, width)
		if index == m.selected {
			line = statusTUIStyle(line, "7") // Reverse video uses terminal theme colors.
		} else if row.attention {
			line = statusTUIStyle(line, statusToneWarning)
		}
		lines = append(lines, fit(line))
	}
	footer := fmt.Sprintf("%d/%d · ↑↓ select · PgUp/PgDn page · q quit", min(m.selected+1, len(m.rows)), len(m.rows))
	if width < 55 {
		footer = fmt.Sprintf("%d/%d · ↑↓ · q quit", min(m.selected+1, len(m.rows)), len(m.rows))
	}
	if height < 8 {
		for len(lines) < height-1 {
			lines = append(lines, fit(""))
		}
		lines = append(lines, fit(statusTUIStyle(footer, statusToneDim)))
		view := tea.NewView(strings.Join(lines[:min(len(lines), height)], "\n"))
		view.AltScreen = true
		return view
	}
	for len(lines) < max(3, height-4) {
		lines = append(lines, fit(""))
	}
	if len(m.rows) > 0 {
		row := m.rows[max(0, min(m.selected, len(m.rows)-1))]
		lines = append(lines, fit(statusTUIStyle(statusTUISafe(row.name), statusToneHeading)))
		lines = append(lines, fit(statusTUIStyle(statusTUISafe(row.detail), statusToneDim)))
		lines = append(lines, fit(statusTUIStyle(statusTUISafe(row.extra), statusToneDim)))
	} else {
		lines = append(lines, fit("No entries"), fit(""), fit(""))
	}
	lines = append(lines, fit(statusTUIStyle(footer, statusToneDim)))
	if len(lines) > height {
		lines = lines[:height]
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}
