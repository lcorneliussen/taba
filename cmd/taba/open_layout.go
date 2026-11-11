package main

import (
	"fmt"
	"strconv"
	"strings"
)

func openSplit(session, pane, direction, ratio, cwd string) (string, error) {
	result, err := openHerdr(session, "pane", "split", pane, "--direction", direction, "--ratio", ratio, "--cwd", cwd, "--no-focus")
	if err != nil {
		return "", err
	}
	id := openFirstPane(result)
	if id == "" {
		return "", fmt.Errorf("herdr did not return a pane id after splitting %s", pane)
	}
	return id, nil
}

func openRenamePane(session, pane, label string) error {
	_, err := openHerdr(session, "pane", "rename", pane, label)
	return err
}

// openLayoutState classifies a workspace by its agent and status panes. The
// browse, review and shell tool panes are optional: a missing one is not damage, but
// one that is present has to sit where the layout puts it.
func openLayoutState(session, workspace string) (string, []openPaneInfo, error) {
	panes, err := openPanes(session, workspace)
	if err != nil {
		return "", nil, err
	}
	tabs, err := openTabs(session, workspace)
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
			stacked, err := openStacked(session, pair[0], pair[1])
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
	stacked, err := openStacked(session, agent.ID, status.ID)
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
	if openSameColumn(session, agent.ID, status.ID) {
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
		stacked, err := openStacked(session, pair[0], pair[1])
		if err != nil {
			return "", nil, err
		}
		if !stacked {
			return "partial", panes, nil
		}
	}
	if shell.ID == "" || openSameColumn(session, agent.ID, shell.ID) {
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

func openApplyWide(session, agent, cwd string) error {
	if err := openRenamePane(session, agent, "agent"); err != nil {
		return err
	}
	status, err := openSplit(session, agent, "right", "0.40", cwd)
	if err != nil {
		return err
	}
	return openRenamePane(session, status, "status")
}

func openApplyNarrow(session, agent, cwd string) error {
	if tab := openPaneTab(session, agent); tab != "" {
		if _, err := openHerdr(session, "tab", "rename", tab, "main"); err != nil {
			return err
		}
	}
	if err := openRenamePane(session, agent, "agent"); err != nil {
		return err
	}
	status, err := openSplit(session, agent, "down", "0.65", cwd)
	if err != nil {
		return err
	}
	return openRenamePane(session, status, "status")
}

// openMoveBelow puts pane under target in target's tab.
func openMoveBelow(session, workspace, pane, target, ratio string) error {
	return openMoveBeside(session, workspace, pane, target, "down", ratio)
}

// openMoveBeside puts pane next to target in target's tab. herdr ignores a
// move within one tab, so a pane already there leaves through a temporary tab,
// which keeps its process alive and closes once the pane moves back.
func openMoveBeside(session, workspace, pane, target, direction, ratio string) error {
	tab := openPaneTab(session, target)
	if tab == "" {
		return fmt.Errorf("cannot find the tab of pane %s", target)
	}
	if openPaneTab(session, pane) == tab {
		if _, err := openHerdr(session, "pane", "move", pane, "--new-tab", "--workspace", workspace, "--label", "restack-tmp", "--no-focus"); err != nil {
			return err
		}
	}
	_, err := openHerdr(session, "pane", "move", pane, "--tab", tab, "--split", direction, "--target-pane", target, "--ratio", ratio, "--no-focus")
	return err
}

// openRestackNarrow repairs a narrow workspace whose tabs hold the right panes
// side by side instead of stacked.
func openRestackNarrow(session, workspace string, panes []openPaneInfo) error {
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
		stacked, err := openStacked(session, upper, lower)
		if err != nil {
			return err
		}
		if stacked {
			continue
		}
		if err := openMoveBelow(session, workspace, lower, upper, stack[2]); err != nil {
			return err
		}
	}
	return nil
}

// openSwitchNarrow moves the panes a workspace has into the narrow layout;
// it creates only a missing status pane, never a tool pane.
func openSwitchNarrow(session, workspace, agent, cwd string, panes []openPaneInfo) error {
	if err := openRenamePane(session, agent, "agent"); err != nil {
		return err
	}
	if tab := openPaneTab(session, agent); tab != "" {
		if _, err := openHerdr(session, "tab", "rename", tab, "main"); err != nil {
			return err
		}
	}
	tools := openToolPanes(panes)
	for i, pane := range tools {
		if i == 0 {
			if _, err := openHerdr(session, "pane", "move", pane.ID, "--new-tab", "--workspace", workspace, "--label", "tools", "--no-focus"); err != nil {
				return err
			}
		} else {
			ratio := "0.50"
			if pane.Label == "shell" {
				ratio = "0.80"
			}
			if err := openMoveBelow(session, workspace, pane.ID, tools[i-1].ID, ratio); err != nil {
				return err
			}
		}
	}
	if status := openPaneByLabel(panes, "status").ID; status != "" {
		return openMoveBelow(session, workspace, status, agent, "0.65")
	}
	status, err := openSplit(session, agent, "down", "0.65", cwd)
	if err != nil {
		return err
	}
	return openRenamePane(session, status, "status")
}

// openSwitchWide moves the panes a workspace has into the wide layout; it
// creates only a missing status pane, never a tool pane.
func openSwitchWide(session, workspace, agent, cwd string, panes []openPaneInfo) error {
	if err := openRenamePane(session, agent, "agent"); err != nil {
		return err
	}
	main := openPaneTab(session, agent)
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
		if err := openMoveBeside(session, workspace, top, agent, "right", "0.40"); err != nil {
			return err
		}
	}
	bottom := top
	if browse != "" && review != "" {
		if err := openMoveBelow(session, workspace, review, browse, "0.50"); err != nil {
			return err
		}
		bottom = review
	}
	switch {
	case status != "" && top != "":
		if err := openMoveBelow(session, workspace, status, bottom, "0.65"); err != nil {
			return err
		}
	case status != "":
		if err := openMoveBeside(session, workspace, status, agent, "right", "0.40"); err != nil {
			return err
		}
	default:
		base, direction, ratio := agent, "right", "0.40"
		if top != "" {
			base, direction, ratio = bottom, "down", "0.65"
		}
		created, err := openSplit(session, base, direction, ratio, cwd)
		if err != nil {
			return err
		}
		if err := openRenamePane(session, created, "status"); err != nil {
			return err
		}
	}
	if shell != "" {
		if err := openMoveBelow(session, workspace, shell, agent, "0.80"); err != nil {
			return err
		}
	}
	return nil
}

func openEnsureLayout(session, workspace, cwd, desired string) (string, error) {
	current, panes, err := openLayoutState(session, workspace)
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
			err = openApplyNarrow(session, agent, cwd)
		} else {
			err = openApplyWide(session, agent, cwd)
		}
		return "built", err
	}
	if current == "unstacked" && desired == "narrow" {
		return "restacked", openRestackNarrow(session, workspace, panes)
	}
	if desired == "narrow" {
		err = openSwitchNarrow(session, workspace, agent, cwd, panes)
	} else {
		err = openSwitchWide(session, workspace, agent, cwd, panes)
	}
	return "switched", err
}

// openAddTool adds one browse, review or shell pane where the layout puts it: on the
// tools tab in narrow, beside the agent in wide. A pane that has to sit above
// an existing one is split off it, and the existing pane moves below.
func openAddTool(session, workspace, cwd, layout, kind string, panes []openPaneInfo) (string, error) {
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
			id, err = openSplit(session, upper, "down", ratio, cwd)
		} else {
			id, err = openSplit(session, lower, "right", "0.50", cwd)
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
				if err = openMoveBelow(session, workspace, pane.ID, previous, lowerRatio); err != nil {
					break
				}
				previous = pane.ID
			}
		}
	case layout == "narrow":
		var created openHerdrResponse
		created, err = openHerdr(session, "tab", "create", "--workspace", workspace, "--cwd", cwd, "--label", "tools", "--no-focus")
		if id = openFirstPane(created); err == nil && id == "" {
			err = fmt.Errorf("herdr did not return the tools tab's pane id")
		}
	case agent == "":
		return "", fmt.Errorf("workspace %s has no agent pane to add %s beside", workspace, kind)
	case kind == "review" && browse != "":
		if id, err = openSplit(session, browse, "down", "0.50", cwd); err == nil && status != "" {
			err = openMoveBelow(session, workspace, status, id, "0.65")
		}
	case kind == "browse" && review != "":
		if id, err = openSplit(session, review, "right", "0.50", cwd); err == nil {
			err = openMoveBelow(session, workspace, review, id, "0.50")
			if err == nil && status != "" {
				err = openMoveBelow(session, workspace, status, review, "0.65")
			}
		}
	case (kind == "browse" || kind == "review") && status != "":
		if id, err = openSplit(session, status, "right", "0.50", cwd); err == nil {
			err = openMoveBelow(session, workspace, status, id, "0.65")
		}
	case kind == "browse" || kind == "review":
		id, err = openSplit(session, agent, "right", "0.40", cwd)
	default:
		id, err = openSplit(session, agent, "down", "0.80", cwd)
	}
	if err != nil {
		return "", err
	}
	return id, openRenamePane(session, id, kind)
}

// openEnsureTools adds each wanted tool pane the workspace does not have yet.
func openEnsureTools(session, workspace, cwd, layout string, tools []string) ([]string, error) {
	var added []string
	for _, kind := range tools {
		panes, err := openPanes(session, workspace)
		if err != nil {
			return added, err
		}
		if openPaneByLabel(panes, kind).ID != "" {
			continue
		}
		if kind == "browse" {
			// Workspaces from before the browse label called this pane tui.
			if legacy := openPaneByLabel(panes, "tui").ID; legacy != "" {
				if err := openRenamePane(session, legacy, "browse"); err != nil {
					return added, err
				}
				continue
			}
		}
		id, err := openAddTool(session, workspace, cwd, layout, kind, panes)
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
