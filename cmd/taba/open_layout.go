package main

import (
	"fmt"
	"strconv"
	"strings"
)

// openLayoutState classifies a workspace by its agent and status panes. The
// browse, review and shell tool panes are optional: a missing one is not damage, but
// one that is present has to sit where the layout puts it.
func openLayoutState(host terminalHost, workspace string) (string, []openPaneInfo, error) {
	panes, err := host.Panes(workspace)
	if err != nil {
		return "", nil, err
	}
	tabs, err := host.Tabs(workspace)
	if err != nil {
		return "", nil, err
	}
	agent, status := openPaneByLabel(panes, "agent"), openPaneByLabel(panes, "status")
	browse, shell := openPaneByLabel(panes, "browse"), openPaneByLabel(panes, "shell")
	review := openPaneByLabel(panes, "review")
	if agent.ID == "" || status.ID == "" || agent.TabID != status.TabID {
		return "partial", panes, nil
	}
	tools := []openPaneInfo{}
	for _, pane := range []openPaneInfo{browse, review, shell} {
		if pane.ID != "" {
			tools = append(tools, pane)
		}
	}
	if tab := openTabByLabel(tabs, "tools").ID; tab != "" {
		for _, pane := range tools {
			if pane.TabID != tab {
				return "partial", panes, nil
			}
		}
		pairs := [][2]string{{agent.ID, status.ID}}
		for i := 1; i < len(tools); i++ {
			pairs = append(pairs, [2]string{tools[i-1].ID, tools[i].ID})
		}
		for _, pair := range pairs {
			stacked, err := openStacked(host, workspace, pair[0], pair[1])
			if err != nil {
				return "", nil, err
			}
			if !stacked {
				return "unstacked", panes, nil
			}
		}
		return "narrow", panes, nil
	}
	for _, pane := range tools {
		if pane.TabID != agent.TabID {
			return "partial", panes, nil
		}
	}
	stacked, err := openStacked(host, workspace, agent.ID, status.ID)
	if err != nil {
		return "", nil, err
	}
	if stacked {
		// Narrow before any tool pane was added: agent over status, no tools tab.
		if len(tools) == 0 {
			return "narrow", panes, nil
		}
		return "partial", panes, nil
	}
	// Wide keeps status out of the agent's column; status above the agent is
	// a reversed stack.
	if openSameColumn(host, workspace, agent.ID, status.ID) {
		return "partial", panes, nil
	}
	var pairs [][2]string
	for _, pane := range []openPaneInfo{browse, review} {
		if pane.ID != "" {
			pairs = append(pairs, [2]string{pane.ID, status.ID})
		}
	}
	if browse.ID != "" && review.ID != "" {
		pairs = append(pairs, [2]string{browse.ID, review.ID})
	}
	for _, pair := range pairs {
		stacked, err := openStacked(host, workspace, pair[0], pair[1])
		if err != nil {
			return "", nil, err
		}
		if !stacked {
			return "partial", panes, nil
		}
	}
	if shell.ID == "" || openSameColumn(host, workspace, agent.ID, shell.ID) {
		return "wide", panes, nil
	}
	return "partial", panes, nil
}

func openToolPanes(panes []openPaneInfo) []openPaneInfo {
	var tools []openPaneInfo
	for _, label := range []string{"browse", "review", "shell"} {
		if pane := openPaneByLabel(panes, label); pane.ID != "" {
			tools = append(tools, pane)
		}
	}
	return tools
}

func openAgentHome(panes []openPaneInfo) string {
	if pane := openPaneByLabel(panes, "agent"); pane.ID != "" {
		return pane.ID
	}
	for _, pane := range panes {
		if pane.Agent != "" {
			return pane.ID
		}
	}
	if len(panes) > 0 {
		return panes[0].ID
	}
	return ""
}

func openApplyWide(host terminalHost, agent, cwd string) error {
	if err := host.RenamePane(agent, "agent"); err != nil {
		return err
	}
	status, err := host.Split(agent, "right", "0.40", cwd)
	if err != nil {
		return err
	}
	return host.RenamePane(status, "status")
}

func openApplyNarrow(host terminalHost, agent, cwd string) error {
	if tab := host.PaneTab(agent); tab != "" {
		if err := host.RenameTab(tab, "main"); err != nil {
			return err
		}
	}
	if err := host.RenamePane(agent, "agent"); err != nil {
		return err
	}
	status, err := host.Split(agent, "down", "0.65", cwd)
	if err != nil {
		return err
	}
	return host.RenamePane(status, "status")
}

// openMoveBelow puts pane under target in target's tab.
func openMoveBelow(host terminalHost, workspace, pane, target, ratio string) error {
	return openMoveBeside(host, workspace, pane, target, "down", ratio)
}

// openMoveBeside puts pane next to target in target's tab. herdr ignores a
// move within one tab, so a pane already there leaves through a temporary tab,
// which keeps its process alive and closes once the pane moves back.
func openMoveBeside(host terminalHost, workspace, pane, target, direction, ratio string) error {
	tab := host.PaneTab(target)
	if tab == "" {
		return fmt.Errorf("cannot find the tab of pane %s", target)
	}
	if host.PaneTab(pane) == tab {
		if err := host.MovePaneToNewTab(workspace, pane, "restack-tmp"); err != nil {
			return err
		}
	}
	return host.MovePane(pane, tab, direction, target, ratio)
}

// openRestackNarrow repairs a narrow workspace whose tabs hold the right panes
// side by side instead of stacked.
func openRestackNarrow(host terminalHost, workspace string, panes []openPaneInfo) error {
	stacks := [][3]string{{"agent", "status", "0.65"}}
	tools := openToolPanes(panes)
	for i := 1; i < len(tools); i++ {
		ratio := "0.50"
		if tools[i].Label == "shell" {
			ratio = "0.80"
		}
		stacks = append(stacks, [3]string{tools[i-1].Label, tools[i].Label, ratio})
	}
	for _, stack := range stacks {
		upper, lower := openPaneByLabel(panes, stack[0]).ID, openPaneByLabel(panes, stack[1]).ID
		if upper == "" || lower == "" {
			continue
		}
		stacked, err := openStacked(host, workspace, upper, lower)
		if err != nil {
			return err
		}
		if stacked {
			continue
		}
		if err := openMoveBelow(host, workspace, lower, upper, stack[2]); err != nil {
			return err
		}
	}
	return nil
}

// openSwitchNarrow moves the panes a workspace has into the narrow layout;
// it creates only a missing status pane, never a tool pane.
func openSwitchNarrow(host terminalHost, workspace, agent, cwd string, panes []openPaneInfo) error {
	if err := host.RenamePane(agent, "agent"); err != nil {
		return err
	}
	if tab := host.PaneTab(agent); tab != "" {
		if err := host.RenameTab(tab, "main"); err != nil {
			return err
		}
	}
	tools := openToolPanes(panes)
	for i, pane := range tools {
		if i == 0 {
			if err := host.MovePaneToNewTab(workspace, pane.ID, "tools"); err != nil {
				return err
			}
		} else {
			ratio := "0.50"
			if pane.Label == "shell" {
				ratio = "0.80"
			}
			if err := openMoveBelow(host, workspace, pane.ID, tools[i-1].ID, ratio); err != nil {
				return err
			}
		}
	}
	if status := openPaneByLabel(panes, "status").ID; status != "" {
		return openMoveBelow(host, workspace, status, agent, "0.65")
	}
	status, err := host.Split(agent, "down", "0.65", cwd)
	if err != nil {
		return err
	}
	return host.RenamePane(status, "status")
}

// openSwitchWide moves the panes a workspace has into the wide layout; it
// creates only a missing status pane, never a tool pane.
func openSwitchWide(host terminalHost, workspace, agent, cwd string, panes []openPaneInfo) error {
	if err := host.RenamePane(agent, "agent"); err != nil {
		return err
	}
	main := host.PaneTab(agent)
	if main == "" {
		return fmt.Errorf("cannot find agent tab")
	}
	browse := openPaneByLabel(panes, "browse").ID
	review := openPaneByLabel(panes, "review").ID
	top := browse
	if top == "" {
		top = review
	}
	shell := openPaneByLabel(panes, "shell").ID
	status := openPaneByLabel(panes, "status").ID
	if top != "" {
		if err := openMoveBeside(host, workspace, top, agent, "right", "0.40"); err != nil {
			return err
		}
	}
	bottom := top
	if browse != "" && review != "" {
		if err := openMoveBelow(host, workspace, review, browse, "0.50"); err != nil {
			return err
		}
		bottom = review
	}
	switch {
	case status != "" && top != "":
		if err := openMoveBelow(host, workspace, status, bottom, "0.65"); err != nil {
			return err
		}
	case status != "":
		if err := openMoveBeside(host, workspace, status, agent, "right", "0.40"); err != nil {
			return err
		}
	default:
		base, direction, ratio := agent, "right", "0.40"
		if top != "" {
			base, direction, ratio = bottom, "down", "0.65"
		}
		created, err := host.Split(base, direction, ratio, cwd)
		if err != nil {
			return err
		}
		if err := host.RenamePane(created, "status"); err != nil {
			return err
		}
	}
	if shell != "" {
		if err := openMoveBelow(host, workspace, shell, agent, "0.80"); err != nil {
			return err
		}
	}
	return nil
}

func openEnsureLayout(host terminalHost, workspace, cwd, desired string) (string, error) {
	current, panes, err := openLayoutState(host, workspace)
	if err != nil {
		return "", err
	}
	if current == desired {
		return "unchanged", nil
	}
	agent := openAgentHome(panes)
	if agent == "" {
		return "", fmt.Errorf("workspace %s has no pane for the agent", workspace)
	}
	if current == "partial" && len(panes) == 1 {
		if desired == "narrow" {
			err = openApplyNarrow(host, agent, cwd)
		} else {
			err = openApplyWide(host, agent, cwd)
		}
		return "built", err
	}
	if current == "unstacked" && desired == "narrow" {
		return "restacked", openRestackNarrow(host, workspace, panes)
	}
	if desired == "narrow" {
		err = openSwitchNarrow(host, workspace, agent, cwd, panes)
	} else {
		err = openSwitchWide(host, workspace, agent, cwd, panes)
	}
	return "switched", err
}

// openAddTool adds one browse, review or shell pane where the layout puts it: on the
// tools tab in narrow, beside the agent in wide. A pane that has to sit above
// an existing one is split off it, and the existing pane moves below.
func openAddTool(host terminalHost, workspace, cwd, layout, kind string, panes []openPaneInfo) (string, error) {
	agent := openPaneByLabel(panes, "agent").ID
	status := openPaneByLabel(panes, "status").ID
	browse := openPaneByLabel(panes, "browse").ID
	var id string
	var err error
	review := openPaneByLabel(panes, "review").ID
	tools := openToolPanes(panes)
	switch {
	case layout == "narrow" && len(tools) > 0:
		order := map[string]int{"browse": 0, "review": 1, "shell": 2}
		var upper, lower string
		for _, pane := range tools {
			if order[pane.Label] < order[kind] {
				upper = pane.ID
			} else if lower == "" {
				lower = pane.ID
			}
		}
		ratio := "0.50"
		if kind == "shell" {
			ratio = "0.80"
		}
		if upper != "" {
			id, err = host.Split(upper, "down", ratio, cwd)
		} else {
			id, err = host.Split(lower, "right", "0.50", cwd)
		}
		if err == nil && lower != "" {
			previous := id
			for _, pane := range tools {
				if order[pane.Label] < order[kind] {
					continue
				}
				lowerRatio := "0.50"
				if pane.Label == "shell" {
					lowerRatio = "0.80"
				}
				if err = openMoveBelow(host, workspace, pane.ID, previous, lowerRatio); err != nil {
					break
				}
				previous = pane.ID
			}
		}
	case layout == "narrow":
		id, err = host.CreateTab(workspace, cwd, "tools", nil)
		if err == nil && id == "" {
			err = fmt.Errorf("%s did not return the tools tab's pane id", host.Kind())
		}
	case agent == "":
		return "", fmt.Errorf("workspace %s has no agent pane to add %s beside", workspace, kind)
	case kind == "review" && browse != "":
		if id, err = host.Split(browse, "down", "0.50", cwd); err == nil && status != "" {
			err = openMoveBelow(host, workspace, status, id, "0.65")
		}
	case kind == "browse" && review != "":
		if id, err = host.Split(review, "right", "0.50", cwd); err == nil {
			err = openMoveBelow(host, workspace, review, id, "0.50")
			if err == nil && status != "" {
				err = openMoveBelow(host, workspace, status, review, "0.65")
			}
		}
	case (kind == "browse" || kind == "review") && status != "":
		if id, err = host.Split(status, "right", "0.50", cwd); err == nil {
			err = openMoveBelow(host, workspace, status, id, "0.65")
		}
	case kind == "browse" || kind == "review":
		id, err = host.Split(agent, "right", "0.40", cwd)
	default:
		id, err = host.Split(agent, "down", "0.80", cwd)
	}
	if err != nil {
		return "", err
	}
	return id, host.RenamePane(id, kind)
}

// openEnsureTools adds each wanted tool pane the workspace does not have yet.
func openEnsureTools(host terminalHost, workspace, cwd, layout string, tools []string) ([]string, error) {
	var added []string
	for _, kind := range tools {
		panes, err := host.Panes(workspace)
		if err != nil {
			return added, err
		}
		if openPaneByLabel(panes, kind).ID != "" {
			continue
		}
		if kind == "browse" {
			// Workspaces from before the browse label called this pane tui.
			if legacy := openPaneByLabel(panes, "tui").ID; legacy != "" {
				if err := host.RenamePane(legacy, "browse"); err != nil {
					return added, err
				}
				continue
			}
		}
		id, err := openAddTool(host, workspace, cwd, layout, kind, panes)
		if err != nil {
			return added, fmt.Errorf("add %s pane: %w", kind, err)
		}
		added = append(added, id)
	}
	return added, nil
}

func openLayoutDescription(layout string, panes []openPaneInfo) string {
	if layout == "" {
		return "unknown"
	}
	detail := strconv.Itoa(len(panes)) + " panes"
	if layout == "partial" {
		var missing []string
		for _, label := range []string{"agent", "status"} {
			if openPaneByLabel(panes, label).ID == "" {
				missing = append(missing, label)
			}
		}
		if len(missing) > 0 {
			// An open that failed part-way, often with openpty, leaves this.
			detail += "; missing " + strings.Join(missing, ", ")
		}
	}
	return layout + " (" + detail + ")"
}
