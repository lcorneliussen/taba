package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

type statusStartupOptions struct {
	all, procs, reposOnly, noFetch, noClick, click, mouse bool
	reposChanged, noClickChanged                          bool
	watchSeconds, fetchAgeSeconds                         int
	incompatible                                          bool
}
type statusDiscoveredMsg struct {
	receiver *wtc.Context
	err      error
}
type statusStartupModel struct {
	ctx           context.Context
	cancel        context.CancelFunc
	cwd           string
	args          []string
	options       statusStartupOptions
	width, height int
	help          bool
	activityAt    time.Time
	receiver      *wtc.Context
	err           error
}

func (m statusStartupModel) Init() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		c, err := wtc.DiscoverContext(ctx, m.cwd)
		if err == nil && len(m.args) != 0 {
			name := m.args[0]
			if name == "." || name == ".." || filepath.Base(name) != name {
				err = fmt.Errorf("expected a collection name")
			} else if err = c.RequireWorkspace("status <collection>"); err == nil {
				c, err = wtc.OpenCollectionContext(ctx, filepath.Join(c.Workspace, name))
			}
		}
		if err == nil {
			c = c.WithStatusContext(m.ctx)
		}
		return statusDiscoveredMsg{c, err}
	}
}
func (m statusStartupModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "?":
			m.activityAt = time.Now()
			m.help = !m.help
		case "esc":
			m.activityAt = time.Now()
			m.help = false
		}
	case statusDiscoveredMsg:
		if m.ctx.Err() != nil {
			return m, tea.Quit
		}
		m.receiver, m.err = msg.receiver, msg.err
		if msg.err != nil {
			return m, tea.Quit
		}
		opt := m.options
		seconds := statusIntervalSetting(msg.receiver, "WTC_STATUS_WATCH", 30)
		if opt.watchSeconds > 0 {
			seconds = opt.watchSeconds
		}
		if seconds == 0 {
			return m, tea.Quit
		} // Configured one-shot continues after closing the loading view.
		if opt.incompatible {
			m.err = fmt.Errorf("--tui/--watch cannot be combined with preview, cached, Markdown, or JSON output")
			return m, tea.Quit
		}
		background := statusIntervalSetting(msg.receiver, "WTC_STATUS_WATCH_BG", 300)
		if background == 0 {
			background = seconds
		}
		if !opt.procs && !opt.reposChanged && statusTrue(statusSetting(msg.receiver, "WTC_STATUS_REPOS")) {
			opt.reposOnly = true
		}
		clickSetting := statusSetting(msg.receiver, "WTC_STATUS_NO_CLICK")
		if clickSetting == "" {
			clickSetting = statusSetting(msg.receiver, "HARNESS_STATUS_NO_CLICK")
		}
		if !opt.noClickChanged && statusTrue(clickSetting) {
			opt.noClick = true
		}
		if opt.click {
			opt.noClick = false
		}
		next := newStatusTUIModel(msg.receiver, opt.all, opt.procs, opt.reposOnly, opt.noFetch, opt.noClick, opt.mouse, time.Duration(seconds)*time.Second, time.Duration(background)*time.Second, time.Duration(opt.fetchAgeSeconds)*time.Second, m.ctx, m.cancel)
		next.width, next.height = m.width, m.height
		next.showHelp = m.help
		if !m.activityAt.IsZero() {
			next.observedUntil = m.activityAt.Add(time.Minute)
			next.focused = time.Now().Before(next.observedUntil)
		}
		return next, next.Init()
	}
	return m, nil
}
func (m statusStartupModel) View() tea.View {
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	lines := []string{branding.Product + " status · loading", "Validating collection scope…"}
	if m.help {
		lines = append(lines, "Waiting for scope validation.", "No saved facts or actions yet.", "? help · q quit")
	}
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, "? help · q quit")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = statusTUIFitANSI(line, width)
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}

func runStatusStartup(cwd string, args []string, options statusStartupOptions) (*wtc.Context, bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	final, err := runStatusProgram(statusStartupModel{ctx: ctx, cancel: cancel, cwd: cwd, args: args, options: options}, ctx, cancel)
	if start, ok := final.(statusStartupModel); ok {
		if start.receiver != nil {
			return start.receiver.WithStatusContext(context.Background()), false, err
		}
		return nil, true, err
	}
	return nil, true, err
}

func statusSnapshotTime(snapshot wtc.StatusSnapshot) time.Time {
	at, err := time.Parse(time.RFC3339Nano, snapshot.GeneratedAt)
	if err != nil || at.After(time.Now()) {
		return time.Time{}
	}
	return at
}
