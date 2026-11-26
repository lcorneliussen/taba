package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"taba.sh/taba/internal/prompts"
	"taba.sh/taba/internal/wtc"
)

// rexHost keeps each collection in a Rex session labelled
// "<session>/<collection>"; its Rex windows are tabs and its blocks panes.
type rexHost struct {
	session string
	// ctx bounds every rex call; nil is no bound beyond each call's own.
	ctx context.Context
}

func (h *rexHost) Kind() string    { return "rex" }
func (h *rexHost) Session() string { return h.session }

func rexCommand() (string, error) {
	if path, err := exec.LookPath("rex"); err == nil {
		return path, nil
	}
	if dir := os.Getenv("REX_BIN_DIR"); dir != "" {
		path := filepath.Join(dir, "rex")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("rex not found on PATH")
}

func (h *rexHost) rex(args ...string) ([]byte, error) {
	binary, err := rexCommand()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(cmp.Or(h.ctx, context.Background()), 35*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, append([]string{"--autostart=false"}, args...)...)
	command.Env = withoutCallerBlock(os.Environ())
	var stderr strings.Builder
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		return out, fmt.Errorf("rex %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()+string(out)))
	}
	return out, nil
}

// withoutCallerBlock drops REX_BLOCK, which would make rex resolve names in
// the caller's own session rather than the session taba addresses.
func withoutCallerBlock(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(setting string) bool { return strings.HasPrefix(setting, "REX_BLOCK=") })
}

// rexUnreachable: rex exits 4 when no server answers.
func rexUnreachable(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 4
}

// rexGone: rex exits 3 for a block that no longer exists.
func rexGone(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 3
}

func (h *rexHost) rexJSON(result any, args ...string) error {
	out, err := h.rex(args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("rex %s returned invalid JSON: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (h *rexHost) blockCall(result any, pane, method string) error {
	return h.rexJSON(result, "block", "call", "-b", pane, method)
}

func (h *rexHost) Available() error {
	_, err := rexCommand()
	return err
}

func (h *rexHost) Running() bool {
	_, err := h.rex("ls", "--json")
	return err == nil
}

func (h *rexHost) EnsureRunning() error {
	if h.Running() {
		return nil
	}
	return errors.New("the Rex server is not running; start Rex first")
}

func (h *rexHost) AttachHint() string {
	return "Rex sessions " + h.session + "/<collection>"
}

type rexSessionInfo struct {
	ID    string `json:"session_id"`
	Label string `json:"label"`
}

func (h *rexHost) sessions() ([]rexSessionInfo, error) {
	var listing struct {
		Sessions []rexSessionInfo `json:"sessions"`
	}
	err := h.rexJSON(&listing, "ls", "--json")
	return listing.Sessions, err
}

func (h *rexHost) Workspaces() ([]openWorkspaceInfo, error) {
	sessions, err := h.sessions()
	if err != nil {
		return nil, err
	}
	workspaces := []openWorkspaceInfo{}
	for _, session := range sessions {
		if name, ok := strings.CutPrefix(session.Label, h.session+"/"); ok && name != "" && !strings.Contains(name, "/") {
			workspaces = append(workspaces, openWorkspaceInfo{Label: name, ID: session.ID})
		}
	}
	return workspaces, nil
}

func rexLoginShell() string {
	if shell := os.Getenv("SHELL"); filepath.IsAbs(shell) {
		return shell
	}
	return "/bin/sh"
}

// rexTerminal: a Rex terminal takes no environment, and env(1) passes its
// arguments on unchanged, the literal values herdr's --env sets.
func rexTerminal(cwd string, env []string) []string {
	args := []string{"-c", cwd, "--json"}
	if len(env) == 0 {
		return args
	}
	args = append(args, "--shell=none", "--", "/usr/bin/env")
	return append(append(args, env...), rexLoginShell(), "-l")
}

func (h *rexHost) collectionEnvironment(cwd string) ([]string, error) {
	target, err := wtc.OpenCollection(cwd)
	if err != nil || target.Collection != cwd {
		return nil, nil //nolint:nilerr // Not a collection: a plain shell.
	}
	return openWorkspaceEnvironment(target, h.session, filepath.Base(cwd))
}

type rexCreated struct {
	SessionID      string   `json:"session_id"`
	BlockIDs       []string `json:"block_ids"`
	InitialWindows []struct {
		BlockIDs []string `json:"block_ids"`
	} `json:"initial_windows"`
}

func (c rexCreated) block() string {
	if len(c.BlockIDs) > 0 {
		return c.BlockIDs[0]
	}
	if len(c.InitialWindows) > 0 && len(c.InitialWindows[0].BlockIDs) > 0 {
		return c.InitialWindows[0].BlockIDs[0]
	}
	return ""
}

// Rex would call a new block "shell", the shell tool pane's label.
const rexUnassignedPane = "pane"

func (h *rexHost) CreateWorkspace(label, cwd string, env []string) (string, string, error) {
	var created rexCreated
	args := append([]string{"new", h.session + "/" + label, "--window", "main"}, rexTerminal(cwd, env)...)
	if err := h.rexJSON(&created, args...); err != nil {
		return "", "", err
	}
	pane := created.block()
	if created.SessionID == "" || pane == "" {
		return created.SessionID, pane, errors.New("rex created a session without a session and block ID")
	}
	return created.SessionID, pane, h.RenamePane(pane, rexUnassignedPane)
}

func (h *rexHost) CloseWorkspace(workspace string) error {
	_, err := h.rex("kill", workspace)
	return err
}

func (h *rexHost) FocusWorkspace(workspace string) error {
	if _, err := h.rex("do", "session.select", "session_id="+workspace); err != nil {
		return fmt.Errorf("the Rex app did not show the session (it needs Remote Control): %w", err)
	}
	return nil
}

type rexLayoutNode struct {
	Rect  []float64 `json:"rect"`
	Block string    `json:"block_id"`
	Split *struct {
		Before *rexLayoutNode `json:"before"`
		After  *rexLayoutNode `json:"after"`
	} `json:"split"`
}

type rexBlock struct {
	ID    string `json:"block_id"`
	Label string `json:"label"`
	Owner string `json:"owner"`
}

type rexWindow struct {
	ID      string `json:"window_id"`
	Label   string `json:"label"`
	Focused string `json:"focused_block_id"`
	Layers  []struct {
		Kind   string         `json:"kind"`
		Layout *rexLayoutNode `json:"layout"`
		Blocks []rexBlock     `json:"blocks"`
	} `json:"layers"`
}

type rexView struct {
	ActiveWindow string      `json:"active_window_id"`
	Windows      []rexWindow `json:"windows"`
}

func (v rexView) hasWindow(id string) bool {
	return slices.ContainsFunc(v.Windows, func(w rexWindow) bool { return w.ID == id })
}

func (h *rexHost) view(workspace string) (rexView, error) {
	var view rexView
	err := h.rexJSON(&view, "session", "inspect", workspace, "--json")
	return view, err
}

func (h *rexHost) Tabs(workspace string) ([]openTabInfo, error) {
	view, err := h.view(workspace)
	if err != nil {
		return nil, err
	}
	tabs := []openTabInfo{}
	for _, window := range view.Windows {
		tabs = append(tabs, openTabInfo{Label: window.Label, ID: window.ID})
	}
	return tabs, nil
}

func (h *rexHost) CreateTab(workspace, cwd, label string, env []string) (string, error) {
	if env == nil {
		var err error
		if env, err = h.collectionEnvironment(cwd); err != nil {
			return "", err
		}
	}
	var created rexCreated
	args := append([]string{"window", "new", label, "-s", workspace, "--focus=false"}, rexTerminal(cwd, env)...)
	if err := h.rexJSON(&created, args...); err != nil {
		return "", err
	}
	pane := created.block()
	if pane == "" {
		return "", errors.New("rex created a window without a block ID")
	}
	return pane, h.RenamePane(pane, rexUnassignedPane)
}

func (h *rexHost) RenameTab(tab, label string) error {
	_, err := h.rex("window", "rename", tab, label)
	return err
}

func (h *rexHost) Panes(workspace string) ([]openPaneInfo, error) {
	panes := []openPaneInfo{}
	err := h.eachLiveBlock(workspace, func(view rexView, window rexWindow, block rexBlock) (bool, error) {
		pane := openPaneInfo{Label: block.Label, ID: block.ID, TabID: window.ID}
		pane.Focused = window.ID == view.ActiveWindow && window.Focused == block.ID && rexShownInApp(block)
		var err error
		if pane.Agent, pane.AgentStatus, err = h.agentState(block.ID); err != nil {
			return false, fmt.Errorf("cannot tell whether pane %s runs an agent: %w", block.ID, err)
		}
		panes = append(panes, pane)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return panes, nil
}

// eachLiveBlock visits the workspace's blocks until visit is done, skipping a
// block that closed after the workspace was inspected.
func (h *rexHost) eachLiveBlock(workspace string, visit func(rexView, rexWindow, rexBlock) (done bool, err error)) error {
	view, err := h.view(workspace)
	if err != nil {
		return err
	}
	for _, window := range view.Windows {
		for _, layer := range window.Layers {
			for _, block := range layer.Blocks {
				done, err := visit(view, window, block)
				if rexGone(err) {
					continue
				}
				if err != nil || done {
					return err
				}
			}
		}
	}
	return nil
}

// rexShownInApp: every Rex window has a focused block, but only a session an
// app has open can hold the person's focus, and the app owns all its blocks.
func rexShownInApp(block rexBlock) bool {
	return block.Owner != ""
}

type rexProcessInfo struct {
	PID        int    `json:"pid"`
	Name       string `json:"name"`
	Argv0      string `json:"argv0"`
	Script     string `json:"script"`
	InvokedVia string `json:"invoked_path"`
}

type rexProcess struct {
	Child      *rexProcessInfo  `json:"child"`
	Foreground *rexProcessInfo  `json:"foreground"`
	Ancestors  []rexProcessInfo `json:"ancestors"`
}

func (h *rexHost) process(pane string) (rexProcess, error) {
	var process rexProcess
	err := h.blockCall(&process, pane, "process")
	return process, err
}

var rexAgentPrograms = map[string]string{"claude": "claude", "codex": "codex", "gemini": "gemini", "opencode": "opencode", "cursor": "cursor-agent", "pi": "pi", "agy": "agy"}

// rexPromptArgumentKinds open an interactive session on a prompt argument.
var rexPromptArgumentKinds = []string{"claude", "codex"}

func rexAgentKind(process *rexProcessInfo) string {
	if process == nil {
		return ""
	}
	for _, name := range []string{process.Argv0, process.Script, process.InvokedVia, process.Name} {
		program := filepath.Base(strings.TrimPrefix(name, "-"))
		for kind, agent := range rexAgentPrograms {
			if program == agent {
				return kind
			}
		}
	}
	switch {
	case strings.Contains(process.Script, "claude-code"):
		return "claude"
	case strings.Contains(process.Script, "codex"):
		return "codex"
	}
	return ""
}

// agentState is "unknown" for an agent that reports no OSC 7501 state.
func (h *rexHost) agentState(pane string) (kind, state string, err error) {
	process, err := h.process(pane)
	if err != nil {
		return "", "", err
	}
	kind = rexAgentKind(process.Foreground)
	if kind == "" {
		return "", "", nil
	}
	var status struct {
		Records []struct {
			State string `json:"state"`
			Owner *struct {
				PID  int    `json:"pid"`
				Name string `json:"name"`
			} `json:"owner"`
		} `json:"records"`
	}
	if err := h.blockCall(&status, pane, "program_status"); err != nil {
		return "", "", err
	}
	for _, record := range status.Records {
		if record.Owner == nil || record.Owner.PID != process.Foreground.PID || record.Owner.Name != process.Foreground.Name {
			continue
		}
		switch record.State {
		case "working", "blocked", "done", "error", "idle":
			return kind, record.State, nil
		}
		return kind, "idle", nil
	}
	return kind, "unknown", nil
}

func (h *rexHost) PaneTab(pane string) string {
	var inspected struct {
		Block struct {
			WindowID string `json:"window_id"`
		} `json:"block"`
	}
	if h.rexJSON(&inspected, "block", "inspect", pane, "--json") != nil {
		return ""
	}
	return inspected.Block.WindowID
}

func (h *rexHost) paneSession(pane string) (string, error) {
	var inspected struct {
		SessionID string `json:"session_id"`
	}
	if err := h.rexJSON(&inspected, "block", "inspect", pane, "--json"); err != nil {
		return "", err
	}
	if inspected.SessionID == "" {
		return "", fmt.Errorf("cannot find the Rex session of pane %s", pane)
	}
	return inspected.SessionID, nil
}

func rexDirection(direction string) string {
	if direction == "down" {
		return "below"
	}
	return direction
}

// rexPercent turns herdr's ratio, the share the existing pane keeps, into the
// percentage Rex gives the new or moved pane.
func rexPercent(ratio string) string {
	value, err := strconv.ParseFloat(ratio, 64)
	if err != nil || value <= 0 || value >= 1 {
		return "50"
	}
	return strconv.Itoa(int(math.Round((1 - value) * 100)))
}

func (h *rexHost) Split(pane, direction, ratio, cwd string) (string, error) {
	env, err := h.collectionEnvironment(cwd)
	if err != nil {
		return "", err
	}
	var created rexCreated
	args := append([]string{"split", "-b", pane, "--split", rexDirection(direction), "--ratio", rexPercent(ratio), "--label", rexUnassignedPane, "--focus=false"}, rexTerminal(cwd, env)...)
	if err := h.rexJSON(&created, args...); err != nil {
		return "", err
	}
	if created.block() == "" {
		return "", fmt.Errorf("rex did not return a block id after splitting %s", pane)
	}
	return created.block(), nil
}

func (h *rexHost) RenamePane(pane, label string) error {
	_, err := h.rex("block", "rename", pane, label)
	return err
}

// MovePaneToNewTab moves pane beside a placeholder in a new tab, then closes
// the placeholder: Rex places a block only next to another one.
func (h *rexHost) MovePaneToNewTab(workspace, pane, label string) error {
	return h.keepingActiveWindow(workspace, func() error {
		var created rexCreated
		if err := h.rexJSON(&created, "window", "new", label, "-s", workspace, "--focus=false", "--json"); err != nil {
			return err
		}
		placeholder := created.block()
		if placeholder == "" {
			return errors.New("rex created a window without a block ID")
		}
		if _, err := h.rex("move", pane, "right", placeholder, "-s", workspace); err != nil {
			return errors.Join(err, h.ClosePane(placeholder))
		}
		return h.ClosePane(placeholder)
	})
}

func (h *rexHost) MovePane(pane, tab, direction, target, ratio string) error {
	session, err := h.paneSession(target)
	if err != nil {
		return err
	}
	return h.keepingActiveWindow(session, func() error {
		_, err := h.rex("move", pane, rexDirection(direction), target, "--ratio", rexPercent(ratio), "-s", session)
		return err
	})
}

// keepingActiveWindow undoes the switch Rex makes when a focused block moves
// to another window, so a repair does not change what an app shows.
func (h *rexHost) keepingActiveWindow(session string, move func() error) error {
	before, err := h.view(session)
	if err != nil {
		return err
	}
	if err := move(); err != nil {
		return err
	}
	after, err := h.view(session)
	if err != nil || after.ActiveWindow == before.ActiveWindow || !after.hasWindow(before.ActiveWindow) {
		return err
	}
	payload, err := json.Marshal(map[string]string{"session_id": session, "window_id": before.ActiveWindow})
	if err != nil {
		return err
	}
	_, err = h.rex("api", "call", "session.focus_window", string(payload))
	return err
}

func (h *rexHost) ClosePane(pane string) error {
	_, err := h.rex("block", "close", pane)
	return err
}

// Run types command in one write, behind a Ctrl-U that clears whatever an
// earlier write left on the prompt line.
func (h *rexHost) Run(pane, command string) error {
	_, err := h.rex("send", "-b", pane, "--", "\x15"+command+"\r")
	return err
}

func (h *rexHost) Foreground(pane string) ([]string, error) {
	process, err := h.process(pane)
	if err != nil {
		return nil, err
	}
	if process.Foreground == nil {
		return nil, fmt.Errorf("foreground process unavailable for pane %s", pane)
	}
	return rexArgv(*process.Foreground), nil
}

// rexArgv takes the arguments Rex withholds from ps.
func rexArgv(process rexProcessInfo) []string {
	name := cmp.Or(process.Argv0, process.Name)
	out, err := exec.Command("ps", "-ww", "-o", "args=", "-p", strconv.Itoa(process.PID)).Output()
	if args, found := strings.CutPrefix(strings.TrimSpace(string(out)), name); err == nil && found {
		return append([]string{name}, strings.Fields(args)...)
	}
	if process.Script != "" {
		return []string{name, process.Script}
	}
	return []string{name}
}

func (h *rexHost) ProcessIDs(pane string) (map[int]bool, error) {
	process, err := h.process(pane)
	if err != nil {
		return nil, err
	}
	pids := map[int]bool{}
	for _, info := range append([]*rexProcessInfo{process.Child, process.Foreground}, rexPointers(process.Ancestors)...) {
		if info != nil {
			pids[info.PID] = true
		}
	}
	return withDescendants(pids)
}

func rexPointers(processes []rexProcessInfo) []*rexProcessInfo {
	pointers := make([]*rexProcessInfo, len(processes))
	for i := range processes {
		pointers[i] = &processes[i]
	}
	return pointers
}

func withDescendants(pids map[int]bool) (map[int]bool, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("cannot list processes: %w", err)
	}
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		var pid, parent int
		if _, err := fmt.Sscan(line, &pid, &parent); err == nil {
			children[parent] = append(children[parent], pid)
		}
	}
	queue := slices.Collect(maps.Keys(pids))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if !pids[child] {
				pids[child] = true
				queue = append(queue, child)
			}
		}
	}
	return pids, nil
}

func (h *rexHost) Layouts(workspace string) ([]openTabLayout, error) {
	view, err := h.view(workspace)
	if err != nil {
		return nil, err
	}
	var layouts []openTabLayout
	for _, window := range view.Windows {
		tab := openTabLayout{TabID: window.ID}
		for _, layer := range window.Layers {
			if layer.Kind == "tiled" {
				tab.Panes = rexRects(layer.Layout, tab.Panes)
			}
		}
		layouts = append(layouts, tab)
	}
	return layouts, nil
}

func rexRects(node *rexLayoutNode, rects []openPaneRect) []openPaneRect {
	if node == nil {
		return rects
	}
	if node.Block != "" && len(node.Rect) >= 2 {
		return append(rects, openPaneRect{ID: node.Block, X: int(math.Round(node.Rect[0] * 10000)), Y: int(math.Round(node.Rect[1] * 10000))})
	}
	if node.Split != nil {
		rects = rexRects(node.Split.Before, rects)
		rects = rexRects(node.Split.After, rects)
	}
	return rects
}

// SessionWidth estimates the caller's window width in columns from its pane's
// columns and the pane's share of the layout; without that share it is the
// pane's own columns.
func (h *rexHost) SessionWidth() int {
	block := os.Getenv("REX_BLOCK")
	if block == "" {
		return 0
	}
	var size struct {
		Columns int `json:"columns"`
	}
	if h.blockCall(&size, block, "size") != nil || size.Columns <= 0 {
		return 0
	}
	session, err := h.paneSession(block)
	if err != nil {
		return 0
	}
	view, err := h.view(session)
	if err != nil {
		return 0
	}
	for _, window := range view.Windows {
		for _, layer := range window.Layers {
			if node := rexFind(layer.Layout, block); node != nil && len(node.Rect) >= 3 && node.Rect[2] > 0 {
				return int(math.Round(float64(size.Columns) / node.Rect[2]))
			}
		}
	}
	return size.Columns
}

func rexFind(node *rexLayoutNode, block string) *rexLayoutNode {
	if node == nil {
		return nil
	}
	if node.Block == block {
		return node
	}
	if node.Split != nil {
		if found := rexFind(node.Split.Before, block); found != nil {
			return found
		}
		return rexFind(node.Split.After, block)
	}
	return nil
}

func (h *rexHost) PromptsAtStart(kind string) bool {
	return slices.Contains(rexPromptArgumentKinds, kind)
}

// StartAgent types the agent's command at the shell prompt, so the shell
// returns when the agent exits and a later open can start it again.
func (h *rexHost) StartAgent(name, kind, pane string, args []string) error {
	program := rexAgentPrograms[kind]
	if program == "" {
		return fmt.Errorf("taba cannot start agent kind %q in Rex; known kinds: %s", kind, strings.Join(slices.Sorted(maps.Keys(rexAgentPrograms)), ", "))
	}
	words := []string{shellQuote(program)}
	for _, arg := range args {
		words = append(words, shellQuote(arg))
	}
	return h.Run(pane, strings.Join(words, " "))
}

func (h *rexHost) PromptAgent(pane string, text prompts.Text) error {
	payload, err := json.Marshal(map[string]any{"block_id": pane, "event": map[string]any{"type": "paste", "payload": map[string]string{"text": text.String()}}})
	if err != nil {
		return err
	}
	session, err := h.paneSession(pane)
	if err != nil {
		return err
	}
	if _, err := h.rex("api", "call", "-s", session, "block.event", string(payload)); err != nil {
		return err
	}
	_, err = h.rex("send-key", "-b", pane, "enter")
	return err
}

func (h *rexHost) SubmitFirstPrompt(string, prompts.Text) error {
	return errors.New("first prompt not sent: Rex cannot tell when this agent takes input; submit the initial task in its pane")
}

func (h *rexHost) Here() (openHostLocation, bool) {
	block := os.Getenv("REX_BLOCK")
	if block == "" {
		return openHostLocation{}, false
	}
	session, err := h.paneSession(block)
	if err != nil {
		return openHostLocation{}, false
	}
	here := openHostLocation{Workspace: session, Pane: block}
	sessions, err := h.sessions()
	if err != nil {
		return here, true
	}
	for _, s := range sessions {
		if i := strings.LastIndex(s.Label, "/"); s.ID == session && i > 0 {
			here.Session = s.Label[:i]
			return here, true
		}
	}
	// A Rex terminal taba did not open is a plain terminal, as one outside herdr.
	return openHostLocation{}, false
}

func (h *rexHost) SessionPanes() []openSessionPanes {
	sessions, err := h.sessions()
	if err != nil {
		return nil
	}
	var all []openSessionPanes
	for _, session := range sessions {
		view, err := h.view(session.ID)
		if err != nil {
			continue
		}
		entry := openSessionPanes{Session: session.Label}
		for _, window := range view.Windows {
			for _, layer := range window.Layers {
				for _, block := range layer.Blocks {
					entry.Panes = append(entry.Panes, openPaneInfo{Label: block.Label, ID: block.ID, TabID: window.ID})
				}
			}
		}
		all = append(all, entry)
	}
	return all
}

func (h *rexHost) VisibleText(pane string) (string, error) {
	var size struct {
		Rows int `json:"rows"`
	}
	if err := h.blockCall(&size, pane, "size"); err != nil {
		return "", err
	}
	out, err := h.rex("capture", "-b", pane)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-size.Rows):], "\n"), nil
}
