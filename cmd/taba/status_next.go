package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"taba.sh/taba/internal/next"
	"taba.sh/taba/internal/wtc"
)

type statusNextDoneMsg struct {
	text    string
	err     error
	pending []string
	handoff *statusNextHandoff
}

type statusNextHandoff struct {
	owner  *wtc.Context
	repo   wtc.StatusRepo
	prompt string
}

// Keep process boundaries replaceable in tests; state comparison and routing
// are the same as in the live view.
type statusNextRunner struct {
	readStatus func(*wtc.Context) (wtc.StatusSnapshot, error)
	busy       func(*wtc.Context) (string, error)
	command    func(*wtc.Context, []string) (string, error)
	openURL    func(string) error
	herdr      func(string, ...string) (openHerdrResponse, error)
}

func statusNextRepos(snapshot wtc.StatusSnapshot) []wtc.StatusRepo {
	var repos []wtc.StatusRepo
	for _, r := range snapshot.Repos {
		if r.Next != nil {
			repos = append(repos, r)
		} else if r.Choices != nil && len(r.Choices.Actions) > 0 {
			s := r.Choices.Actions[0].Step
			r.Next = &s
			repos = append(repos, r)
		}
	}
	return repos
}

func statusNextLabel(s *next.Step) string {
	if label := map[string]string{"return-to-tip": "Return to tip", "catch-up": "Catch up", "new-branch": "Create a branch", "resolve": "Resolve conflicts", "merge": "Merge the base", "fix-checks": "Fix checks", "address-review": "Address review", "push": "Push", "merge-pr": "Open pull request", "commit": "Commit changes", "follow-pr": "Check PR progress", "review": "Run independent review"}[s.Step]; label != "" {
		return label
	}
	return s.Step
}

func statusNextLines(snapshot wtc.StatusSnapshot, width int, styled bool, selected int, marked ...map[string]wtc.StatusRepo) []string {
	lines, _ := statusNextRows(snapshot, width, styled, selected, marked...)
	return lines
}

func statusNextRows(snapshot wtc.StatusSnapshot, width int, styled bool, selected int, marked ...map[string]wtc.StatusRepo) ([]string, []statusTUIRow) {
	repos := statusNextRepos(snapshot)
	if len(repos) == 0 {
		return nil, nil
	}
	var rows []statusTUIRow
	lines := []string{"", "Next actions"}
	if styled {
		lines[1] = statusTUIStyle(lines[1], statusToneHeading)
	}
	for i, r := range repos {
		start := len(lines)
		checked := false
		if len(marked) > 0 {
			if choice, ok := marked[0][statusNextIdentity(r)]; ok {
				r.Next = choice.Next
				checked = true
			}
		}
		name := r.Dir
		if snapshot.ShowCollectionColumn {
			name = r.Collection + "/" + name
		}
		who := r.Next.By
		if r.Next.Approve != "" {
			who += "; " + r.Next.Approve + " approval"
		}
		prefix := "  "
		mark := ""
		if len(marked) > 0 {
			mark = "[ ] "
			if checked {
				mark = "[x] "
			}
		}
		if selected == i {
			prefix = "› "
		}
		lines = append(lines, statusTUIFit(statusTUISafe(fmt.Sprintf("%s%s · %s · %s", prefix+mark, name, statusNextLabel(r.Next), who)), width),
			statusTUIFit("    "+statusTUISafe(r.Next.Reason), width))
		if selected == i && r.Choices != nil {
			for _, a := range r.Choices.Actions {
				if !a.Available {
					lines = append(lines, statusTUIFit("    "+statusTUISafe(statusNextLabel(&a.Step)+": "+a.Blocked), width))
				}
			}
		}
		rows = append(rows, statusTUIRow{key: "next:" + statusNextIdentity(r), start: start, end: len(lines)})
	}
	return lines, rows
}

// A click selects a fact, not arbitrary argv. A refresh must still propose
// the same step in the same checkout before execution or an agent handoff.
func statusNextCurrent(wanted wtc.StatusRepo, snapshot wtc.StatusSnapshot) (wtc.StatusRepo, error) {
	for _, r := range snapshot.Repos {
		if r.Collection != wanted.Collection || r.Dir != wanted.Dir || r.Worktree != wanted.Worktree || wanted.Next == nil {
			continue
		}
		if r.Choices != nil {
			if s := r.Choices.Select(wanted.Next.Key); s != nil && reflect.DeepEqual(s, wanted.Next) {
				r.Next = s
				return r, nil
			}
		} else if reflect.DeepEqual(r.Next, wanted.Next) {
			return r, nil
		}
	}
	return wtc.StatusRepo{}, fmt.Errorf("the selected action changed or is blocked; refresh and choose again")
}

func statusNextCommand(r wtc.StatusRepo, userSelected ...bool) ([]string, bool) {
	s := r.Next
	repo := r.Repo
	if repo == "" {
		repo = r.Dir
	}
	want := []string{"wtc", "catch-up", r.Collection, "--repos", repo}
	if s == nil || s.By != next.ByWTC || (s.Approve != "" && !(len(userSelected) > 0 && userSelected[0] && s.Step == "catch-up" && s.Approve == next.ApproveAgent)) || !reflect.DeepEqual(s.Command, want) {
		return nil, false
	}
	// Leave any newly dirty or busy checkout with its owner.
	return append(want[1:], "--clean-only", "--idle-only"), true
}

func statusNextRun(c *wtc.Context, selected wtc.StatusRepo) tea.Cmd {
	return statusNextRunWith(c, selected, statusNextDefaultRunner())
}

func statusNextDefaultRunner() statusNextRunner {
	return statusNextRunner{
		readStatus: (*wtc.Context).StatusForgePreview,
		busy:       (*wtc.Context).AgentBusy,
		command: func(owner *wtc.Context, argv []string) (string, error) {
			binary, err := os.Executable()
			if err != nil {
				return "", err
			}
			cmd := exec.Command(binary, argv...)
			cmd.Dir = owner.Collection
			out, err := cmd.CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		openURL: func(target string) error { return statusOpenURL(target)().(statusOpenedMsg).err },
		herdr:   openHerdr,
	}
}

func statusNextRunWith(c *wtc.Context, selected wtc.StatusRepo, run statusNextRunner) tea.Cmd {
	return statusNextRunInContext(context.Background(), c, selected, run, false)
}

func statusNextRunInContext(ctx context.Context, c *wtc.Context, selected wtc.StatusRepo, run statusNextRunner, deferHandoff bool) tea.Cmd {
	return func() tea.Msg {
		failed := func(err error) tea.Msg { return statusNextDoneMsg{err: err} }
		if c == nil || selected.Collection == "" || selected.Collection == "." || selected.Collection == ".." || filepath.Base(selected.Collection) != selected.Collection {
			return failed(fmt.Errorf("cannot locate this collection"))
		}
		owner, err := wtc.OpenCollection(filepath.Join(c.Workspace, selected.Collection))
		if err != nil {
			return failed(err)
		}
		fresh, err := run.readStatus(owner)
		if err != nil {
			return failed(err)
		}
		r, err := statusNextCurrent(selected, fresh)
		if err != nil {
			return failed(err)
		}
		if err := ctx.Err(); err != nil {
			return failed(err)
		}
		if argv, ok := statusNextCommand(r, true); ok {
			state, err := run.busy(owner)
			if err != nil {
				return failed(err)
			}
			if state != "" {
				return failed(fmt.Errorf("the collection's agent is %s; leave the checkout with its owner", state))
			}
			if err := ctx.Err(); err != nil {
				return failed(err)
			}
			out, err := run.command(owner, argv)
			return statusNextDoneMsg{text: out, err: err}
		}
		if r.Next.By == next.ByPerson && r.Next.Step == "merge-pr" && r.PR != nil && r.PR.URL != "" {
			target := statusTUIURL(r.PR.URL)
			if target == "" {
				return failed(fmt.Errorf("the pull request has no web URL"))
			}
			if err := ctx.Err(); err != nil {
				return failed(err)
			}
			return statusNextDoneMsg{text: "Opened the pull request", err: run.openURL(target)}
		}
		skill := r.Next.Skill
		if r.Next.By == next.ByWTC && r.Next.Approve == next.ApproveAgent && r.Next.Step == "catch-up" {
			skill = "wtc-catch-up"
		}
		if skill == "" || r.Next.Approve == next.ApprovePerson || (r.Next.By != next.ByAgent && r.Next.Approve != next.ApproveAgent) {
			return failed(fmt.Errorf("this step needs its owner's action"))
		}
		prompt := fmt.Sprintf("/skill %s %s: %s. Work on this repository only; recheck its state before acting. Carry out only this selected step; report follow-up actions for separate selection.", skill, r.Dir, r.Next.Reason)
		if deferHandoff {
			return statusNextDoneMsg{handoff: &statusNextHandoff{owner: owner, repo: r, prompt: prompt}}
		}
		return statusNextPrompt(owner, r, prompt, run, ctx)
	}
}

func statusNextPrompt(owner *wtc.Context, r wtc.StatusRepo, prompt string, run statusNextRunner, contexts ...context.Context) tea.Msg {
	failed := func(err error) tea.Msg { return statusNextDoneMsg{err: err} }
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	instruction := func(reason string) tea.Msg {
		return statusNextDoneMsg{text: reason + ". Not sent. Ask the collection's agent: " + prompt}
	}
	// The owning collection's named author pane is the destination. Focus can
	// point to status, a shell or a reviewer, so it never selects a recipient.
	session := openSession(owner, "")
	workspaces, err := run.herdr(session, "workspace", "list")
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	if err != nil {
		return instruction("No herdr destination available")
	}
	var matches []openWorkspaceInfo
	for _, workspace := range workspaces.Result.Workspaces {
		if workspace.Label == r.Collection {
			matches = append(matches, workspace)
		}
	}
	if len(matches) != 1 {
		return instruction("Cannot identify one workspace for " + r.Collection)
	}
	panes, err := run.herdr(session, "pane", "list", "--workspace", matches[0].ID)
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	if err != nil {
		return failed(err)
	}
	var agents []openPaneInfo
	for _, pane := range panes.Result.Panes {
		if pane.Label == "agent" && pane.ID != "" && pane.Agent != "" {
			agents = append(agents, pane)
		}
	}
	if len(agents) != 1 {
		return instruction("Cannot identify one author agent pane in " + r.Collection)
	}
	pane := agents[0]
	destination := r.Collection + " / " + pane.Agent + " (" + pane.ID + ")"
	if pane.AgentStatus != "idle" && pane.AgentStatus != "done" {
		return instruction("Author agent " + destination + " is " + pane.AgentStatus)
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	_, err = run.herdr(session, "agent", "prompt", pane.ID, prompt)
	result := statusNextDoneMsg{err: err}
	if err == nil {
		result.text = "Sent to " + destination + "; completion pending"
		result.pending = []string{statusNextPendingKey(r)}
	} else {
		result.text = "Could not send to " + destination
	}
	return result
}

// Selected batches serialize collection-wide commands and send one prompt per
// author conversation. Each target is freshly checked; failures do not roll
// back independent work, and an accepted handoff is never called completed.
func statusNextBatch(ctx context.Context, c *wtc.Context, targets []wtc.StatusRepo, run statusNextRunner) tea.Cmd {
	return func() tea.Msg {
		var lines []string
		var pending []string
		groups := map[string][]statusNextHandoff{}
		var order []string
		for _, r := range targets {
			if ctx.Err() != nil {
				lines = append(lines, r.Dir+": cancelled before dispatch")
				continue
			}
			result := statusNextRunInContext(ctx, c, r, run, true)().(statusNextDoneMsg)
			if result.handoff != nil {
				key := result.handoff.owner.Collection
				if _, ok := groups[key]; !ok {
					order = append(order, key)
				}
				groups[key] = append(groups[key], *result.handoff)
				continue
			}
			if result.err != nil {
				lines = append(lines, r.Dir+": failed or stale: "+result.err.Error())
			} else {
				lines = append(lines, r.Dir+": "+result.text)
			}
		}
		for _, key := range order {
			var current []statusNextHandoff
			for _, h := range groups[key] {
				if ctx.Err() != nil {
					lines = append(lines, h.repo.Dir+": cancelled before handoff")
					continue
				}
				fresh, err := run.readStatus(h.owner)
				if err == nil {
					_, err = statusNextCurrent(h.repo, fresh)
				}
				if err != nil {
					lines = append(lines, h.repo.Dir+": failed or stale: "+err.Error())
					continue
				}
				current = append(current, h)
			}
			if len(current) == 0 {
				continue
			}
			prompt := "/skill wtc Carry out only these selected single steps, sequentially. Recheck current state, ownership and branch policy. Honor publication authority and confirmation requirements; commit steps save local commits only. Do not automatically continue to another step.\n"
			for _, h := range current {
				prompt += h.prompt + "\n"
			}
			if ctx.Err() != nil {
				for _, h := range current {
					lines = append(lines, h.repo.Dir+": cancelled before handoff")
				}
				continue
			}
			result := statusNextPrompt(current[0].owner, current[0].repo, prompt, run, ctx).(statusNextDoneMsg)
			if result.err != nil {
				lines = append(lines, key+": handoff failed: "+result.err.Error())
			} else {
				lines = append(lines, result.text)
				if len(result.pending) > 0 {
					for _, h := range current {
						pending = append(pending, statusNextPendingKey(h.repo))
					}
				}
				if len(result.pending) > 0 {
					for _, h := range current {
						lines = append(lines, h.repo.Dir+": handoff pending; verify completion in the author conversation")
					}
				}
			}
		}
		return statusNextDoneMsg{text: strings.Join(lines, "\n"), pending: pending}
	}
}

func statusNextIdentity(r wtc.StatusRepo) string { return r.Collection + "/" + r.Dir }

func statusNextCycle(r wtc.StatusRepo) wtc.StatusRepo {
	if r.Choices == nil || len(r.Choices.Actions) == 0 {
		return r
	}
	for i, a := range r.Choices.Actions {
		if r.Next != nil && a.Key == r.Next.Key {
			r.Next = &r.Choices.Actions[(i+1)%len(r.Choices.Actions)].Step
			return r
		}
	}
	r.Next = &r.Choices.Actions[0].Step
	return r
}

// Keep captured targets even when a refresh removes their row. They must fail
// revalidation visibly instead of silently disappearing from the user's batch.
func statusNextTargets(snapshot wtc.StatusSnapshot, selected map[string]wtc.StatusRepo) []wtc.StatusRepo {
	var out []wtc.StatusRepo
	seen := map[string]bool{}
	for _, r := range statusNextRepos(snapshot) {
		key := statusNextIdentity(r)
		if target, ok := selected[key]; ok {
			out = append(out, target)
			seen[key] = true
		}
	}
	var missing []string
	for key := range selected {
		if !seen[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		out = append(out, selected[key])
	}
	return out
}

func statusNextPendingKey(r wtc.StatusRepo) string {
	key := r.Next.Key
	if r.Choices != nil {
		parts := strings.Split(key, ":")
		key = parts[len(parts)-1]
	}
	return statusNextIdentity(r) + ":" + key
}

// Person-approved operations and pushes keep a preview. Catch-up, local work
// and handoffs run directly; Then hints never grant follow-up authority.
func statusNextConfirmTargets(targets []wtc.StatusRepo) bool {
	for _, r := range targets {
		if r.Next.Approve == next.ApprovePerson || r.Next.Step == "push" {
			return true
		}
	}
	return false
}
