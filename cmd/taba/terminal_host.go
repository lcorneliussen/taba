package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"taba.sh/taba/internal/prompts"
	"taba.sh/taba/internal/retry"
)

// terminalHost is the multiplexer that holds collection workspaces. The
// layout, repair and trim rules above it are the same for every host.
type terminalHost interface {
	Kind() string
	Session() string
	Available() error
	Running() bool
	EnsureRunning() error
	AttachHint() string

	Workspaces() ([]openWorkspaceInfo, error)
	CreateWorkspace(label, cwd string, env []string) (workspace, pane string, err error)
	FocusWorkspace(workspace string) error

	Tabs(workspace string) ([]openTabInfo, error)
	CreateTab(workspace, cwd, label string, env []string) (pane string, err error)
	RenameTab(tab, label string) error

	Panes(workspace string) ([]openPaneInfo, error)
	PaneTab(pane string) string
	Split(pane, direction, ratio, cwd string) (string, error)
	RenamePane(pane, label string) error
	MovePaneToNewTab(workspace, pane, label string) error
	MovePane(pane, tab, direction, target, ratio string) error
	ClosePane(pane string) error
	Run(pane, command string) error
	// Foreground is the argv of the pane's foreground process group leader.
	Foreground(pane string) ([]string, error)
	ProcessIDs(pane string) (map[int]bool, error)
	Layouts(workspace string) ([]openTabLayout, error)
	SessionWidth() int

	StartAgent(name, kind, pane string, args []string) error
	PromptAgent(pane string, text prompts.Text) error
	SubmitFirstPrompt(pane string, text prompts.Text) error

	Here() (openHostLocation, bool)
	SessionPanes() []openSessionPanes
}

// agentRegistry keeps agents by name, as herdr does, so a start it reported
// as unfinished can be checked against the agent it registered.
type agentRegistry interface {
	terminalHost
	AgentIdentity(name, pane string) (byName, byPane openAgentInfo, err error)
}

type openHostLocation struct {
	Session, Workspace, Pane string
}

type openSessionPanes struct {
	Session string
	Panes   []openPaneInfo
}

type openPaneRect struct {
	ID   string
	X, Y int
}

type openTabLayout struct {
	TabID string
	Panes []openPaneRect
}

func newTerminalHost(kind, session string) terminalHost {
	return &herdrHost{session: session}
}

func openHostFor(opt openOptions) terminalHost {
	return newTerminalHost(opt.Multiplexer, opt.Session)
}

type herdrHost struct {
	session string
	// call replaces the herdr command in tests.
	call func(string, ...string) (openHerdrResponse, error)
}

func (h *herdrHost) herdr(args ...string) (openHerdrResponse, error) {
	if h.call != nil {
		return h.call(h.session, args...)
	}
	return openHerdr(h.session, args...)
}

func (h *herdrHost) Kind() string    { return "herdr" }
func (h *herdrHost) Session() string { return h.session }

func (h *herdrHost) Available() error {
	_, err := exec.LookPath("herdr")
	return err
}

func (h *herdrHost) Running() bool {
	_, err := h.herdr("workspace", "list")
	return err == nil
}

func (h *herdrHost) EnsureRunning() error {
	if h.Running() {
		return nil
	}
	cmd := exec.Command("herdr", "--session", h.session, "server")
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
	if err := retry.Poll(context.Background(), retry.Policy{Every: 250 * time.Millisecond, Attempts: 60}, func(context.Context) (bool, error) {
		return h.Running(), nil
	}); err != nil {
		return fmt.Errorf("herdr session %q did not start", h.session)
	}
	return nil
}

func (h *herdrHost) AttachHint() string { return "herdr --session " + h.session }

func (h *herdrHost) Workspaces() ([]openWorkspaceInfo, error) {
	result, err := h.herdr("workspace", "list")
	return result.Result.Workspaces, err
}

func (h *herdrHost) CreateWorkspace(label, cwd string, env []string) (string, string, error) {
	args := []string{"workspace", "create", "--cwd", cwd, "--label", label, "--no-focus"}
	for _, setting := range env {
		args = append(args, "--env", setting)
	}
	result, err := h.herdr(args...)
	workspace := result.Result.WorkspaceID
	if workspace == "" {
		workspace = result.Result.Workspace.ID
	}
	return workspace, herdrRootPane(result), err
}

func (h *herdrHost) FocusWorkspace(workspace string) error {
	_, err := h.herdr("workspace", "focus", workspace)
	return err
}

func (h *herdrHost) Tabs(workspace string) ([]openTabInfo, error) {
	result, err := h.herdr("tab", "list", "--workspace", workspace)
	return result.Result.Tabs, err
}

func (h *herdrHost) CreateTab(workspace, cwd, label string, env []string) (string, error) {
	args := []string{"tab", "create", "--workspace", workspace, "--cwd", cwd, "--label", label, "--no-focus"}
	for _, setting := range env {
		args = append(args, "--env", setting)
	}
	result, err := h.herdr(args...)
	if err != nil {
		return "", err
	}
	if pane := herdrRootPane(result); pane != "" {
		return pane, nil
	}
	return openFirstPane(result), nil
}

func (h *herdrHost) RenameTab(tab, label string) error {
	_, err := h.herdr("tab", "rename", tab, label)
	return err
}

func (h *herdrHost) Panes(workspace string) ([]openPaneInfo, error) {
	result, err := h.herdr("pane", "list", "--workspace", workspace)
	return result.Result.Panes, err
}

func (h *herdrHost) PaneTab(pane string) string {
	result, err := h.herdr("pane", "get", pane)
	if err != nil {
		return ""
	}
	return result.Result.Pane.TabID
}

func (h *herdrHost) Split(pane, direction, ratio, cwd string) (string, error) {
	result, err := h.herdr("pane", "split", pane, "--direction", direction, "--ratio", ratio, "--cwd", cwd, "--no-focus")
	if err != nil {
		return "", err
	}
	id := openFirstPane(result)
	if id == "" {
		return "", fmt.Errorf("herdr did not return a pane id after splitting %s", pane)
	}
	return id, nil
}

func (h *herdrHost) RenamePane(pane, label string) error {
	_, err := h.herdr("pane", "rename", pane, label)
	return err
}

func (h *herdrHost) MovePaneToNewTab(workspace, pane, label string) error {
	_, err := h.herdr("pane", "move", pane, "--new-tab", "--workspace", workspace, "--label", label, "--no-focus")
	return err
}

func (h *herdrHost) MovePane(pane, tab, direction, target, ratio string) error {
	_, err := h.herdr("pane", "move", pane, "--tab", tab, "--split", direction, "--target-pane", target, "--ratio", ratio, "--no-focus")
	return err
}

func (h *herdrHost) ClosePane(pane string) error {
	_, err := h.herdr("pane", "close", pane)
	return err
}

func (h *herdrHost) Run(pane, command string) error {
	_, err := h.herdr("pane", "run", pane, command)
	return err
}

func (h *herdrHost) Foreground(pane string) ([]string, error) {
	result, err := h.herdr("pane", "process-info", "--pane", pane)
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

func (h *herdrHost) ProcessIDs(pane string) (map[int]bool, error) {
	result, err := h.herdr("pane", "process-info", "--pane", pane)
	if err != nil {
		return nil, err
	}
	pids := map[int]bool{}
	for _, process := range result.Result.ProcessInfo.Processes {
		pids[process.PID] = true
	}
	return pids, nil
}

func (h *herdrHost) Layouts(string) ([]openTabLayout, error) {
	result, err := h.herdr("api", "snapshot")
	if err != nil {
		return nil, err
	}
	var layouts []openTabLayout
	for _, layout := range result.Result.Snapshot.Layouts {
		tab := openTabLayout{TabID: layout.TabID}
		for _, pane := range layout.Panes {
			tab.Panes = append(tab.Panes, openPaneRect{ID: pane.ID, X: pane.Rect.X, Y: pane.Rect.Y})
		}
		layouts = append(layouts, tab)
	}
	return layouts, nil
}

func (h *herdrHost) SessionWidth() int {
	result, err := h.herdr("api", "snapshot")
	if err != nil {
		return 0
	}
	width := 0
	for _, layout := range result.Result.Snapshot.Layouts {
		width = max(width, layout.Area.Width)
	}
	return width
}

func (h *herdrHost) StartAgent(name, kind, pane string, args []string) error {
	command := []string{"agent", "start", name, "--kind", kind, "--pane", pane}
	if len(args) > 0 {
		command = append(command, "--")
		command = append(command, args...)
	}
	_, err := h.herdr(command...)
	return err
}

func (h *herdrHost) PromptAgent(pane string, text prompts.Text) error {
	_, err := h.herdr("agent", "prompt", pane, text.String())
	return err
}

func (h *herdrHost) SubmitFirstPrompt(pane string, text prompts.Text) error {
	if _, err := h.herdr("agent", "prompt", pane, text.String(), "--wait", "--until", "working", "--timeout", "20000"); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "agent_prompt_stalled") {
		return fmt.Errorf("agent started, but first prompt was not taken: %w", err)
	}
	if _, err := h.herdr("agent", "send-keys", pane, "enter"); err != nil {
		return fmt.Errorf("first prompt was not taken: %w", err)
	}
	if _, err := h.herdr("agent", "wait", pane, "--until", "working", "--timeout", "10000"); err != nil {
		return fmt.Errorf("first prompt was not taken: %w", err)
	}
	return nil
}

func (h *herdrHost) AgentIdentity(name, pane string) (openAgentInfo, openAgentInfo, error) {
	byName, err := h.herdr("agent", "get", name)
	if err != nil {
		return openAgentInfo{}, openAgentInfo{}, fmt.Errorf("live startup identity lookup failed: %v", err)
	}
	byPane, err := h.herdr("agent", "get", pane)
	if err != nil {
		return openAgentInfo{}, openAgentInfo{}, fmt.Errorf("live startup pane lookup failed: %v", err)
	}
	return byName.Result.Agent, byPane.Result.Agent, nil
}

func (h *herdrHost) Here() (openHostLocation, bool) {
	if os.Getenv("HERDR_ENV") != "1" {
		return openHostLocation{}, false
	}
	return openHostLocation{Session: os.Getenv("HERDR_SESSION"), Workspace: os.Getenv("HERDR_WORKSPACE_ID"), Pane: os.Getenv("HERDR_PANE_ID")}, true
}

func (h *herdrHost) SessionPanes() []openSessionPanes {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "herdr", "session", "list").Output()
	if err != nil {
		return nil
	}
	var sessions []openSessionPanes
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "running" {
			continue
		}
		panes, err := openHerdr(fields[0], "pane", "list")
		if err != nil {
			continue
		}
		sessions = append(sessions, openSessionPanes{Session: fields[0], Panes: panes.Result.Panes})
	}
	return sessions
}

func herdrRootPane(result openHerdrResponse) string {
	if result.Result.RootPane.ID != "" {
		return result.Result.RootPane.ID
	}
	if result.Result.Pane.ID != "" {
		return result.Result.Pane.ID
	}
	return result.Result.PaneID
}
