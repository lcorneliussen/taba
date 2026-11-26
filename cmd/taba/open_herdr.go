package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"taba.sh/taba/internal/retry"
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

type openAgentInfo struct {
	Name             string `json:"name"`
	Kind             string `json:"agent"`
	Pane             string `json:"pane_id"`
	Terminal         string `json:"terminal_id"`
	Workspace        string `json:"workspace_id"`
	Cwd              string `json:"cwd"`
	ForegroundCwd    string `json:"foreground_cwd"`
	Status           string `json:"agent_status"`
	InteractiveReady bool   `json:"interactive_ready"`
	LaunchPending    bool   `json:"launch_pending"`
}

type openHerdrError struct {
	Code  string
	Cause error
}

func (e *openHerdrError) Error() string { return e.Cause.Error() }
func (e *openHerdrError) Unwrap() error { return e.Cause }

type openHerdrResponse struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
	Result struct {
		Agent       openAgentInfo       `json:"agent"`
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
		// Server error codes are structured protocol data, never inferred from
		// arbitrary terminal text or a diagnostic mentioning a known code.
		start := strings.IndexByte(string(out), '{')
		if start >= 0 {
			var parsed openHerdrResponse
			if json.Unmarshal(out[start:], &parsed) == nil {
				result = parsed
			}
		}
		return result, &openHerdrError{Code: result.Error.Code, Cause: fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))}
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

var openAgentEnvironment = regexp.MustCompile(`^(CLAUDE[A-Z0-9_]*|ANTHROPIC[A-Z0-9_]*|AI_AGENT|CODEX[A-Z0-9_]*|CURSOR[A-Z0-9_]*|GEMINI[A-Z0-9_]*)=`)

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

func openSameColumn(host terminalHost, workspace, left, right string) bool {
	layouts, err := host.Layouts(workspace)
	if err != nil {
		return false
	}
	var xleft, xright int
	foundLeft, foundRight := false, false
	for _, layout := range layouts {
		for _, pane := range layout.Panes {
			if pane.ID == left {
				xleft, foundLeft = pane.X, true
			}
			if pane.ID == right {
				xright, foundRight = pane.X, true
			}
		}
	}
	return foundLeft && foundRight && xleft == xright
}

// openStacked reports whether lower sits below upper in the same tab and column.
func openStacked(host terminalHost, workspace, upper, lower string) (bool, error) {
	layouts, err := host.Layouts(workspace)
	if err != nil {
		return false, err
	}
	for _, layout := range layouts {
		var xupper, yupper, xlower, ylower int
		foundUpper, foundLower := false, false
		for _, pane := range layout.Panes {
			if pane.ID == upper {
				xupper, yupper, foundUpper = pane.X, pane.Y, true
			}
			if pane.ID == lower {
				xlower, ylower, foundLower = pane.X, pane.Y, true
			}
		}
		if foundUpper && foundLower {
			return xupper == xlower && yupper < ylower, nil
		}
		if foundUpper || foundLower {
			return false, nil
		}
	}
	return false, fmt.Errorf("%s snapshot has no layout for panes %s and %s", host.Kind(), upper, lower)
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

// openSettlePause, when set, replaces the second between settle samples;
// tests count it instead of sleeping.
var openSettlePause func(context.Context) error

func openPaneIdle(host terminalHost, pane string) bool {
	argv, err := host.Foreground(pane)
	return err == nil && openShellCommand(argv)
}

// openSettled reports whether idle held for three samples in a row, a second
// apart, within ten seconds.
func openSettled(idle func() bool) bool {
	steady := 0
	return retry.Poll(context.Background(), retry.Policy{Every: time.Second, Within: 10 * time.Second, Pause: openSettlePause}, func(context.Context) (bool, error) {
		if idle() {
			steady++
		} else {
			steady = 0
		}
		return steady >= 3, nil
	}) == nil
}

func openWaitIdle(host terminalHost, pane string) bool {
	return openSettled(func() bool { return openPaneIdle(host, pane) })
}

func openWaitWorkspaceIdle(host terminalHost, workspace string) {
	openSettled(func() bool {
		panes, err := host.Panes(workspace)
		if err != nil || len(panes) == 0 {
			return false
		}
		for _, pane := range panes {
			if !openPaneIdle(host, pane.ID) {
				return false
			}
		}
		return true
	})
}
