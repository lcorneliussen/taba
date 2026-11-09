package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"taba.sh/taba/internal/next"
	"taba.sh/taba/internal/wtc"
)

func TestStatusNextActions(t *testing.T) {
	r := wtc.StatusRepo{Collection: "task", Dir: "api", Worktree: "/synthetic/task/api", Next: &next.Step{Step: "return-to-tip", By: next.ByWTC, Key: "k", Reason: "The pull request merged", Command: []string{"wtc", "catch-up", "task", "--repos", "api"}}}
	snapshot := wtc.StatusSnapshot{Collection: "task", Repos: []wtc.StatusRepo{r}}
	if text := statusTable(snapshot, true, 100); !strings.Contains(text, "Return to tip") || !strings.Contains(text, r.Next.Reason) {
		t.Fatal(text)
	}
	if _, err := statusNextCurrent(r, snapshot); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.Next = nil
	if _, err := statusNextCurrent(r, wtc.StatusSnapshot{Repos: []wtc.StatusRepo{changed}}); err == nil {
		t.Fatal("a stale action ran after the checkout became dirty")
	}
	argv, ok := statusNextCommand(r)
	if !ok || strings.Join(argv, " ") != "catch-up task --repos api --clean-only --idle-only" {
		t.Fatal(argv, ok)
	}
	registered := r
	registered.Repo = "service"
	s := *r.Next
	s.Command = []string{"wtc", "catch-up", "task", "--repos", "service"}
	registered.Next = &s
	if _, ok := statusNextCommand(registered); !ok {
		t.Fatal("registry names can differ from sibling directories")
	}
	for _, approval := range []string{next.ApproveAgent, next.ApprovePerson} {
		s := *r.Next
		s.Approve = approval
		changed.Next = &s
		if _, ok := statusNextCommand(changed); ok {
			t.Fatal("executed without required approval", approval)
		}
		if _, err := statusNextCurrent(r, wtc.StatusSnapshot{Repos: []wtc.StatusRepo{changed}}); err == nil {
			t.Fatal("an approval change was not rechecked")
		}
	}
	m := statusTUIModel{snapshot: snapshot, width: 100, height: 30}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	m = updated.(statusTUIModel)
	if !m.showNext || !strings.Contains(m.View().Content, "Enter run or ask agent") {
		t.Fatal(m.View().Content)
	}
	m.nextRunning = true
	if _, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd != nil {
		t.Fatal("a second execution started while the first was running")
	}
}

func TestStatusNextExecution(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "task")
	if err := os.MkdirAll(filepath.Join(collection, "harness"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, "harness", ".harness-repos.yml"), []byte("repos:\n  - name: api\n    remote: https://github.com/example/api.git\n    default_ref: origin/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := wtc.OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	r := wtc.StatusRepo{Collection: "task", Dir: "api", Worktree: filepath.Join(collection, "api"), Next: &next.Step{Step: "catch-up", By: next.ByWTC, Key: "k", Command: []string{"wtc", "catch-up", "task", "--repos", "api"}}}
	for _, state := range []string{"", "working", "blocked", "unknown"} {
		t.Run("catch-up/"+state, func(t *testing.T) {
			executed := false
			run := statusNextRunner{
				readStatus: func(*wtc.Context) (wtc.StatusSnapshot, error) {
					return wtc.StatusSnapshot{Repos: []wtc.StatusRepo{r}}, nil
				},
				busy: func(*wtc.Context) (string, error) {
					if state == "unknown" {
						return "", errors.New("cannot read agent state")
					}
					return state, nil
				},
				command: func(owner *wtc.Context, argv []string) (string, error) {
					executed = true
					if owner.Collection != collection || strings.Join(argv, " ") != "catch-up task --repos api --clean-only --idle-only" {
						t.Fatal(owner.Collection, argv)
					}
					return "caught up", nil
				},
			}
			result := statusNextRunWith(c, r, run)().(statusNextDoneMsg)
			if executed != (state == "") || (result.err == nil) != (state == "") {
				t.Fatal(executed, result)
			}
			// Changing facts, even with the old key still selected, must
			// reject the step before any execution or owner lookup.
			run.readStatus = func(*wtc.Context) (wtc.StatusSnapshot, error) { return wtc.StatusSnapshot{}, nil }
			executed = false
			if result := statusNextRunWith(c, r, run)().(statusNextDoneMsg); result.err == nil || executed {
				t.Fatal(result, executed)
			}
		})
	}
	for _, state := range []string{"idle", "working", "blocked", "unavailable"} {
		t.Run("agent/"+state, func(t *testing.T) {
			agent := r
			agent.Next = &next.Step{Step: "fix-checks", By: next.ByAgent, Key: "agent", Skill: "wtc-follow", Reason: "checks failed"}
			prompted := false
			run := statusNextRunner{
				readStatus: func(*wtc.Context) (wtc.StatusSnapshot, error) {
					return wtc.StatusSnapshot{Repos: []wtc.StatusRepo{agent}}, nil
				},
				herdr: func(_ string, argv ...string) (openHerdrResponse, error) {
					var response openHerdrResponse
					if state == "unavailable" {
						return response, errors.New("no herdr")
					}
					switch argv[0] {
					case "workspace":
						response.Result.Workspaces = []openWorkspaceInfo{{Label: "another", ID: "w0"}, {Label: "task", ID: "w1"}}
					case "pane":
						if argv[3] != "w1" {
							t.Fatal(argv)
						}
						response.Result.Panes = []openPaneInfo{{Label: "agent", ID: "p1", Agent: "codex", AgentStatus: state}}
					case "agent":
						prompted = true
						if argv[2] != "p1" || !strings.HasPrefix(argv[3], "/skill wtc-follow api:") {
							t.Fatal(argv)
						}
					default:
						t.Fatal(argv)
					}
					return response, nil
				},
			}
			result := statusNextRunWith(c, agent, run)().(statusNextDoneMsg)
			if result.err != nil || prompted != (state == "idle") || (!prompted && !strings.Contains(result.text, "/skill wtc-follow api:")) {
				t.Fatal(result, prompted)
			}
		})
	}
	pr := r
	pr.Next = &next.Step{Step: "merge-pr", By: next.ByPerson, Key: "pr"}
	pr.PR = &wtc.StatusPRFacts{URL: "https://example.invalid/pr/1"}
	opened := ""
	run := statusNextRunner{
		readStatus: func(*wtc.Context) (wtc.StatusSnapshot, error) {
			return wtc.StatusSnapshot{Repos: []wtc.StatusRepo{pr}}, nil
		},
		openURL: func(url string) error { opened = url; return nil },
	}
	if result := statusNextRunWith(c, pr, run)().(statusNextDoneMsg); result.err != nil || opened != pr.PR.URL {
		t.Fatal(result, opened)
	}
	pr.PR.URL, opened = "file:///synthetic/private", ""
	if result := statusNextRunWith(c, pr, run)().(statusNextDoneMsg); result.err == nil || opened != "" {
		t.Fatal(result, opened)
	}
	if result := statusNextRun(nil, r)().(statusNextDoneMsg); result.err == nil {
		t.Fatal("nil context accepted")
	}

	m := statusTUIModel{snapshot: wtc.StatusSnapshot{Repos: []wtc.StatusRepo{r, r}}, selectedNext: 1, nextRunning: true}
	updated, refresh := m.Update(statusNextDoneMsg{text: "caught up"})
	m = updated.(statusTUIModel)
	if m.nextRunning || !m.refreshing || refresh == nil || m.nextNotice != "caught up" {
		t.Fatal(m, refresh)
	}
	updated, _ = m.Update(statusLoadedMsg{snapshot: wtc.StatusSnapshot{Repos: []wtc.StatusRepo{r}}})
	if updated.(statusTUIModel).selectedNext != 0 {
		t.Fatal("selection did not follow shrinking action list")
	}
}

func TestStatusNextHandoffDestination(t *testing.T) {
	c, rows := batchFixture(t)
	author := openPaneInfo{ID: "author", Label: "agent", Agent: "codex", AgentStatus: "idle"}
	reviewer := openPaneInfo{ID: "reviewer", Label: "review", Agent: "claude", AgentStatus: "idle", Focused: true}
	for _, tc := range []struct {
		name  string
		panes []openPaneInfo
		sent  bool
		want  string
	}{
		{"focused reviewer", []openPaneInfo{reviewer, author}, true, "Sent to task / codex (author); completion pending"},
		{"reviewer only", []openPaneInfo{reviewer}, false, "Cannot identify one author agent pane"},
		{"ambiguous authors", []openPaneInfo{author, {ID: "second", Label: "agent", Agent: "claude", AgentStatus: "idle"}}, false, "Cannot identify one author agent pane"},
		{"busy author", []openPaneInfo{{ID: "author", Label: "agent", Agent: "codex", AgentStatus: "working"}, reviewer}, false, "Author agent task / codex (author) is working"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := false
			run := statusNextRunner{readStatus: func(*wtc.Context) (wtc.StatusSnapshot, error) { return wtc.StatusSnapshot{Repos: rows}, nil },
				herdr: func(_ string, args ...string) (openHerdrResponse, error) {
					var out openHerdrResponse
					switch args[0] {
					case "workspace":
						out.Result.Workspaces = []openWorkspaceInfo{{ID: "w", Label: "task"}}
					case "pane":
						out.Result.Panes = tc.panes
					case "agent":
						if args[2] != "author" {
							t.Fatal("wrong destination", args)
						}
						sent = true
					default:
						t.Fatal(args)
					}
					return out, nil
				}}
			result := statusNextBatch(context.Background(), c, rows[:2], run)().(statusNextDoneMsg)
			if result.err != nil || sent != tc.sent || !strings.Contains(result.text, tc.want) {
				t.Fatal(sent, result)
			}
			if !tc.sent && (len(result.pending) != 0 || strings.Contains(result.text, "handoff pending") || !strings.Contains(result.text, "Not sent")) {
				t.Fatal("unsent work reported as pending", result)
			}
		})
	}
}

func TestStatusNextCompactHintAndScrolling(t *testing.T) {
	var rows []wtc.StatusRepo
	for i := 0; i < 8; i++ {
		rows = append(rows, wtc.StatusRepo{Collection: "task", Dir: fmt.Sprintf("repo%d", i), Next: &next.Step{Step: "commit", By: next.ByAgent, Key: fmt.Sprint(i), Reason: fmt.Sprintf("intended changes %d", i)}})
	}
	m := statusTUIModel{snapshot: wtc.StatusSnapshot{Collection: "task", Repos: rows}, width: 100, height: 12}
	idle := ansi.Strip(strings.Join(m.contentLines(), "\n"))
	if !strings.Contains(idle, "[n] Next · repo0: Commit changes") || strings.Contains(idle, "intended changes") || strings.Contains(idle, "Next actions") {
		t.Fatal(idle)
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	m = updated.(statusTUIModel)
	if text := ansi.Strip(strings.Join(m.contentLines(), "\n")); !strings.Contains(text, "Local steps run here") || !strings.Contains(text, "owning collection's idle agent pane") {
		t.Fatal(text)
	}
	for i := 1; i < len(rows); i++ {
		updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
		m = updated.(statusTUIModel)
		visible := ansi.Strip(m.View().Content)
		if !strings.Contains(visible, fmt.Sprintf("› [ ] repo%d", i)) || !strings.Contains(visible, fmt.Sprintf("intended changes %d", i)) {
			t.Fatalf("selected row %d hidden: %s", i, visible)
		}
	}
	m.showNext = false
	m.snapshot.ShowCollectionColumn = true
	if text := ansi.Strip(strings.Join(m.contentLines(), "\n")); !strings.Contains(text, "task/repo0: Commit changes") {
		t.Fatal(text)
	}
}

func TestStatusNextViewportUsesRenderedRows(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusOverflowModel()
	m.showHelp = true
	for i := range m.snapshot.Repos {
		r := &m.snapshot.Repos[i]
		r.Next = &next.Step{Step: "commit", By: next.ByAgent, Key: r.Dir, Reason: "Synthetic action reason"}
		if i%3 == 0 {
			r.Choices = &next.Choices{Actions: []next.Choice{{Step: next.Step{Step: "push"}, Blocked: "Publish is not authorized"},
				{Step: next.Step{Step: "merge-pr"}, Blocked: "Current checks required"}}}
		}
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'n'})
	assertFocus := func() {
		t.Helper()
		name := m.snapshot.Repos[m.selectedNext].Dir
		if visible := ansi.Strip(m.View().Content); !strings.Contains(visible, "› [ ] "+name) {
			t.Fatalf("focused %s is offscreen: %s", name, visible)
		}
	}
	assertFocus()
	for i := 1; i < len(m.snapshot.Repos); i++ {
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
		if m.selectedNext != i {
			t.Fatalf("Down selected %d, want %d", m.selectedNext, i)
		}
		assertFocus()
	}
	for i := 1; i < len(m.snapshot.Repos); i++ {
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
		if want := len(m.snapshot.Repos) - 1 - i; m.selectedNext != want {
			t.Fatalf("Up selected %d, want %d", m.selectedNext, want)
		}
		assertFocus()
	}
	top := m.View().Content
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.View().Content == top || m.selectedNext != 0 {
		t.Fatal("Page Down did not scroll independently of action selection")
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.View().Content != top || m.selectedNext != 0 {
		t.Fatal("Page Up did not undo the page without changing action selection")
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(ansi.Strip(m.View().Content), "repo-35") || m.selectedNext != 0 {
		t.Fatal("End did not reach the view tail while retaining action focus")
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	if !strings.Contains(ansi.Strip(m.View().Content), "n next actions") || m.selectedNext != 0 {
		t.Fatal("Home did not reach the help/top without changing action focus")
	}
	for i := 1; i < len(m.snapshot.Repos); i++ {
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m = statusScrollEvent(t, m, tea.WindowSizeMsg{Width: 80, Height: 5})
	assertFocus()
	if m.nextRunning || len(m.nextSelected) != 0 || len(m.nextPending) != 0 {
		t.Fatal("navigation changed action execution or selection state")
	}
}

func TestStatusNextViewportRefreshRetainsFocusedIdentity(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := statusOverflowModel()
	for i := range m.snapshot.Repos {
		r := &m.snapshot.Repos[i]
		r.Next = &next.Step{Step: "commit", By: next.ByAgent, Key: r.Dir, Reason: "Synthetic action reason"}
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'n'})
	for i := 0; i < 15; i++ {
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'j'})
	}
	wanted := m.snapshot.Repos[m.selectedNext].Dir
	fresh := m.snapshot
	fresh.Repos = append([]wtc.StatusRepo(nil), fresh.Repos...)
	for i, j := 0, len(fresh.Repos)-1; i < j; i, j = i+1, j-1 {
		fresh.Repos[i], fresh.Repos[j] = fresh.Repos[j], fresh.Repos[i]
	}
	m = statusScrollEvent(t, m, statusLoadedMsg{snapshot: fresh})
	if got := m.snapshot.Repos[m.selectedNext].Dir; got != wanted || !strings.Contains(ansi.Strip(m.View().Content), "› [ ] "+wanted) {
		t.Fatalf("refresh changed/obscured focused %s: selected %s, %s", wanted, got, m.View().Content)
	}
}

func TestStatusNextKeyboardReadsConfirmationsAndResults(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	fixture := func() statusTUIModel {
		m := statusOverflowModel()
		for i := range m.snapshot.Repos {
			r := &m.snapshot.Repos[i]
			r.Next = &next.Step{Step: "push", By: next.ByWTC, Approve: next.ApprovePerson, Key: r.Dir, Reason: "Synthetic confirmation"}
		}
		return statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'n'})
	}
	t.Run("preview", func(t *testing.T) {
		m := fixture()
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 's'})
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if !strings.Contains(ansi.Strip(m.View().Content), "Selected single steps:") {
			t.Fatal("confirmation began offscreen")
		}
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
		if !strings.Contains(ansi.Strip(m.View().Content), "Enter confirms") {
			t.Fatal("keyboard cannot reach the end of the confirmation")
		}
		if m.nextRunning || len(m.nextPreview) != len(m.snapshot.Repos) {
			t.Fatal("reading the confirmation executed or changed its targets")
		}
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
		if !strings.Contains(ansi.Strip(m.View().Content), "› [x] repo-01") {
			t.Fatal("arrow navigation did not return to the selected actions")
		}
	})
	t.Run("result", func(t *testing.T) {
		m := fixture()
		m.refreshing = true // No collector command is needed for this fixture.
		var lines []string
		for i := 0; i < 30; i++ {
			lines = append(lines, fmt.Sprintf("synthetic result line %d", i))
		}
		lines = append(lines, "result-tail-marker")
		m = statusScrollEvent(t, m, statusNextDoneMsg{text: strings.Join(lines, "\n"), err: errors.New("synthetic failure")})
		if !strings.Contains(ansi.Strip(m.View().Content), "synthetic failure") {
			t.Fatal("new error result began offscreen")
		}
		for i := 0; i < 10; i++ {
			m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		}
		if !strings.Contains(ansi.Strip(m.View().Content), "result-tail-marker") {
			t.Fatal("keyboard cannot page to the result tail")
		}
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
		if strings.Contains(ansi.Strip(m.View().Content), "result-tail-marker") {
			t.Fatal("Home did not leave the result tail")
		}
	})
}

func TestStatusNextLogOverlayRoutesKeysToVisibleView(t *testing.T) {
	m := statusOverflowModel()
	m.showNext, m.refreshing = true, true
	for i := range m.snapshot.Repos {
		r := &m.snapshot.Repos[i]
		r.Next = &next.Step{Step: "commit", By: next.ByAgent, Key: r.Dir, Reason: "Synthetic action"}
		m.progressLog = append(m.progressLog, fmt.Sprintf("synthetic progress %d", i))
	}
	for i := 0; i < 15; i++ {
		m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'j'})
	}
	m = statusScrollEvent(t, m, tea.MouseClickMsg{X: strings.Index(m.headerLine(), "refreshing"), Y: 0, Button: tea.MouseLeft})
	if !m.showLog {
		t.Fatal("refresh control did not open the log")
	}
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "Enter run") || strings.Contains(view, "Esc back") || strings.Contains(view, "↑/↓ choose") {
		t.Fatal("log overlay advertises hidden action bindings")
	}
	bottom := m.View().Content
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'k'})
	if m.View().Content == bottom || m.selectedNext != 15 {
		t.Fatal("log scrolling changed the hidden action focus or stayed stuck")
	}
	m.refreshing = false
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.nextRunning || m.nextPreview != nil {
		t.Fatal("Enter in the log acted on hidden actions")
	}
	m = statusScrollEvent(t, m, tea.KeyPressMsg{Code: 'l'})
	if m.showLog || !strings.Contains(ansi.Strip(m.View().Content), "› [ ] repo-15") {
		t.Fatal("leaving the log did not reveal the retained action focus")
	}
}

func TestStatusNextRefreshRetainsNoticeReadingPosition(t *testing.T) {
	for _, change := range []string{"removed", "reordered"} {
		t.Run(change, func(t *testing.T) {
			m := statusOverflowModel()
			m.showNext = true
			for i := range m.snapshot.Repos {
				r := &m.snapshot.Repos[i]
				r.Next = &next.Step{Step: "commit", By: next.ByAgent, Key: r.Dir, Reason: "Synthetic action"}
			}
			m.selectedNext = 7
			selected := m.snapshot.Repos[m.selectedNext].Dir
			var notice []string
			for i := 0; i < 30; i++ {
				notice = append(notice, fmt.Sprintf("notice-line-%02d", i))
			}
			m.nextNotice = strings.Join(notice, "\n")
			m.revealNextNotice()
			m.scrollBy(5)
			firstBodyLine := func() string {
				return strings.TrimSpace(strings.Split(ansi.Strip(m.View().Content), "\n")[m.viewport(m.contentLayout()).header])
			}
			if got := firstBodyLine(); got != "notice-line-05" {
				t.Fatalf("fixture is not reading the notice: %q", got)
			}
			fresh := m.snapshot
			fresh.Repos = append([]wtc.StatusRepo(nil), fresh.Repos...)
			if change == "removed" {
				for i := range fresh.Repos {
					fresh.Repos[i].Next = nil
				}
			} else {
				for i, j := 0, len(fresh.Repos)-1; i < j; i, j = i+1, j-1 {
					fresh.Repos[i], fresh.Repos[j] = fresh.Repos[j], fresh.Repos[i]
				}
			}
			m = statusScrollEvent(t, m, statusLoadedMsg{snapshot: fresh})
			if got := firstBodyLine(); got != "notice-line-05" {
				t.Fatalf("refresh %s actions skipped the notice being read: %q", change, got)
			}
			if change == "reordered" && statusNextRepos(m.snapshot)[m.selectedNext].Dir != selected {
				t.Fatal("notice anchoring changed the selected action")
			}
			if m.nextRunning || m.nextPreview != nil || len(m.nextSelected) != 0 {
				t.Fatal("refresh dispatched or selected an action")
			}
		})
	}
}
