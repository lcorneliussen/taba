package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type openWorkspaceInfo struct {
	Label string `json:"label"`
	ID    string `json:"workspace_id"`
}

type openPaneInfo struct {
	Label       string `json:"label"`
	ID          string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	Agent       string `json:"agent"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
}

type openTabInfo struct {
	Label string `json:"label"`
	ID    string `json:"tab_id"`
}

type openHerdrResponse struct {
	Result struct {
		Workspaces  []openWorkspaceInfo `json:"workspaces"`
		Panes       []openPaneInfo      `json:"panes"`
		Tabs        []openTabInfo       `json:"tabs"`
		WorkspaceID string              `json:"workspace_id"`
		Workspace   struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
		PaneID   string `json:"pane_id"`
		RootPane struct {
			ID string `json:"pane_id"`
		} `json:"root_pane"`
		Pane struct {
			ID    string `json:"pane_id"`
			TabID string `json:"tab_id"`
		} `json:"pane"`
		ProcessInfo struct {
			Group     int `json:"foreground_process_group_id"`
			Processes []struct {
				PID     int      `json:"pid"`
				Argv    []string `json:"argv"`
				Cmdline string   `json:"cmdline"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
		Snapshot struct {
			Layouts []struct {
				Area struct {
					Width int `json:"width"`
				} `json:"area"`
				TabID string `json:"tab_id"`
				Panes []struct {
					ID   string `json:"pane_id"`
					Rect struct {
						X int `json:"x"`
						Y int `json:"y"`
					} `json:"rect"`
				} `json:"panes"`
			} `json:"layouts"`
		} `json:"snapshot"`
	} `json:"result"`
}

func openHerdr(session string, args ...string) (openHerdrResponse, error) {
	var result openHerdrResponse
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "herdr", append([]string{"--session", session}, args...)...)
	out, err := command.CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	start := strings.IndexByte(string(out), '{')
	if start < 0 {
		return result, nil
	}
	if err := json.Unmarshal(out[start:], &result); err != nil {
		return result, fmt.Errorf("herdr %s returned invalid JSON: %w", strings.Join(args, " "), err)
	}
	return result, nil
}

func openSessionRunning(session string) bool {
	_, err := openHerdr(session, "workspace", "list")
	return err == nil
}

var openAgentEnvironment = regexp.MustCompile(`^(CLAUDE[A-Z0-9_]*|ANTHROPIC[A-Z0-9_]*|AI_AGENT|CODEX[A-Z0-9_]*|CURSOR[A-Z0-9_]*|GEMINI[A-Z0-9_]*)=`)

func openEnsureSession(session string) error {
	if openSessionRunning(session) {
		return nil
	}
	cmd := exec.Command("herdr", "--session", session, "server")
	cmd.Env = []string{}
	for _, setting := range os.Environ() {
		if !openAgentEnvironment.MatchString(setting) {
			cmd.Env = append(cmd.Env, setting)
		}
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, devNull, devNull
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	for i := 0; i < 60; i++ {
		if openSessionRunning(session) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("herdr session %q did not start", session)
}

func openWorkspaces(session string) ([]openWorkspaceInfo, error) {
	result, err := openHerdr(session, "workspace", "list")
	return result.Result.Workspaces, err
}

func openPanes(session, workspace string) ([]openPaneInfo, error) {
	result, err := openHerdr(session, "pane", "list", "--workspace", workspace)
	return result.Result.Panes, err
}

func openTabs(session, workspace string) ([]openTabInfo, error) {
	result, err := openHerdr(session, "tab", "list", "--workspace", workspace)
	return result.Result.Tabs, err
}

func openPaneByLabel(panes []openPaneInfo, label string) openPaneInfo {
	for _, pane := range panes {
		if pane.Label == label {
			return pane
		}
	}
	return openPaneInfo{}
}

func openTabByLabel(tabs []openTabInfo, label string) openTabInfo {
	for _, tab := range tabs {
		if tab.Label == label {
			return tab
		}
	}
	return openTabInfo{}
}

func openSessionWidth(session string) int {
	result, err := openHerdr(session, "api", "snapshot")
	if err != nil {
		return 0
	}
	width := 0
	for _, layout := range result.Result.Snapshot.Layouts {
		if layout.Area.Width > width {
			width = layout.Area.Width
		}
	}
	return width
}

func openSameColumn(session, left, right string) bool {
	result, err := openHerdr(session, "api", "snapshot")
	if err != nil {
		return false
	}
	var xleft, xright int
	foundLeft, foundRight := false, false
	for _, layout := range result.Result.Snapshot.Layouts {
		for _, pane := range layout.Panes {
			if pane.ID == left {
				xleft, foundLeft = pane.Rect.X, true
			}
			if pane.ID == right {
				xright, foundRight = pane.Rect.X, true
			}
		}
	}
	return foundLeft && foundRight && xleft == xright
}

// openStacked reports whether lower sits below upper in the same tab and column.
func openStacked(session, upper, lower string) (bool, error) {
	result, err := openHerdr(session, "api", "snapshot")
	if err != nil {
		return false, err
	}
	for _, layout := range result.Result.Snapshot.Layouts {
		var xupper, yupper, xlower, ylower int
		foundUpper, foundLower := false, false
		for _, pane := range layout.Panes {
			if pane.ID == upper {
				xupper, yupper, foundUpper = pane.Rect.X, pane.Rect.Y, true
			}
			if pane.ID == lower {
				xlower, ylower, foundLower = pane.Rect.X, pane.Rect.Y, true
			}
		}
		if foundUpper && foundLower {
			return xupper == xlower && yupper < ylower, nil
		}
		if foundUpper || foundLower {
			return false, nil
		}
	}
	return false, fmt.Errorf("herdr snapshot has no layout for panes %s and %s", upper, lower)
}

func openFirstPane(result openHerdrResponse) string {
	if result.Result.PaneID != "" {
		return result.Result.PaneID
	}
	if result.Result.RootPane.ID != "" {
		return result.Result.RootPane.ID
	}
	if result.Result.Pane.ID != "" {
		return result.Result.Pane.ID
	}
	if len(result.Result.Panes) > 0 {
		return result.Result.Panes[0].ID
	}
	return ""
}

func openPaneTab(session, pane string) string {
	result, err := openHerdr(session, "pane", "get", pane)
	if err != nil {
		return ""
	}
	return result.Result.Pane.TabID
}

func openPaneCommand(session, pane string) ([]string, error) {
	result, err := openHerdr(session, "pane", "process-info", "--pane", pane)
	if err != nil {
		return nil, err
	}
	for _, process := range result.Result.ProcessInfo.Processes {
		if process.PID == result.Result.ProcessInfo.Group {
			if len(process.Argv) > 0 {
				return process.Argv, nil
			}
			return strings.Fields(process.Cmdline), nil
		}
	}
	return nil, fmt.Errorf("foreground process unavailable for pane %s", pane)
}

func openShellCommand(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	name := filepath.Base(strings.TrimPrefix(argv[0], "-"))
	switch name {
	case "zsh", "bash", "fish", "sh", "nu", "nushell", "dash", "ksh":
		return len(argv) == 1 || len(argv) == 2 && strings.HasPrefix(argv[1], "-")
	}
	return false
}

func openWaitIdle(session, pane string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	steady := 0
	for {
		argv, err := openPaneCommand(session, pane)
		if err == nil && openShellCommand(argv) {
			steady++
			if budget == 0 || steady >= 3 {
				return true
			}
		} else {
			steady = 0
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}

func openWaitWorkspaceIdle(session, workspace string) {
	deadline := time.Now().Add(10 * time.Second)
	steady := 0
	for time.Now().Before(deadline) {
		panes, err := openPanes(session, workspace)
		all := err == nil && len(panes) > 0
		for _, pane := range panes {
			argv, err := openPaneCommand(session, pane.ID)
			if err != nil || !openShellCommand(argv) {
				all = false
				break
			}
		}
		if all {
			steady++
		} else {
			steady = 0
		}
		if steady >= 3 {
			return
		}
		time.Sleep(time.Second)
	}
}
