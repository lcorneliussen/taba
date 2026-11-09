package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/envcompat"
	"taba.sh/taba/internal/wtc"
)

func openCollection(source *wtc.Context, name string, opt openOptions, desired string, running, last bool) openItem {
	item := openItem{Collection: name, Layout: desired, Actions: []string{}}
	targetDir := filepath.Join(source.Workspace, name)
	target, err := wtc.OpenCollection(targetDir)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	workspaces := []openWorkspaceInfo{}
	if running {
		workspaces, err = openWorkspaces(opt.Session)
		if err != nil {
			item.Error = err.Error()
			return item
		}
	}
	for _, workspace := range workspaces {
		if workspace.Label == name {
			item.Workspace = workspace.ID
			break
		}
	}
	if item.Workspace == "" {
		if opt.List {
			item.Actions = append(item.Actions, "no workspace")
			return item
		}
		if opt.DryRun {
			item.Actions = append(item.Actions, "would create workspace ("+desired+")")
			return item
		}
		if err := openCreateWorkspace(target, opt.Session, name); err != nil {
			item.Error = err.Error()
			return item
		}
		workspaces, err = openWorkspaces(opt.Session)
		if err != nil {
			item.Error = err.Error()
			return item
		}
		for _, workspace := range workspaces {
			if workspace.Label == name {
				item.Workspace = workspace.ID
				break
			}
		}
		if item.Workspace == "" {
			item.Error = "herdr created a workspace but did not list it"
			return item
		}
		if _, err := openEnsureLayout(opt.Session, item.Workspace, target.Collection, desired); err != nil {
			item.Error = err.Error()
			return item
		}
		openWaitWorkspaceIdle(opt.Session, item.Workspace)
		item.Actions = append(item.Actions, "workspace created ("+desired+")")
	} else {
		current, panes, err := openLayoutState(opt.Session, item.Workspace)
		if err != nil {
			item.Error = err.Error()
			return item
		}
		if opt.List {
			item.Layout = current
			item.Actions = append(item.Actions, openLayoutDescription(current, panes))
			for _, label := range []string{"agent", "browse", "shell", "review", "status"} {
				state, inspectErr := openPaneState(opt.Session, openPaneByLabel(panes, label))
				item.Actions = append(item.Actions, label+": "+state)
				if inspectErr != nil {
					if item.Error != "" {
						item.Error += "; "
					}
					item.Error += fmt.Sprintf("%s: %v", label, inspectErr)
				}
			}
			return item
		}
		if current == "unstacked" && !opt.LayoutSet {
			desired, item.Layout = "narrow", "narrow" // Auto restacks; it does not flip to wide.
		}
		if current == "partial" || current == "unstacked" || opt.LayoutSet && current != desired {
			if opt.DryRun {
				item.Actions = append(item.Actions, "would layout "+current+" → "+desired)
			} else {
				change, err := openEnsureLayout(opt.Session, item.Workspace, target.Collection, desired)
				if err != nil {
					item.Error = err.Error()
					return item
				}
				if change != "unchanged" {
					item.Actions = append(item.Actions, "layout "+change+" ("+desired+")")
					openWaitWorkspaceIdle(opt.Session, item.Workspace)
				}
			}
		} else {
			item.Layout = current // Auto does not flip a narrow or wide workspace.
		}
	}
	if opt.DryRun {
		panes, err := openPanes(opt.Session, item.Workspace)
		if err != nil {
			item.Error = err.Error()
			return item
		}
		for _, kind := range opt.Tools {
			if openPaneByLabel(panes, kind).ID == "" {
				item.Actions = append(item.Actions, "would add "+kind+" pane")
			}
		}
		if err := openPlanPanes(&item, panes, opt); err != nil {
			item.Error = err.Error()
		}
		return item
	}
	added, err := openEnsureTools(opt.Session, item.Workspace, target.Collection, item.Layout, opt.Tools)
	for _, pane := range added {
		openWaitIdle(opt.Session, pane, 10*time.Second)
	}
	if len(added) > 0 {
		item.Actions = append(item.Actions, fmt.Sprintf("%d tool pane(s) added", len(added)))
	}
	if err != nil {
		item.Error = err.Error()
		return item
	}
	panes, err := openPanes(opt.Session, item.Workspace)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	if err := openStartPanes(&item, target, panes, opt); err != nil {
		item.Error = err.Error()
	}
	if opt.Focus && last {
		if _, err := openHerdr(opt.Session, "workspace", "focus", item.Workspace); err != nil {
			item.Error = err.Error()
		}
	}
	if len(item.Actions) == 0 {
		item.Actions = append(item.Actions, "already open")
	}
	return item
}

func openReadEnvironment(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var settings []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("invalid environment line in %s", path)
		}
		// Herdr receives these as environment assignments. Preserve the value
		// exactly as the shell compatibility entry point passes it.
		settings = append(settings, key+"="+value)
	}
	return settings, nil
}

func openCreateWorkspace(target *wtc.Context, session, name string) error {
	args := []string{"workspace", "create", "--cwd", target.Collection, "--label", name, "--no-focus"}
	for _, file := range []string{".env.collection", ".env.collection.local"} {
		settings, err := openReadEnvironment(filepath.Join(target.Collection, file))
		if err != nil {
			return err
		}
		for _, setting := range settings {
			args = append(args, "--env", setting)
		}
	}
	path, err := target.AgentToolchainPath(true)
	if err == nil && path != "" {
		for _, setting := range envcompat.Both("WTC_TOOLCHAIN_PATH", path) {
			args = append(args, "--env", setting)
		}
	}
	for _, setting := range envcompat.Both("WTC_AGENT_NAME", openAgentName(session, name)) {
		args = append(args, "--env", setting)
	}
	_, err = openHerdr(session, args...)
	return err
}

func openAgentName(session, collection string) string {
	return wtc.AgentName(session, collection)
}

func openPaneState(session string, pane openPaneInfo) (string, error) {
	if pane.ID == "" {
		return "no pane", nil
	}
	argv, err := openPaneCommand(session, pane.ID)
	if err != nil {
		return "unknown", err
	}
	if openShellCommand(argv) {
		if pane.Label == "shell" {
			return "ready", nil
		}
		if pane.Agent != "" {
			return "empty (" + pane.Agent + " exited)", nil
		}
		return "empty", nil
	}
	if pane.Agent != "" {
		return pane.Agent + " " + pane.AgentStatus, nil
	}
	if len(argv) == 0 {
		return "busy", nil
	}
	for _, word := range argv {
		name := filepath.Base(word)
		if strings.HasPrefix(word, "-") || strings.Contains(word, "=") {
			continue
		}
		switch name {
		case "env", "zsh", "bash", "fish", "sh", "nu", "dash", "ksh", "python3", "python", "node", "ruby", "perl":
			continue
		}
		return name, nil
	}
	return filepath.Base(argv[0]), nil
}

func openPlanPanes(item *openItem, panes []openPaneInfo, opt openOptions) error {
	var failures []error
	for _, label := range []string{"agent", "browse", "review", "status"} {
		if label == "review" && !openWants(opt, label) {
			continue
		}
		if label == "agent" && opt.NoAgent || label == "browse" && opt.NoBrowse || label == "status" && opt.NoStatus {
			continue
		}
		pane := openPaneByLabel(panes, label)
		state, err := openPaneState(opt.Session, pane)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", label, err))
			continue
		}
		if state == "empty" || strings.HasPrefix(state, "empty (") {
			item.Actions = append(item.Actions, label+" empty → would start")
		}
	}
	return errors.Join(failures...)
}

func openStartPanes(item *openItem, target *wtc.Context, panes []openPaneInfo, opt openOptions) error {
	var failures []error
	for _, label := range []string{"agent", "browse", "shell", "review", "status"} {
		if label == "agent" && opt.NoAgent || label == "browse" && opt.NoBrowse || label == "status" && opt.NoStatus {
			item.Actions = append(item.Actions, label+" skipped")
			continue
		}
		pane := openPaneByLabel(panes, label)
		if pane.ID == "" && (label == "browse" || label == "shell" || label == "review") && !openWants(opt, label) {
			continue // Tool panes come on demand.
		}
		state, inspectErr := openPaneState(opt.Session, pane)
		if inspectErr != nil {
			item.Actions = append(item.Actions, label+" inspection failed")
			failures = append(failures, fmt.Errorf("%s: %w", label, inspectErr))
			continue
		}
		switch label {
		case "agent":
			if pane.ID == "" {
				item.Actions = append(item.Actions, "agent no pane")
				failures = append(failures, errors.New("agent pane is missing"))
				continue
			}
			if state != "empty" && !strings.HasPrefix(state, "empty (") {
				item.Actions = append(item.Actions, "agent live ("+state+")")
				continue
			}
			if err := openStartAgent(target, opt, pane.ID); err != nil {
				failures = append(failures, fmt.Errorf("agent: %w", err))
				if strings.Contains(err.Error(), "first prompt") {
					item.Actions = append(item.Actions, "agent started; "+err.Error())
				} else {
					item.Actions = append(item.Actions, "agent start failed: "+err.Error())
				}
			} else {
				item.Actions = append(item.Actions, "agent started")
			}
		case "browse", "review", "status":
			if pane.ID == "" {
				item.Actions = append(item.Actions, label+" no pane")
				failures = append(failures, fmt.Errorf("%s pane is missing", label))
				continue
			}
			if state != "empty" && !strings.HasPrefix(state, "empty (") {
				item.Actions = append(item.Actions, label+" live ("+state+")")
				continue
			}
			if label == "review" && !openWants(opt, label) {
				item.Actions = append(item.Actions, "review empty (use --add review to restart)")
				continue
			}
			if label == "browse" {
				if _, err := exec.LookPath("nvim"); err != nil {
					item.Actions = append(item.Actions, "browse empty (no nvim)")
					continue
				}
			}
			command := "wtc status --tui"
			if label == "browse" {
				command = "wtc browse --here"
			}
			if label == "review" {
				var detail string
				var err error
				command, detail, err = openReviewCommand(target)
				if err != nil {
					failures = append(failures, fmt.Errorf("review: %w", err))
					continue
				}
				item.Actions = append(item.Actions, detail)
			}
			if !openWaitIdle(opt.Session, pane.ID, 0) {
				item.Actions = append(item.Actions, label+" busy — left alone")
				continue
			}
			if _, err := openHerdr(opt.Session, "pane", "run", pane.ID, command); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", label, err))
				item.Actions = append(item.Actions, label+" start failed: "+err.Error())
			} else {
				item.Actions = append(item.Actions, label+" started")
			}
		case "shell":
			item.Actions = append(item.Actions, "shell "+state)
			if pane.ID == "" {
				failures = append(failures, errors.New("shell pane is missing"))
			}
		}
	}
	return errors.Join(failures...)
}

func openWants(opt openOptions, kind string) bool {
	for _, tool := range opt.Tools {
		if tool == kind {
			return true
		}
	}
	return false
}

func openStartAgent(target *wtc.Context, opt openOptions, pane string) error {
	kind := opt.Agent
	if kind == "" {
		kind = statusSetting(target, "WTC_AGENT_KIND")
	}
	if kind == "" {
		kind = "claude"
	}
	args := opt.AgentArgs
	configuredArgs := false
	if !opt.AgentArgsSet {
		args = statusSetting(target, "WTC_AGENT_ARGS")
		configuredArgs = args != ""
	}
	if !opt.AgentArgsSet && !configuredArgs && kind == "claude" {
		args = "--dangerously-skip-permissions"
	}
	if kind == "claude" && !opt.NoRemoteControl && !opt.AgentArgsSet && !configuredArgs {
		args += " --remote-control " + openAgentName(opt.Session, filepath.Base(target.Collection))
	}
	command := []string{"agent", "start", openAgentName(opt.Session, filepath.Base(target.Collection)), "--kind", kind, "--pane", pane}
	if args != "" {
		command = append(command, "--")
		command = append(command, strings.Fields(args)...)
	}
	var last error
	for attempt := 0; attempt < 15; attempt++ {
		if _, err := openHerdr(opt.Session, command...); err == nil {
			last = nil
			break
		} else {
			last = err
		}
		time.Sleep(time.Second)
	}
	if last != nil {
		return last
	}
	if opt.NoFirstPrompt {
		return nil
	}
	if _, err := os.Stat(filepath.Join(target.Collection, "HANDOFF.md")); err != nil {
		return nil
	}
	prompt := firstAgentPrompt(target.Collection, kind)
	if _, err := openHerdr(opt.Session, "agent", "prompt", pane, prompt, "--wait", "--until", "working", "--timeout", "20000"); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "agent_prompt_stalled") {
		return fmt.Errorf("agent started, but first prompt was not taken: %w", err)
	}
	if _, err := openHerdr(opt.Session, "agent", "send-keys", pane, "enter"); err != nil {
		return fmt.Errorf("first prompt was not taken: %w", err)
	}
	if _, err := openHerdr(opt.Session, "agent", "wait", pane, "--until", "working", "--timeout", "10000"); err != nil {
		return fmt.Errorf("first prompt was not taken: %w", err)
	}
	return nil
}

// firstAgentPrompt starts the agent of a new collection on its launch note,
// and says so up front when the collection only reviews someone else's PR.
func firstAgentPrompt(collection, kind string) string {
	prompt := "Run the wtc-start skill (harness/skills/wtc-start/SKILL.md): read HANDOFF.md at the collection root, then start."
	if kind == "claude" {
		prompt = "/wtc-start"
	}
	if _, ok := wtc.ReadReviewMode(collection); ok {
		prompt += branding.Text(" This collection is review-only: produce and post the review, nothing else. No commits, pushes, thread resolution or `{{command}} review ready`.")
	}
	return prompt
}
