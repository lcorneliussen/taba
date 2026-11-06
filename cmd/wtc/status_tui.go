package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/lcorneliussen/wtc-cli/internal/branding"
	"github.com/lcorneliussen/wtc-cli/internal/wtc"
	"github.com/mattn/go-runewidth"
)

type statusTickMsg time.Time

type statusRefreshStartedMsg struct{ events <-chan tea.Msg }

type statusProgressMsg struct {
	message string
	at      time.Time
	events  <-chan tea.Msg
}

type statusLoadedMsg struct {
	snapshot  wtc.StatusSnapshot
	processes []wtc.StatusProcess
	fetched   wtc.StatusFetchReport
	err       error
	at        time.Time
}

type statusOpenedMsg struct{ err error }

type statusTUIModel struct {
	context        *wtc.Context
	runtimeReading bool      // a service state read is in flight
	runtimeReadAt  time.Time // when the last one started
	snapshot       wtc.StatusSnapshot
	all            bool
	procs          bool
	reposOnly      bool
	processes      []wtc.StatusProcess
	noFetch        bool
	fetchAge       time.Duration
	noClick        bool
	interval       time.Duration
	background     time.Duration
	nextRefresh    time.Time
	lastRefresh    time.Time
	width          int
	height         int
	scroll         int
	focused        bool
	refreshing     bool
	showHelp       bool
	showArchived   bool
	showLog        bool
	showNext       bool
	selectedNext   int
	nextRunning    bool
	nextNotice     string
	nextSelected   map[string]wtc.StatusRepo
	nextPreview    []wtc.StatusRepo
	nextPending    map[string]bool
	nextCancel     context.CancelFunc
	progressLog    []string
	progressStep   string
	progressKey    string
	startedAt      time.Time
	errorText      string
	hover          string
}

// Row locations come from the renderers, including conditional detail lines.
// The viewport and hit testing use this same layout.
type statusTUIRow struct {
	key        string
	start, end int
	repo       *wtc.StatusRepo
	service    *statusRuntimeGroup
	pr         *wtc.StatusPRRow
}

type statusTUIContent struct {
	lines       []string
	rows        []statusTUIRow
	nextRows    []statusTUIRow
	noticeStart int
}

type statusTUIViewport struct {
	header, body, scroll, limit int
}

type statusTUIAnchor struct {
	key    string
	offset int
}

func (m statusTUIModel) viewport(content statusTUIContent) statusTUIViewport {
	height := m.height
	if height <= 0 {
		height = 24
	}
	available := max(1, height-2)
	if height <= 3 {
		available = max(1, height-1)
	}
	header := min(len(content.lines), min(2, max(1, available-1)))
	body := max(0, available-header)
	limit := max(0, len(content.lines)-header-body)
	if body == 0 {
		limit = 0
	}
	return statusTUIViewport{header: header, body: body, scroll: min(max(0, m.scroll), limit), limit: limit}
}

func (m *statusTUIModel) scrollBy(delta int) {
	v := m.viewport(m.contentLayout())
	m.scroll = min(max(0, v.scroll+delta), v.limit)
	m.hover = ""
}

func (m statusTUIModel) scrollAnchor() statusTUIAnchor {
	content := m.contentLayout()
	v := m.viewport(content)
	if v.scroll == 0 {
		return statusTUIAnchor{}
	}
	line := v.header + v.scroll
	distance := func(a, b int) int { return max(a-b, b-a) }
	var closest *statusTUIRow
	for i := range content.rows {
		r := &content.rows[i]
		if closest == nil || distance(line, r.start) < distance(line, closest.start) {
			closest = r
		}
	}
	if closest != nil {
		return statusTUIAnchor{key: closest.key, offset: line - closest.start}
	}
	return statusTUIAnchor{}
}

func (m *statusTUIModel) restoreScroll(anchor statusTUIAnchor) {
	content := m.contentLayout()
	v := m.viewport(content)
	if anchor.key != "" {
		for _, row := range content.rows {
			if row.key == anchor.key {
				m.scroll = row.start + anchor.offset - v.header
				break
			}
		}
	}
	m.scroll = m.viewport(content).scroll
	m.hover = ""
}

func (m *statusTUIModel) revealNext() {
	content := m.contentLayout()
	v := m.viewport(content)
	if v.body == 0 || m.selectedNext >= len(content.nextRows) {
		m.scroll = v.scroll
		return
	}
	row := content.nextRows[m.selectedNext]
	start, end := row.start-v.header, row.end-v.header
	switch {
	case start < v.scroll || end-start > v.body:
		m.scroll = start
	case end > v.scroll+v.body:
		m.scroll = end - v.body
	default:
		m.scroll = v.scroll
	}
	m.scroll = m.viewport(content).scroll
	m.hover = ""
}

func (m statusTUIModel) selectedNextVisible() bool {
	if !m.showNext {
		return false
	}
	content := m.contentLayout()
	v := m.viewport(content)
	return m.selectedNext < len(content.nextRows) &&
		content.nextRows[m.selectedNext].start >= v.header+v.scroll &&
		content.nextRows[m.selectedNext].start < v.header+v.scroll+v.body
}

func (m *statusTUIModel) revealNextNotice() {
	if !m.showNext || m.showLog || m.nextNotice == "" {
		return
	}
	content := m.contentLayout()
	v := m.viewport(content)
	m.scroll = min(max(0, content.noticeStart-v.header), v.limit)
	m.hover = ""
}

func (m statusTUIModel) visibleRow(x, y int) (statusTUIRow, bool) {
	content := m.contentLayout()
	v := m.viewport(content)
	width := m.width
	if width <= 0 {
		width = 80
	}
	if x < 0 || x >= width || y < v.header || y >= v.header+v.body {
		return statusTUIRow{}, false
	}
	line := y + v.scroll
	for _, row := range content.rows {
		if line >= row.start && line < row.end {
			return row, true
		}
	}
	return statusTUIRow{}, false
}

func statusTUIRepoKey(row wtc.StatusRepo) string {
	return "repo:" + row.Collection + "\x00" + row.Dir + "\x00" + row.Worktree
}

func statusTUITick() tea.Cmd {
	return tea.Tick(time.Second, func(at time.Time) tea.Msg { return statusTickMsg(at) })
}

func statusTUIRefresh(c *wtc.Context, all, procs, noFetch bool, fetchAge time.Duration) tea.Cmd {
	return statusTUIRefreshWithCollector(c, procs, func(collector *wtc.Context) statusLoadedMsg {
		snapshot, report, err := collector.StatusLiveSnapshotWithFetchAge(all, noFetch, fetchAge)
		return statusLoadedMsg{snapshot: snapshot, fetched: report, err: err, at: time.Now()}
	})
}

func statusTUIRefreshWithCollector(c *wtc.Context, procs bool, collect func(*wtc.Context) statusLoadedMsg) tea.Cmd {
	return func() tea.Msg {
		events := make(chan tea.Msg, 128)
		go func() {
			if procs {
				processes, err := c.StatusProcesses()
				events <- statusLoadedMsg{processes: processes, err: err, at: time.Now()}
				return
			}
			collector := *c
			collector.StatusProgress = func(message string) {
				select {
				case events <- statusProgressMsg{message: message, at: time.Now(), events: events}:
				default: // Keep collecting if the terminal cannot render every step.
				}
			}
			events <- collect(&collector)
		}()
		return statusRefreshStartedMsg{events: events}
	}
}

func statusTUIWait(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

func (m *statusTUIModel) startRefresh() {
	m.refreshing = true
	m.startedAt = time.Now()
	m.progressStep = "Starting refresh"
	m.progressKey = m.progressStep
	m.progressLog = []string{"0s  Starting refresh"}
}

var statusTUIProgressCounter = regexp.MustCompile(`^(.*) [0-9]+(?:/[0-9]+)?$`)

func statusTUIProgressKey(message string) string {
	if parts := statusTUIProgressCounter.FindStringSubmatch(message); len(parts) == 2 {
		return parts[1]
	}
	return message
}

func (m *statusTUIModel) focusLogTail() {
	if m.showLog {
		m.scroll = m.viewport(m.contentLayout()).limit
	}
}

func (m statusTUIModel) Init() tea.Cmd {
	cmds := []tea.Cmd{statusTUITick(), statusTUIRefresh(m.context, m.all, m.procs, m.noFetch, m.fetchAge)}
	if m.context != nil && !m.all && !m.procs {
		cmds = append(cmds, statusTUIRuntimeTick())
	}
	return tea.Batch(cmds...)
}

// statusRuntimeMsg carries service states. They are read on their own short
// cycle: a full refresh, with its fetches and forge calls, would show a
// stopped or crashed service only minutes later.
type statusRuntimeMsg struct {
	items []wtc.RuntimeItem
	at    time.Time
}

// statusRuntimeTickMsg is the one service timer. Each tick reads the states
// when a wtc svc action signalled a change, or when the last read is older
// than the polling interval, and otherwise just ticks again.
type statusRuntimeTickMsg struct{}

const statusRuntimeTickEvery = 500 * time.Millisecond

func statusTUIRuntimeRefresh(c *wtc.Context) tea.Cmd {
	return func() tea.Msg { return statusRuntimeMsg{items: c.RuntimeStatus(), at: time.Now()} }
}

func statusTUIRuntimeTick() tea.Cmd {
	return tea.Tick(statusRuntimeTickEvery, func(time.Time) tea.Msg { return statusRuntimeTickMsg{} })
}

// runtimeDue says whether the next tick reads the service states.
func (m statusTUIModel) runtimeDue(now time.Time) bool {
	if m.runtimeReading {
		return false
	}
	if signal := m.context.ServicesChangedAt(); signal.After(m.runtimeReadAt) {
		return true
	}
	every := 3 * time.Second
	if !m.focused {
		every = 15 * time.Second
	}
	return now.Sub(m.runtimeReadAt) >= every
}

func (m statusTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		anchor := m.scrollAnchor()
		selectedVisible := m.selectedNextVisible()
		m.width, m.height = msg.Width, msg.Height
		m.restoreScroll(anchor)
		if selectedVisible {
			m.revealNext()
		}
	case tea.FocusMsg:
		m.focused = true
		m.nextRefresh = time.Now().Add(m.interval)
	case tea.BlurMsg:
		m.focused = false
		m.nextRefresh = time.Now().Add(m.background)
	case statusRuntimeMsg:
		anchor := m.scrollAnchor()
		m.snapshot.Runtime, m.runtimeReading = msg.items, false
		m.restoreScroll(anchor)
		return m, statusTUIRuntimeTick()
	case statusRuntimeTickMsg:
		if m.context == nil {
			return m, nil
		}
		if now := time.Now(); m.runtimeDue(now) {
			// Stamp the read when it starts: a signal during it reads again.
			m.runtimeReading, m.runtimeReadAt = true, now
			return m, statusTUIRuntimeRefresh(m.context)
		}
		return m, statusTUIRuntimeTick()
	case statusRefreshStartedMsg:
		return m, statusTUIWait(msg.events)
	case statusProgressMsg:
		if m.startedAt.IsZero() {
			m.startedAt = msg.at
		}
		m.progressStep = msg.message
		entry := fmt.Sprintf("%s  %s", msg.at.Sub(m.startedAt).Truncate(time.Second), msg.message)
		key := statusTUIProgressKey(msg.message)
		if key == m.progressKey && len(m.progressLog) > 0 {
			m.progressLog[len(m.progressLog)-1] = entry
		} else {
			m.progressLog = append(m.progressLog, entry)
		}
		m.progressKey = key
		m.focusLogTail()
		return m, statusTUIWait(msg.events)
	case statusLoadedMsg:
		anchor := m.scrollAnchor()
		selectedVisible := m.selectedNextVisible()
		selectedKey := ""
		if rows := statusNextRepos(m.snapshot); m.selectedNext < len(rows) {
			selectedKey = statusNextIdentity(rows[m.selectedNext])
		}
		if m.startedAt.IsZero() {
			m.startedAt = msg.at
		}
		m.refreshing = false
		m.progressStep = ""
		outcome := "Refresh finished"
		if msg.err != nil {
			outcome = "Refresh failed: " + msg.err.Error()
		} else if msg.fetched.Failed != 0 {
			plural := "es"
			if msg.fetched.Failed == 1 {
				plural = ""
			}
			outcome = fmt.Sprintf("Refresh finished: %d ref refresh%s failed; showing local refs", msg.fetched.Failed, plural)
		}
		m.progressLog = append(m.progressLog, fmt.Sprintf("%s  %s", msg.at.Sub(m.startedAt).Truncate(time.Second), outcome))
		m.focusLogTail()
		m.lastRefresh = msg.at
		m.nextRefresh = msg.at.Add(m.interval)
		if !m.focused {
			m.nextRefresh = msg.at.Add(m.background)
		}
		if msg.err != nil {
			m.errorText = msg.err.Error()
		} else {
			// Service states from the short cycle are newer than the ones a
			// full refresh read when it started.
			live := m.snapshot.Runtime
			m.snapshot = msg.snapshot
			m.selectedNext = min(m.selectedNext, max(0, len(statusNextRepos(m.snapshot))-1))
			for i, row := range statusNextRepos(m.snapshot) {
				if statusNextIdentity(row) == selectedKey {
					m.selectedNext = i
					break
				}
			}
			if live != nil && m.context != nil && !m.all && !m.procs {
				m.snapshot.Runtime = live
			}
			m.processes = msg.processes
			m.errorText = ""
			if msg.fetched.Failed != 0 {
				m.errorText = fmt.Sprintf("%d ref refresh(es) failed; showing local refs", msg.fetched.Failed)
			}
		}
		if m.showLog {
			m.focusLogTail()
		} else {
			m.restoreScroll(anchor)
			if selectedVisible {
				m.revealNext()
			}
		}
	case statusTickMsg:
		if !m.refreshing && !m.nextRunning && !m.nextRefresh.IsZero() && !time.Now().Before(m.nextRefresh) {
			m.startRefresh()
			return m, tea.Batch(statusTUITick(), statusTUIRefresh(m.context, m.all, m.procs, m.noFetch, m.fetchAge))
		}
		return m, statusTUITick()
	case tea.MouseMotionMsg:
		m.hover = ""
		if item, ok := m.runtimeItemAt(msg.X, msg.Y); ok {
			m.hover = statusRuntimeHover(item)
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scrollBy(-3)
		case tea.MouseWheelDown:
			m.scrollBy(3)
		}
	case tea.MouseClickMsg:
		header := m.headerLine()
		refreshIndex := strings.Index(header, "refreshing")
		headerWidth := m.width
		if headerWidth <= 0 {
			headerWidth = 80
		}
		if msg.Y == 0 && m.refreshing && refreshIndex >= 0 &&
			msg.X >= runewidth.StringWidth(header[:refreshIndex]) &&
			msg.X < min(headerWidth, runewidth.StringWidth(header)) {
			m.showLog = !m.showLog
			m.scroll = 0
			m.focusLogTail()
			if !m.showLog && m.showNext {
				m.revealNext()
			}
			return m, nil
		}
		if !m.noClick && !m.procs && !m.showNext {
			if target := m.buildClickTarget(msg.X, msg.Y); target != "" {
				return m, statusOpenURL(target)
			}
		}
	case statusOpenedMsg:
		if msg.err != nil {
			m.errorText = msg.err.Error()
		}
	case statusNextDoneMsg:
		if m.nextPending == nil {
			m.nextPending = map[string]bool{}
		}
		for _, key := range msg.pending {
			m.nextPending[key] = true
		}
		if m.nextCancel != nil {
			m.nextCancel()
			m.nextCancel = nil
		}
		m.nextPreview = nil
		m.nextSelected = nil
		m.nextRunning = false
		m.nextNotice = msg.text
		if msg.err != nil {
			m.nextNotice = msg.err.Error() + "\n" + msg.text
		}
		m.revealNextNotice()
		if !m.refreshing {
			m.startRefresh()
			return m, statusTUIRefresh(m.context, m.all, m.procs, m.noFetch, m.fetchAge)
		}
	case tea.KeyPressMsg:
		if m.showNext && !m.showLog && msg.String() != "q" && msg.String() != "ctrl+c" {
			repos := statusNextRepos(m.snapshot)
			switch msg.String() {
			case "esc", "n":
				m.showNext, m.scroll = false, 0
			case "up", "k":
				m.selectedNext = max(0, m.selectedNext-1)
				m.revealNext()
			case "down", "j":
				m.selectedNext = min(max(0, len(repos)-1), m.selectedNext+1)
				m.revealNext()
			case "pgup":
				m.scrollBy(-max(1, m.viewport(m.contentLayout()).body))
			case "pgdown":
				m.scrollBy(max(1, m.viewport(m.contentLayout()).body))
			case "home":
				m.scroll, m.hover = 0, ""
			case "end":
				m.scroll = m.viewport(m.contentLayout()).limit
				m.hover = ""
			case "space", " ":
				if !m.nextRunning && m.selectedNext < len(repos) {
					if m.nextSelected == nil {
						m.nextSelected = map[string]wtc.StatusRepo{}
					}
					r := repos[m.selectedNext]
					id := statusNextIdentity(r)
					if _, ok := m.nextSelected[id]; ok {
						delete(m.nextSelected, id)
					} else if !m.nextPending[statusNextPendingKey(r)] {
						m.nextSelected[id] = r
					}
					m.nextPreview = nil
				}
			case "right", "l":
				if !m.nextRunning && m.selectedNext < len(repos) {
					if m.nextSelected == nil {
						m.nextSelected = map[string]wtc.StatusRepo{}
					}
					r := repos[m.selectedNext]
					id := statusNextIdentity(r)
					if old, ok := m.nextSelected[id]; ok {
						r.Next = old.Next
					}
					m.nextSelected[id] = statusNextCycle(r)
					m.nextPreview = nil
				}
			case "s":
				if !m.nextRunning {
					if m.nextSelected == nil {
						m.nextSelected = map[string]wtc.StatusRepo{}
					}
					for _, r := range repos {
						if _, ok := m.nextSelected[statusNextIdentity(r)]; !ok && !m.nextPending[statusNextPendingKey(r)] && (r.Choices == nil || r.Choices.Select(r.Next.Key) != nil) {
							m.nextSelected[statusNextIdentity(r)] = r
						}
					}
					m.nextPreview = nil
				}
			case "x":
				if !m.nextRunning {
					m.nextPending = nil
					m.nextNotice = "Retry enabled. Check the author conversation before selecting unchanged work again."
					m.revealNextNotice()
				}
			case "c":
				if m.nextCancel != nil {
					m.nextCancel()
					m.nextNotice = "Cancelling queued actions; active work is not undone…"
					m.revealNextNotice()
				}
			case "enter":
				if !m.refreshing && !m.nextRunning && len(m.nextSelected) > 0 {
					for _, r := range m.nextSelected {
						if m.nextPending[statusNextPendingKey(r)] {
							m.nextNotice = "An unchanged handoff is pending. Check the author conversation; x allows an explicit retry."
							m.revealNextNotice()
							return m, nil
						}
					}
					if m.nextPreview == nil && statusNextConfirmTargets(statusNextTargets(m.snapshot, m.nextSelected)) {
						var lines []string
						m.nextPreview = statusNextTargets(m.snapshot, m.nextSelected)
						for _, target := range m.nextPreview {
							lines = append(lines, target.Dir+": "+statusNextLabel(target.Next))
						}
						m.nextNotice = "Selected single steps:\n" + strings.Join(lines, "\n") + "\nEnter confirms; Space changes selection. Publication retains its authority requirements."
						m.revealNextNotice()
						return m, nil
					}
					if m.nextPreview == nil {
						m.nextPreview = statusNextTargets(m.snapshot, m.nextSelected)
					}
					ctx, cancel := context.WithCancel(context.Background())
					m.nextCancel = cancel
					m.nextRunning = true
					m.nextNotice = "Rechecking selected actions; c cancels queued work…"
					m.revealNextNotice()
					return m, statusNextBatch(ctx, m.context, append([]wtc.StatusRepo(nil), m.nextPreview...), statusNextDefaultRunner())
				}
				if !m.refreshing && !m.nextRunning && m.selectedNext < len(repos) {
					if m.nextPending[statusNextPendingKey(repos[m.selectedNext])] {
						m.nextNotice = "An unchanged handoff is pending. Check the author conversation; x allows an explicit retry."
						m.revealNextNotice()
						return m, nil
					}
					m.nextRunning, m.nextNotice = true, "Rechecking the selected step…"
					m.revealNextNotice()
					ctx, cancel := context.WithCancel(context.Background())
					m.nextCancel = cancel
					return m, statusNextRunInContext(ctx, m.context, repos[m.selectedNext], statusNextDefaultRunner(), false)
				}
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			if m.nextCancel != nil {
				m.nextCancel()
			}
			return m, tea.Quit
		case "esc":
			m.showHelp = false
		case "?":
			m.showHelp = !m.showHelp
			m.scroll = 0
		case "n":
			if !m.procs {
				m.showNext, m.showLog, m.selectedNext, m.scroll = true, false, 0, 0
				m.revealNext()
			}
		case "a":
			anchor := m.scrollAnchor()
			m.showArchived = !m.showArchived
			m.restoreScroll(anchor)
		case "r":
			if !m.refreshing && !m.nextRunning {
				m.startRefresh()
				return m, statusTUIRefresh(m.context, m.all, m.procs, m.noFetch, m.fetchAge)
			}
		case "l":
			m.showLog = !m.showLog
			m.scroll = 0
			m.focusLogTail()
			if !m.showLog && m.showNext {
				m.revealNext()
			}
		case "up", "k":
			m.scrollBy(-1)
		case "down", "j":
			m.scrollBy(1)
		case "pgup":
			m.scrollBy(-max(1, m.viewport(m.contentLayout()).body))
		case "pgdown":
			m.scrollBy(max(1, m.viewport(m.contentLayout()).body))
		case "home":
			m.scroll, m.hover = 0, ""
		case "end":
			m.scroll = m.viewport(m.contentLayout()).limit
			m.hover = ""
		}
	}
	return m, nil
}

func statusTUIFit(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(value) <= width {
		return value + strings.Repeat(" ", width-runewidth.StringWidth(value))
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range value {
		w := runewidth.RuneWidth(r)
		if used+w >= width {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + strings.Repeat(" ", width-1-used) + "…"
}

func statusTUIPRConflict(pr *wtc.StatusPRFacts) bool {
	return pr != nil && (pr.Merge == "CONFLICTING" || pr.Merge == "DIRTY")
}

func statusTUIBehind(row wtc.StatusRepo) string {
	count := ""
	if row.Behind > 0 {
		count = fmt.Sprint(row.Behind)
	}
	if row.Conflict || statusTUIPRConflict(row.PR) {
		return "✗" + count
	}
	if count == "" {
		return "·"
	}
	return count
}

func statusTUIPRNumber(number string) string {
	if number == "" {
		return "·"
	}
	return "#" + statusTUISafe(number)
}

func statusTUIPRSignals(checks, merge, review *string, styled bool) [3]string {
	cells := [3]string{statusTUIPRGlyph(checks), statusTUIPRGlyph(merge), statusTUIPRGlyph(review)}
	if styled {
		for i := range cells {
			cells[i] = statusTUIGlyphStyle(cells[i])
		}
	}
	return cells
}

func statusTUIPRSuffix(pr *wtc.StatusPRFacts, styled bool) string {
	cells := statusTUIPRSignals(&pr.Checks, &pr.Merge, &pr.Review, styled)
	suffix := strings.Join(cells[:], " ")
	if statusTUIPRConflict(pr) {
		warning := " conflict"
		if styled {
			warning = statusTUIStyle(warning, statusToneFailure)
		}
		suffix += warning
	}
	if pr.Draft {
		suffix += " ◇"
	}
	return suffix
}

// Numbers use the cohort width. Reserve " ↗" when it fits; the identifier
// takes priority over that optional marker within the same column.
func statusTUIPRNumberCell(number, target string, width int, styled bool, tone string) string {
	labelWidth, slot := max(1, width-2), "  "
	if ansi.StringWidth(statusTUIPRNumber(number)) > labelWidth {
		labelWidth, slot = width, ""
	}
	label := statusTUIFit(statusTUIPRNumber(number), labelWidth)
	if styled && number != "" && statusTUIURL(target) != "" {
		if slot != "" {
			slot = " ↗"
		}
		return statusTUILinkTone(label+slot, target, tone)
	}
	if styled {
		label = statusTUIStyle(label, statusToneLabel)
	}
	return label + slot
}

type statusRepoLayout struct {
	name, branch, pr, number, tree, ahead, behind, tip, prod, runtime int
	showTree, showSync, showBuilds, showRuntime                       bool
	hasTip, hasProd                                                   bool
}

func statusTUIRepoLayout(snapshot wtc.StatusSnapshot, width int) statusRepoLayout {
	l := statusRepoLayout{name: 16, branch: 30, pr: 15, number: 1, tree: 4, ahead: 3, behind: 3, tip: 8, prod: 8,
		showTree: true, showSync: true}
	compactPRWidth := 1
	for _, row := range snapshot.Repos {
		if marks := len(statusRuntimeSlots(statusRuntimeItems(snapshot, row))); marks > 0 && width >= 60 {
			l.showRuntime = true
			// One column of air after the marks so they do not run into PR.
			l.runtime = max(l.runtime, len("LOCAL")+1, marks+1)
		}
		l.hasTip = l.hasTip || row.Tip != nil
		l.hasProd = l.hasProd || row.Prod != nil
		if row.Tree != "clean" {
			l.tree = max(l.tree, runewidth.StringWidth(row.Tree))
		}
		l.ahead = max(l.ahead, len(fmt.Sprint(row.Ahead)))
		l.behind = max(l.behind, runewidth.StringWidth(statusTUIBehind(row)))
		if row.PR != nil && row.PR.Number != "" {
			identifierWidth := ansi.StringWidth(statusTUIPRNumber(row.PR.Number))
			l.number = max(l.number, identifierWidth)
			if row.PR.Draft {
				identifierWidth += 2
			}
			if statusTUIPRConflict(row.PR) {
				identifierWidth += 2
			}
			compactPRWidth = max(compactPRWidth, identifierWidth)
		}
	}
	l.pr = max(l.pr, l.number+8)
	suffixWidth := 5
	for _, row := range snapshot.Repos {
		if row.PR != nil && row.PR.Number != "" {
			suffixWidth = max(suffixWidth, ansi.StringWidth(statusTUIPRSuffix(row.PR, false)))
		}
	}
	l.pr = max(l.pr, l.number+3+suffixWidth)
	// Build columns exist only for the tiers a provider reported on.
	l.showBuilds = l.hasTip || l.hasProd
	if !l.hasTip {
		l.tip = 0
	}
	if !l.hasProd {
		l.prod = 0
	}
	if width >= 100 {
		l.name = 20
	}
	if width < 72 {
		l.showBuilds = false
	}
	if width < 55 {
		l.showSync = false
	}
	if width < 46 {
		l.name = 10
	}
	if width < 40 {
		l.name = 8
		l.showSync, l.showBuilds = false, false
		l.showTree = width >= 31
	}
	other := func() int {
		n := l.name + 1 + l.pr
		if l.showRuntime {
			n += 1 + l.runtime
		}
		if l.showTree {
			n += 1 + l.tree
		}
		if l.showSync {
			n += 1 + l.ahead + 1 + l.behind
		}
		if l.showBuilds {
			n += l.buildsWidth()
		}
		return n
	}
	minBranch := 8
	if width < 31 {
		minBranch = 6
	}
	if other()+minBranch+1 > width {
		l.showBuilds = false
	}
	if other()+minBranch+1 > width {
		l.showSync = false
	}
	if other()+minBranch+1 > width {
		l.showTree = false
	}
	// Reclaim descriptive space before clipping an identifier and its state markers.
	compactBudget := width - (other() - l.pr) - minBranch - 1
	l.name -= min(max(0, compactPRWidth-compactBudget), max(0, l.name-len("REPO")))
	l.pr = min(l.pr, max(1, width-(other()-l.pr)-minBranch-1))
	l.branch = max(minBranch, min(l.branch, width-other()-1))
	// Reserve two number-field spare cells, one gap and the five-cell C/M/R suffix.
	// Per-row state labels compact before they can displace another row's signals.
	l.number = min(l.number, max(0, l.pr-2-1-5))
	return l
}

// LOCAL sits between BRANCH and PR: what runs here comes before what is
// under review.
func (l statusRepoLayout) runtimeStart() int { return l.name + 1 + l.branch + 1 }
func (l statusRepoLayout) prStart() int {
	if l.showRuntime {
		return l.runtimeStart() + l.runtime + 1
	}
	return l.runtimeStart()
}

// buildsWidth is the width of the build columns, each with its leading space.
func (l statusRepoLayout) buildsWidth() int {
	n := 0
	if l.hasTip {
		n += 1 + l.tip
	}
	if l.hasProd {
		n += 1 + l.prod
	}
	return n
}

func (l statusRepoLayout) prodStart() int {
	if l.hasTip {
		return l.tipStart() + l.tip + 1
	}
	return l.tipStart()
}
func (l statusRepoLayout) tipStart() int {
	start := l.prStart() + l.pr + 1 + l.tree
	if l.showSync {
		start += 1 + l.ahead + 1 + l.behind
	}
	return start + 1
}

func statusTUIRepoURL(row wtc.StatusRepo) string {
	if row.Forge != "github.com" && row.Forge != "bitbucket.org" {
		return ""
	}
	parts := strings.Split(row.Slug, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return "https://" + row.Forge + "/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
}

func statusTUIBranchURL(row wtc.StatusRepo) string {
	base := statusTUIRepoURL(row)
	if base == "" || row.Branch == "" {
		return ""
	}
	if row.Forge == "bitbucket.org" {
		return base + "/src/" + url.PathEscape(row.Branch) + "/"
	}
	return base + "/tree/" + strings.ReplaceAll(url.PathEscape(row.Branch), "%2F", "/")
}

func statusTUIBuildCell(prefix string, build *wtc.StatusBuild) string {
	if build == nil {
		return "·"
	}
	glyph := "·"
	if build.Checks != nil {
		switch *build.Checks {
		case "SUCCESS":
			glyph = "✓"
		case "FAILURE", "ERROR":
			glyph = "✗"
		case "PENDING", "EXPECTED":
			glyph = "●"
		}
	}
	if build.Error == "" && build.HeadInBuild != nil && !*build.HeadInBuild {
		// The build is real but does not contain this row's HEAD.
		glyph = "○"
	}
	cell := prefix + glyph
	if build.Build != nil && *build.Build != "" {
		cell += "#" + *build.Build
	}
	return cell
}

func statusTUIRepoPRCell(pr *wtc.StatusPRFacts, width int, styled bool, numberWidth ...int) string {
	if pr == nil || pr.Number == "" {
		return statusTUIFit("·", width)
	}
	n := ansi.StringWidth(statusTUIPRNumber(pr.Number))
	if len(numberWidth) > 0 {
		n = numberWidth[0]
	}
	cell := statusTUIPRNumberCell(pr.Number, pr.URL, n+2, styled, statusToneLink) + " " + statusTUIPRSuffix(pr, styled)
	if ansi.StringWidth(cell) > width || ansi.StringWidth(statusTUIPRNumber(pr.Number)) > n+2 {
		// Draft state can use spare number-field cells while signals stay aligned.
		// The merge signal itself preserves conflict state without the long label.
		draft := ""
		if pr.Draft {
			draft = " ◇"
			if styled {
				draft = statusTUIStyle(draft, statusToneDim)
			}
		}
		signals := statusTUIPRSignals(&pr.Checks, &pr.Merge, &pr.Review, styled)
		suffix := strings.Join(signals[:], " ")
		numberCells, draftWidth := n+2, ansi.StringWidth(draft)
		if ansi.StringWidth(statusTUIPRNumber(pr.Number))+draftWidth <= numberCells && numberCells+1+ansi.StringWidth(suffix) <= width {
			compact := statusTUIPRNumberCell(pr.Number, pr.URL, numberCells-draftWidth, styled, statusToneLink) + draft + " " + suffix
			if !styled {
				return statusTUIFit(compact, width)
			}
			return statusTUIFitANSI(compact, width)
		}
		// Preserve PR state before optional signals if the cell or full identifier exceeds its column.
		marker, label := "", statusTUIPRNumber(pr.Number)
		if statusTUIPRConflict(pr) {
			marker = "✗ "
			if styled {
				marker = statusTUIStyle(marker, statusToneFailure)
			}
		}
		if pr.Draft {
			draft := "◇ "
			if styled {
				draft = statusTUIStyle(draft, statusToneDim)
			}
			marker += draft
		}
		if styled {
			label = statusTUILinkCell(label, pr.URL, statusToneLabel)
		}
		if !styled {
			return statusTUIFit(marker+label, width)
		}
		return statusTUIFitANSI(marker+label, width)
	}
	if !styled {
		return statusTUIFit(cell, width)
	}
	return statusTUIFitANSI(cell, width)
}

func statusTUIRepoLines(snapshot wtc.StatusSnapshot, width int, styled bool, position ...func(int, wtc.StatusRepo)) []string {
	l := statusTUIRepoLayout(snapshot, width)
	header := statusTUIFit("REPO", l.name) + " " + statusTUIFit("BRANCH", l.branch)
	if l.showRuntime {
		header += " " + statusTUIFit("LOCAL", l.runtime)
	}
	header += " " + statusTUIFit(statusTUIFit("PR", l.number+2)+" C M R", l.pr)
	if l.showTree {
		header += " " + statusTUIFit("±", l.tree)
	}
	if l.showSync {
		header += " " + statusTUIFit("↑", l.ahead) + " " + statusTUIFit("↓", l.behind)
	}
	if l.showBuilds && l.hasTip {
		header += " " + statusTUIFit("TEST", l.tip)
	}
	if l.showBuilds && l.hasProd {
		header += " " + statusTUIFit("PROD", l.prod)
	}
	if styled {
		header = statusTUIStyle(header, statusToneHeading)
	}
	lines := []string{header}
	for _, row := range snapshot.Repos {
		name := row.Dir
		if snapshot.ShowCollectionColumn {
			name = row.Collection + "/" + row.Dir
		}
		tree := "·"
		if row.Conflict {
			tree = "✗"
		} else if row.Operation != "" {
			tree = "⚠"
		} else if row.Tree != "" && row.Tree != "clean" {
			tree = row.Tree
		}
		nameCell := statusTUIFit(statusTUISafe(name), l.name)
		branchCell := statusTUIFit(statusTUISafe(row.BranchDisplay), l.branch)
		prCell := statusTUIRepoPRCell(row.PR, l.pr, styled, l.number)
		if styled {
			if target := statusTUIRepoURL(row); target != "" {
				nameCell = statusTUILinkCell(nameCell, target, statusToneLabel)
			} else {
				nameCell = statusTUIStyle(nameCell, statusToneLabel)
			}
			if target := statusTUIBranchURL(row); target != "" {
				tone := statusToneSecondaryLink
				if row.BranchKind == "detached" {
					tone = statusToneDim
				}
				branchCell = statusTUILinkCell(branchCell, target, tone)
			} else if row.BranchKind == "detached" {
				branchCell = statusTUIStyle(branchCell, statusToneDim)
			}
		}
		treeCell := statusTUIFit(tree, l.tree)
		if styled {
			if tree == "·" {
				treeCell = statusTUIStyle(treeCell, statusToneDim)
			} else if row.Conflict {
				treeCell = statusTUIStyle(treeCell, statusToneFailure)
			} else {
				treeCell = statusTUIStyle(treeCell, statusToneWarning)
			}
		}
		line := nameCell + " " + branchCell
		if l.showRuntime {
			line += " " + statusRuntimeCell(statusRuntimeItems(snapshot, row), l.runtime, styled)
		}
		line += " " + prCell
		if l.showTree {
			line += " " + treeCell
		}
		if l.showSync {
			ahead, behind := "·", statusTUIBehind(row)
			if row.Ahead > 0 {
				ahead = fmt.Sprint(row.Ahead)
			}
			aheadCell, behindCell := statusTUIFit(ahead, l.ahead), statusTUIFit(behind, l.behind)
			if styled {
				if row.Ahead > 0 {
					aheadCell = statusTUIStyle(aheadCell, statusToneSuccess)
				} else {
					aheadCell = statusTUIStyle(aheadCell, statusToneDim)
				}
				if row.Conflict || statusTUIPRConflict(row.PR) {
					behindCell = statusTUIStyle(behindCell, statusToneFailure)
				} else if row.Behind > 0 {
					behindCell = statusTUIStyle(behindCell, statusToneWarning)
				} else {
					behindCell = statusTUIStyle(behindCell, statusToneDim)
				}
			}
			line += " " + aheadCell + " " + behindCell
		}
		if l.showBuilds {
			tipCell := statusTUIFit(statusTUIBuildCell("T", row.Tip), l.tip)
			prodCell := statusTUIFit(statusTUIBuildCell("P", row.Prod), l.prod)
			if styled {
				tipCell = statusTUIBuildLink(tipCell, row.Tip)
				prodCell = statusTUIBuildLink(prodCell, row.Prod)
			}
			if l.hasTip {
				line += " " + tipCell
			}
			if l.hasProd {
				line += " " + prodCell
			}
		}
		if len(position) > 0 {
			position[0](len(lines), row)
		}
		lines = append(lines, line)
	}
	if len(snapshot.Repos) == 0 {
		lines = append(lines, "(no worktrees)")
	}
	return lines
}

// runtimeItemAt returns the service, tunnel or resource whose mark is at a cell.
func (m statusTUIModel) runtimeItemAt(x, y int) (wtc.RuntimeItem, bool) {
	if m.showLog || m.showNext || m.procs {
		return wtc.RuntimeItem{}, false
	}
	row, ok := m.visibleRow(x, y)
	if !ok || row.repo == nil {
		return wtc.RuntimeItem{}, false
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	layout := statusTUIRepoLayout(m.snapshot, width)
	items := statusRuntimeItems(m.snapshot, *row.repo)
	slots := statusRuntimeSlots(items)
	if index := x - layout.runtimeStart(); layout.showRuntime && index >= 0 && index < min(len(slots), layout.runtime) && slots[index].item >= 0 {
		return items[slots[index].item], true
	}
	return wtc.RuntimeItem{}, false
}

func (m statusTUIModel) buildClickTarget(x, y int) string {
	if m.showLog || m.showNext || m.procs || os.Getenv("TERM") == "dumb" {
		return ""
	}
	row, ok := m.visibleRow(x, y)
	if !ok {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	if row.repo != nil {
		r := *row.repo
		if item, ok := m.runtimeItemAt(x, y); ok {
			if statusRuntimeLive(item) {
				return statusTUIURL(item.URL)
			}
			return ""
		}
		layout := statusTUIRepoLayout(m.snapshot, width)
		if x < layout.name {
			return statusTUIURL(statusTUIRepoURL(r))
		}
		if x >= layout.name+1 && x < layout.name+1+layout.branch {
			return statusTUIURL(statusTUIBranchURL(r))
		}
		prStart := layout.prStart()
		if x >= prStart && x < prStart+layout.pr && r.PR != nil {
			return statusTUIURL(r.PR.URL)
		}
		if !layout.showBuilds {
			return ""
		}
		var build *wtc.StatusBuild
		if start := layout.tipStart(); layout.hasTip && x >= start && x < start+layout.tip {
			build = r.Tip
		} else if start := layout.prodStart(); layout.hasProd && x >= start && x < start+layout.prod {
			build = r.Prod
		}
		if build != nil && build.URL != nil {
			return statusTUIURL(*build.URL)
		}
		return ""
	}
	if m.reposOnly || m.snapshot.ShowCollectionColumn {
		return ""
	}
	if row.service != nil {
		return statusTUIURL(statusTUIRuntimeLayout(m.snapshot, width).urlAt(*row.service, x))
	}
	visible, _ := statusTUIVisiblePRs(m.snapshot, m.showArchived)
	if row.pr != nil && row.pr.URL != nil && x < statusTUIPRLayout(width, visible...).number {
		return statusTUIURL(*row.pr.URL)
	}
	return ""
}

func (m statusTUIModel) effectiveScroll() int {
	return m.viewport(m.contentLayout()).scroll
}

func statusOpenURL(target string) tea.Cmd {
	return func() tea.Msg {
		tool := "xdg-open"
		if runtime.GOOS == "darwin" {
			tool = "open"
		}
		command := exec.Command(tool, target)
		if err := command.Run(); err != nil {
			return statusOpenedMsg{err: fmt.Errorf("open status URL: %w", err)}
		}
		return statusOpenedMsg{}
	}
}

func statusTUIPRGlyph(value *string) string {
	if value == nil {
		return "·"
	}
	switch *value {
	case "SUCCESS", "approved", "MERGEABLE", "CLEAN":
		return "✓"
	case "FAILURE", "ERROR", "changes", "CONFLICTING", "DIRTY":
		return "✗"
	case "PENDING", "EXPECTED", "waiting":
		return "●"
	case "BEHIND":
		return "↓"
	case "BLOCKED":
		return "⊘"
	case "commented":
		return "✎"
	case "noreviewers":
		return "∅"
	}
	return "·"
}

// statusTUIFollowState names the tier a merged PR is waiting on and how that
// tier's build stands, with the tone to show it in.
func statusTUIFollowState(follow *wtc.StatusFollow) (string, string) {
	if follow == nil {
		return "merged", statusToneDim
	}
	if follow.Stage == "done" {
		return "✓ built", statusToneSuccess
	}
	stage := "test"
	if follow.Stage == "prod" {
		stage = "prod"
	}
	switch follow.Checks {
	case "WAITING":
		return "→ " + stage, statusToneWarning
	case "PENDING", "EXPECTED":
		return "● " + stage, statusToneWarning
	case "FAILURE", "ERROR":
		return "✗ " + stage, statusToneFailure
	case "UNKNOWN":
		return "? " + stage, statusToneDim
	}
	return "merged", statusToneDim
}

func statusTUIMergedPR(row wtc.StatusPRRow) bool {
	return row.Merge != nil && *row.Merge == "MERGED" || row.MergedOn != nil && *row.MergedOn != ""
}

func statusTUIVisiblePRs(snapshot wtc.StatusSnapshot, showArchived bool) ([]wtc.StatusPRRow, int) {
	active := make([]wtc.StatusPRRow, 0, len(snapshot.PRs))
	merged := make([]wtc.StatusPRRow, 0)
	archived := make([]wtc.StatusPRRow, 0)
	for _, row := range wtc.StatusPRsByPriority(snapshot.PRs) {
		switch {
		case row.Archived:
			archived = append(archived, row)
		case statusTUIMergedPR(row) && !row.OnBranch:
			merged = append(merged, row)
		default:
			active = append(active, row)
		}
	}
	active = append(active, merged...)
	if showArchived {
		active = append(active, archived...)
	}
	return active, len(archived)
}

type statusPRLayout struct {
	number, repo, state, title int
	signalWidths               [3]int
	signals                    bool
}

func statusTUIPRLayout(width int, rows ...wtc.StatusPRRow) statusPRLayout {
	l := statusPRLayout{number: 5, repo: 16, state: 10, signalWidths: [3]int{1, 1, 1}, signals: true}
	identifier := 1
	for _, row := range rows {
		identifier = max(identifier, ansi.StringWidth(statusTUIPRNumber(row.Number)))
		l.number = max(l.number, identifier+2)
	}
	switch {
	case width >= 52:
	case width >= 40:
		l.repo, l.state = 12, 8
	case width >= 34:
		l.repo, l.state, l.signals = 10, 8, false
	default:
		l.repo, l.state, l.signals = 8, 7, false
	}
	if width >= 100 {
		l.repo = 20
	}
	if width >= 80 {
		l.signalWidths = [3]int{6, 5, 7}
	}
	signalWidth := 0
	if l.signals {
		for _, n := range l.signalWidths {
			signalWidth += n + 1
		}
	}
	// Prefer the identifier to optional signals and wider descriptive columns.
	numberBudget := func() int { return width - l.repo - l.state - signalWidth - 4 }
	if identifier > numberBudget() && l.signals {
		l.signals, signalWidth = false, 0
	}
	if identifier > numberBudget() {
		l.repo -= min(identifier-numberBudget(), max(0, l.repo-len("REPO")))
	}
	if identifier > numberBudget() {
		l.state -= min(identifier-numberBudget(), max(0, l.state-len("STATE")))
	}
	l.number = min(l.number, max(3, numberBudget()))
	prefix := l.number + 1 + l.repo + 1 + l.state + 1 + signalWidth
	l.title = max(1, width-prefix)
	return l
}

func statusTUIPRLines(snapshot wtc.StatusSnapshot, width int, showArchived, styled bool, position ...func(int, wtc.StatusPRRow)) []string {
	if snapshot.ShowCollectionColumn {
		return nil
	}
	heading := "PRs"
	if styled {
		heading = statusTUIStyle(heading, statusToneHeading)
	}
	visible, archived := statusTUIVisiblePRs(snapshot, showArchived)
	l := statusTUIPRLayout(width, visible...)
	header := statusTUIFit("PR", l.number) + " " + statusTUIFit("REPO", l.repo) + " " + statusTUIFit("STATE", l.state) + " "
	if l.signals {
		labels := [3]string{"C", "M", "R"}
		if l.signalWidths[0] > 1 {
			labels = [3]string{"CHECKS", "MERGE", "REVIEWS"}
		}
		for i, label := range labels {
			header += statusTUIFit(label, l.signalWidths[i]) + " "
		}
	}
	header += statusTUIFit("TITLE", l.title)
	if styled {
		header = statusTUIStyle(header, statusToneDim)
	}
	lines := []string{"", heading, header}
	for _, row := range visible {
		label := statusTUISafe(row.DisplayTitle)
		if label == "" {
			label = statusTUISafe(row.Title)
		}
		if label == "" {
			label = "PR " + statusTUIPRNumber(row.Number)
		}
		state := "open"
		merged := statusTUIMergedPR(row)
		if row.Merge != nil && (*row.Merge == "CONFLICTING" || *row.Merge == "DIRTY") {
			state = "✗ conflict"
		} else if row.OnBranch {
			state = "⚠ catch-up"
			if row.Landing != nil {
				switch row.Landing.State {
				case "follow-up":
					state = "⚠ extra"
				case "unknown":
					state = "unknown"
				}
			}
		} else if row.Draft {
			state = "◇ draft"
		} else if merged {
			state, _ = statusTUIFollowState(row.Follow)
		} else if row.State == "UNKNOWN" {
			state = "unknown"
		}
		target := ""
		if row.URL != nil {
			target = *row.URL
		}
		linkTone := statusToneLink
		if merged && !row.OnBranch {
			linkTone = statusToneDim
		}
		number := statusTUIPRNumberCell(row.Number, target, l.number, styled, linkTone)
		repo := statusTUIFit(statusTUISafe(row.Repo), l.repo)
		stateCell := statusTUIFit(state, l.state)
		signals := statusTUIPRSignals(row.Checks, row.Merge, row.Review, styled)
		if merged && !row.OnBranch {
			signals = statusTUIPRSignals(nil, nil, nil, styled)
		}
		title := statusTUIFit(label, l.title)
		if styled {
			rowTone := statusToneLabel
			if merged && !row.OnBranch {
				rowTone = statusToneDim
			}
			repo = statusTUIStyle(repo, rowTone)
			title = statusTUIStyle(title, rowTone)
			if row.Merge != nil && (*row.Merge == "CONFLICTING" || *row.Merge == "DIRTY") {
				stateCell = statusTUIStyle(stateCell, statusToneFailure)
			} else if row.OnBranch {
				stateCell = statusTUIStyle(stateCell, statusToneWarning)
			} else if merged {
				_, tone := statusTUIFollowState(row.Follow)
				stateCell = statusTUIStyle(stateCell, tone)
			} else {
				stateCell = statusTUIStyle(stateCell, statusToneDim)
			}
		}
		line := number + " " + repo + " " + stateCell + " "
		if l.signals {
			for i, signal := range signals {
				line += statusTUIFitANSI(signal, l.signalWidths[i]) + " "
			}
		}
		if len(position) > 0 {
			position[0](len(lines), row)
		}
		lines = append(lines, line+title)
	}
	if len(snapshot.PRs) == 0 {
		lines = append(lines, "(none enlisted)")
	}
	if archived > 0 && !showArchived {
		hint := fmt.Sprintf("%d archived PR(s) hidden; press a", archived)
		if width < 40 {
			hint = fmt.Sprintf("archived (%d) · a", archived)
		}
		if styled {
			if width >= 40 {
				hint = fmt.Sprintf("▸ archived (%d) · a to show", archived)
			}
			hint = statusTUIStyle(statusTUIFit(hint, width), statusToneDim)
		} else {
			hint = statusTUIFit(hint, width)
		}
		lines = append(lines, hint)
	}
	for _, orphan := range snapshot.Orphans {
		warning := fmt.Sprintf("⚠ %s on %s: PR %s; catch-up", orphan.Repo, orphan.Branch, orphan.State)
		if styled {
			warning = statusTUIStyle(statusTUIFit(warning, width), statusToneWarning)
		} else {
			warning = statusTUIFit(warning, width)
		}
		lines = append(lines, warning)
	}
	return lines
}

// statusTUIRetiredLines lists the folders a retire left behind. Only workspace
// sweeps carry them, so they render outside the collection's PR section.
func statusTUIRetiredLines(snapshot wtc.StatusSnapshot, width int, styled bool) []string {
	var lines []string
	for _, retired := range snapshot.Retired {
		// Names come from the filesystem or the marker; keep controls out.
		note := statusTUISafe(fmt.Sprintf("retired %s: leftovers %s", retired.Folder, strings.Join(retired.BlockingPaths, ", ")))
		if styled {
			note = statusTUIStyle(statusTUIFit(note, width), statusToneDim)
		} else {
			note = statusTUIFit(note, width)
		}
		lines = append(lines, note)
	}
	if len(lines) != 0 {
		lines = append([]string{""}, lines...)
	}
	return lines
}

func statusTUIGlyphStyle(glyph string) string {
	switch glyph {
	case "✓":
		return statusTUIStyle(glyph, statusToneSuccess)
	case "✗", "⊘":
		return statusTUIStyle(glyph, statusToneFailure)
	case "●", "↓", "⚠":
		return statusTUIStyle(glyph, statusToneWarning)
	default:
		return statusTUIStyle(glyph, statusToneDim)
	}
}

// headerLine is the primary heading; refresh hit testing excludes the optional
// version suffix so clicking release notes never toggles the log.
func (m statusTUIModel) headerLine() string {
	name := m.snapshot.Collection
	if m.procs {
		name = "processes"
	}
	if name == "" {
		name = "(all)"
	}
	age := "loading"
	if !m.lastRefresh.IsZero() {
		age = fmt.Sprintf("%ds old", max(0, int(time.Since(m.lastRefresh).Seconds())))
	}
	if m.refreshing {
		age += " · refreshing"
	}
	return fmt.Sprintf("%s status · %s · %s", branding.Product, name, age)
}

func statusTUIProgressLine(step string, elapsed time.Duration, width int) string {
	full := fmt.Sprintf("↻ %s · %s  (l: log)", elapsed, statusTUISafe(step))
	if runewidth.StringWidth(full) <= width {
		return full
	}
	if parts := statusTUIProgressCounter.FindStringSubmatch(step); len(parts) == 2 {
		count := strings.TrimPrefix(step, parts[1]+" ")
		return fmt.Sprintf("↻ %s · %s", count, statusTUISafe(parts[1]))
	}
	return "↻ " + statusTUISafe(step)
}

func (m statusTUIModel) contentLines() []string {
	return m.contentLayout().lines
}

func (m statusTUIModel) contentLayout() statusTUIContent {
	var rows, nextRows []statusTUIRow
	noticeStart := -1
	width := m.width
	if width <= 0 {
		width = 80
	}
	primary := m.headerLine()
	header := statusTUIStyle(primary, statusToneHeading)
	if remaining := width - ansi.StringWidth(primary); remaining > 0 {
		suffix := " · " + wtc.RunningCLIVersion()
		if target := wtc.StatusVersionNotesURL(wtc.CLIVersion); target != "" {
			suffix = statusTUILinkTone(suffix, target, statusToneDim)
		} else {
			suffix = statusTUIStyle(suffix, statusToneDim)
		}
		header += statusTUIFitANSI(suffix, remaining)
	}
	lines := []string{header, ""}
	if m.refreshing && m.progressStep != "" {
		elapsed := time.Since(m.startedAt).Truncate(time.Second)
		if m.startedAt.IsZero() {
			elapsed = 0
		}
		lines[1] = statusTUIStyle(statusTUIProgressLine(m.progressStep, elapsed, width), statusToneWarning)
	}
	if m.showHelp {
		keys := "n next actions   r refresh   l log   a archived PRs   ? help   q quit"
		if m.procs {
			keys = "r refresh   l log   ? help   q quit"
		}
		help := []string{keys,
			"↗ link: modifier-click; PR/TEST/PROD: click; ↑/↓ or wheel scroll; PgUp/PgDn page; Home/End",
			"PR signals, left to right: C checks · M mergeability · R reviews. ? toggles this help.",
			"C checks: ✓ passed; ✗ failed/error; ● pending/expected; · absent/unknown.",
			"M mergeability: ✓ clean/mergeable; ✗ conflict; ↓ behind base; ⊘ blocked; · absent/unknown/other.",
			"R reviews: ✓ approved; ✗ changes requested; ● waiting; ✎ commented (not approval); ∅ no reviewers; · absent/unknown.",
			"◇ draft; ↗ opens a link. Merged PRs off their branch use · signals; retained branches keep recorded observations. See STATE for follow-up.",
		}
		for _, text := range help {
			lines = append(lines, strings.Split(ansi.Wrap(text, width, ""), "\n")...)
		}
		lines = append(lines, "")
	}
	if m.showLog {
		lines = append(lines, statusTUIStyle("Refresh log", statusToneHeading), "")
		for _, entry := range m.progressLog {
			lines = append(lines, strings.Split(ansi.Wrap(statusTUISafe(entry), width, ""), "\n")...)
		}
		return statusTUIContent{lines: lines, rows: rows, nextRows: nextRows, noticeStart: noticeStart}
	}
	if m.showNext {
		lines = append(lines, "↑/↓ choose · Enter run or ask agent · Space select · → alternative · s suggested · c cancel · x allow retry · Esc back")
		selected := min(m.selectedNext, max(0, len(statusNextRepos(m.snapshot))-1))
		lines = append(lines, statusTUIFit("Local steps run here · Agent steps → owning collection's idle agent pane", width))
		base := len(lines)
		nextLines, positions := statusNextRows(m.snapshot, width, true, selected, m.nextSelected)
		for _, row := range positions {
			row.start, row.end = row.start+base, row.end+base
			nextRows = append(nextRows, row)
		}
		rows = append(rows, nextRows...)
		lines = append(lines, nextLines...)
		if len(statusNextRepos(m.snapshot)) == 0 {
			lines = append(lines, "", "No next actions.")
		}
		if m.nextNotice != "" {
			lines = append(lines, "")
			noticeStart = len(lines)
			for _, paragraph := range strings.Split(m.nextNotice, "\n") {
				lines = append(lines, strings.Split(ansi.Wrap(statusTUISafe(paragraph), width, ""), "\n")...)
			}
			rows = append(rows, statusTUIRow{key: "next-notice", start: noticeStart, end: len(lines)})
		}
		return statusTUIContent{lines: lines, rows: rows, nextRows: nextRows, noticeStart: noticeStart}
	}
	if m.procs {
		lines = append(lines, strings.Split(strings.TrimSuffix(wtc.StatusProcessesText(m.processes), "\n"), "\n")...)
	} else {
		base := len(lines)
		lines = append(lines, statusTUIRepoLines(m.snapshot, width, true, func(line int, row wtc.StatusRepo) {
			rows = append(rows, statusTUIRow{key: statusTUIRepoKey(row), start: base + line, end: base + line + 1, repo: &row})
		})...)
		base = len(lines)
		lines = append(lines, statusTUIRuntimeLines(m.snapshot, width, true, func(line int, group statusRuntimeGroup) {
			key := "service:" + group.members[0].Collection + "\x00" + group.members[0].Group
			rows = append(rows, statusTUIRow{key: key, start: base + line, end: base + line + 1, service: &group})
		})...)
		if !m.reposOnly {
			base = len(lines)
			lines = append(lines, statusTUIPRLines(m.snapshot, width, m.showArchived, true, func(line int, row wtc.StatusPRRow) {
				rows = append(rows, statusTUIRow{key: "pr:" + row.Repo + "#" + row.Number, start: base + line, end: base + line + 1, pr: &row})
			})...)
		}
		lines = append(lines, statusTUIRetiredLines(m.snapshot, width, true)...)
		if repos := statusNextRepos(m.snapshot); len(repos) > 0 {
			var hints []string
			for _, r := range repos {
				name := r.Dir
				if m.snapshot.ShowCollectionColumn {
					name = r.Collection + "/" + name
				}
				hints = append(hints, statusTUISafe(name+": "+statusNextLabel(r.Next)))
			}
			lines = append(lines, "", statusTUIFit("[n] Next · "+strings.Join(hints, " · "), width))
		}
		if m.snapshot.StaleCount > 0 {
			lines = append(lines, "", fmt.Sprintf("%d worktree(s) behind the development tip", m.snapshot.StaleCount))
		}
	}
	if !m.procs {
		lines = append(lines, statusRetirementLines(m.snapshot, width)...)
		lines = append(lines, statusSyncNotes(m.snapshot, width)...)
	}
	if m.errorText != "" {
		lines = append(lines, "")
		for _, line := range strings.Split(ansi.Wrap("warning: "+statusTUISafe(m.errorText), width, ""), "\n") {
			lines = append(lines, statusTUIStyle(line, statusToneWarning))
		}
	}
	return statusTUIContent{lines: lines, rows: rows, nextRows: nextRows, noticeStart: noticeStart}
}

func (m statusTUIModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}
	content := m.contentLayout()
	v := m.viewport(content)
	footer := "n next actions · r refresh · l log · a archived · ↗ link · ? help · q quit"
	if m.procs {
		footer = "r refresh · l log · ? help · q quit"
	}
	if m.showNext && !m.showLog {
		footer = "↑/↓ choose · PgUp/PgDn scroll · Home/End · Enter run · Esc back"
	} else if v.limit > 0 {
		footer = "↑/↓ wheel · PgUp/PgDn · Home/End · n next · r refresh · ? help · q quit"
		if m.procs || m.showLog {
			footer = "↑/↓ wheel · PgUp/PgDn · Home/End · l log · r refresh · ? help · q quit"
		}
	}
	if !m.nextRefresh.IsZero() && !m.refreshing {
		footer += fmt.Sprintf(" · next %ds", max(0, int(time.Until(m.nextRefresh).Seconds())))
	}
	if m.hover != "" {
		footer = m.hover
	}
	visible := append([]string(nil), content.lines[:v.header]...)
	start := v.header + v.scroll
	for i := 0; i < v.body; i++ {
		line := ""
		if start+i < len(content.lines) {
			line = content.lines[start+i]
		}
		visible = append(visible, line)
	}
	if height > 1 {
		visible = append(visible, statusTUIStyle(footer, statusToneDim))
	}
	visibleLink := false
	for i, line := range visible {
		fitted := statusTUIFitANSI(line, width)
		visible[i] = fitted
		if statusTUIHasVisibleLink(fitted) {
			visibleLink = true
		}
	}
	view := tea.NewView(strings.Join(visible, "\n"))
	view.AltScreen = true
	view.ReportFocus = os.Getenv("TERM") != "dumb"
	if os.Getenv("TERM") != "dumb" && (m.refreshing || v.limit > 0 || visibleLink && !m.noClick) {
		view.MouseMode = tea.MouseModeCellMotion
	}
	// Hover text for the LOCAL marks needs motion without a pressed button.
	if os.Getenv("TERM") != "dumb" && !m.procs && !m.showLog && !m.showNext && statusTUIRepoLayout(m.snapshot, width).showRuntime {
		view.MouseMode = tea.MouseModeAllMotion
	}
	return view
}

func runStatusTUI(c *wtc.Context, all, procs, reposOnly, noFetch, noClick bool, interval, background, fetchAge time.Duration) error {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("status TUI requires a terminal")
	}
	model := statusTUIModel{context: c, all: all, procs: procs, reposOnly: reposOnly, noFetch: noFetch, noClick: noClick, fetchAge: fetchAge, interval: interval,
		background: background, focused: true, refreshing: true}
	model.startRefresh()
	if !all && !procs {
		model.snapshot.Collection = filepath.Base(c.Collection)
	}
	if !all && !procs {
		if cached, err := c.ReadStatusSnapshot(); err == nil {
			model.snapshot = cached
			if age, err := c.CachedStatusAge(); err == nil {
				model.lastRefresh = time.Now().Add(-time.Duration(age) * time.Second)
			}
		}
		// Read runtime records plus current declarations rather than the shared
		// cache another wtc may have written; live states follow from Init.
		model.snapshot.Runtime = c.RuntimeEnlisted()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup, err := c.RegisterStatusWatcher(cancel)
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
	if ctx.Err() != nil {
		return nil // The watcher lease was cancelled.
	}
	return err
}
