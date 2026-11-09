package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/wtc"
)

func openPtyHint() string {
	return branding.Text("the machine is out of pseudo-terminals (kern.tty.ptmx_max); close idle tool panes with {{command}} open --trim --all or stop unused herdr sessions")
}

func openTrimCommand(c *wtc.Context, names []string, opt openOptions, running bool, asJSON *bool) error {
	if !running {
		return fmt.Errorf("herdr session %q is not running", opt.Session)
	}
	items := make([]openItem, 0, len(names))
	failures := 0
	for _, name := range names {
		item := openTrimCollection(c, name, opt)
		if item.Error != "" {
			failures++
		}
		items = append(items, item)
	}
	if *asJSON {
		if err := emit(envelope{OK: failures == 0, Data: items, Summary: fmt.Sprintf("%d collection(s) trimmed, %d failed", len(items), failures)}, true); err != nil {
			return err
		}
	} else {
		for _, item := range items {
			fmt.Printf("==> %s: %s — %s\n", item.Collection, item.Workspace, strings.Join(item.Actions, ", "))
			if item.Error != "" {
				fmt.Fprintln(os.Stderr, "error:", item.Error)
			}
		}
	}
	if failures != 0 {
		return fmt.Errorf("%d collection(s) could not be trimmed", failures)
	}
	return nil
}

// openTrimCollection closes the idle tool panes of one collection's
// workspace that nobody is using. Agent and status panes are never touched.
func openTrimCollection(source *wtc.Context, name string, opt openOptions) openItem {
	item := openItem{Collection: name, Actions: []string{}}
	workspaces, err := openWorkspaces(opt.Session)
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
		item.Actions = append(item.Actions, "no workspace")
		return item
	}
	panes, err := openPanes(opt.Session, item.Workspace)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	socket := browseSocket(source.Workspace, name)
	for _, pane := range panes {
		if pane.Label != "browse" && pane.Label != "shell" && pane.Label != "review" {
			continue
		}
		reason, err := openTrimKeep(opt.Session, pane, socket)
		if err != nil {
			item.Actions = append(item.Actions, pane.Label+" kept (inspection failed)")
			item.Error = strings.TrimPrefix(item.Error+"; "+pane.Label+": "+err.Error(), "; ")
			continue
		}
		if reason != "" {
			item.Actions = append(item.Actions, pane.Label+" kept ("+reason+")")
			continue
		}
		if opt.DryRun {
			item.Actions = append(item.Actions, "would close "+pane.Label)
			continue
		}
		if _, err := openHerdr(opt.Session, "pane", "close", pane.ID); err != nil {
			item.Error = strings.TrimPrefix(item.Error+"; "+pane.Label+": "+err.Error(), "; ")
			continue
		}
		item.Actions = append(item.Actions, pane.Label+" closed")
	}
	if len(item.Actions) == 0 {
		item.Actions = append(item.Actions, "nothing to trim")
	}
	return item
}

// openTrimKeep returns why a tool pane stays open, or "" when it is idle: a
// shell or review pane at its prompt, or a browse Neovim with no unsaved buffers.
// A focused pane, a running command, or a Neovim that does not answer stays.
func openTrimKeep(session string, pane openPaneInfo, socket string) (string, error) {
	if pane.Focused {
		return "focused", nil
	}
	argv, err := openPaneCommand(session, pane.ID)
	if err != nil {
		return "", err
	}
	if openShellCommand(argv) {
		return "", nil
	}
	state, _ := openPaneState(session, pane)
	if pane.Label != "browse" || !openBrowseCommand(argv) {
		return "running " + state, nil
	}
	pid, parent, modified, err := openNvimModified(socket)
	if err != nil {
		return "Neovim did not answer", nil
	}
	// A second wtc browse starts a Neovim without the socket, so the one that
	// answers may belong to another terminal. The socket is served by the
	// --embed child of the Neovim the pane runs.
	pids, err := openPaneProcessIDs(session, pane.ID)
	if err != nil {
		return "", err
	}
	if !pids[pid] && !pids[parent] {
		return "Neovim on the socket runs elsewhere", nil
	}
	if modified > 0 {
		return "unsaved buffers", nil
	}
	return "", nil
}

// openBrowseCommand recognises the browse pane's Neovim, run directly or
// through wtc browse, which leads its process group.
func openBrowseCommand(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if filepath.Base(argv[0]) == "nvim" {
		return true
	}
	name := strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
	return (name == "wtc" || name == "taba") && len(argv) > 1 && argv[1] == "browse"
}

func openPaneProcessIDs(session, pane string) (map[int]bool, error) {
	result, err := openHerdr(session, "pane", "process-info", "--pane", pane)
	if err != nil {
		return nil, err
	}
	pids := map[int]bool{}
	for _, process := range result.Result.ProcessInfo.Processes {
		pids[process.PID] = true
	}
	return pids, nil
}

// openNvimModified reports the PID of the Neovim on socket and its parent,
// and counts its unsaved buffers that can be written: files, and acwrite
// buffers such as an Octo PR edit. A modified scratch buffer, like the
// dashboard, holds nothing to lose.
func openNvimModified(socket string) (pid, parent, modified int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	expr := "getpid() .. ' ' .. luaeval('(vim.uv or vim.loop).os_getppid()') .. ' ' .. len(filter(getbufinfo({'bufmodified': 1}), {_, b -> index(['', 'acwrite'], getbufvar(b.bufnr, '&buftype')) >= 0}))"
	out, err := exec.CommandContext(ctx, "nvim", "--server", socket, "--remote-expr", expr).Output()
	if err != nil {
		return 0, 0, 0, err
	}
	if _, err := fmt.Sscan(string(out), &pid, &parent, &modified); err != nil {
		return 0, 0, 0, fmt.Errorf("unexpected Neovim answer %q", strings.TrimSpace(string(out)))
	}
	return pid, parent, modified, nil
}

// openPtyCounts reports the pseudo-terminals in use and the machine's cap.
// Only macOS caps them low enough to matter; elsewhere max is 0.
var openPtyCounts = func() (used, max int) {
	if runtime.GOOS != "darwin" {
		return 0, 0
	}
	out, err := exec.Command("sysctl", "-n", "kern.tty.ptmx_max").Output()
	if err != nil {
		return 0, 0
	}
	max, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, 0
	}
	// macOS creates a /dev/ttysNNN node per open pty and removes it on close.
	nodes, _ := filepath.Glob("/dev/ttys[0-9]*")
	return len(nodes), max
}

func openPtyLow(used, max int) bool {
	return max > 0 && max-used < max/8
}

// openPtyReport describes the pty budget, and when it is low names the herdr
// sessions and pane kinds that hold the most.
func openPtyReport() (string, bool) {
	used, max := openPtyCounts()
	if max == 0 {
		return "", false
	}
	report := fmt.Sprintf("%d of %d pseudo-terminals in use (kern.tty.ptmx_max)", used, max)
	if !openPtyLow(used, max) {
		return report, false
	}
	if holders := openPtyHolders(); holders != "" {
		report += "; most panes: " + holders
	}
	return report + branding.Text("; close idle tool panes with {{command}} open --trim --all"), true
}

func openPtyHolders() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "herdr", "session", "list").Output()
	if err != nil {
		return ""
	}
	type holder struct {
		session string
		total   int
		kinds   map[string]int
	}
	var holders []holder
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "running" {
			continue
		}
		panes, err := openHerdr(fields[0], "pane", "list")
		if err != nil {
			continue
		}
		h := holder{session: fields[0], total: len(panes.Result.Panes), kinds: map[string]int{}}
		for _, pane := range panes.Result.Panes {
			switch pane.Label {
			case "agent", "browse", "shell", "status":
				h.kinds[pane.Label]++
			default:
				h.kinds["other"]++
			}
		}
		holders = append(holders, h)
	}
	sort.SliceStable(holders, func(i, j int) bool { return holders[i].total > holders[j].total })
	if len(holders) > 3 {
		holders = holders[:3]
	}
	parts := make([]string, 0, len(holders))
	for _, h := range holders {
		var kinds []string
		for _, kind := range []string{"agent", "status", "browse", "shell", "other"} {
			if h.kinds[kind] > 0 {
				kinds = append(kinds, fmt.Sprintf("%s %d", kind, h.kinds[kind]))
			}
		}
		parts = append(parts, fmt.Sprintf("%s %d (%s)", h.session, h.total, strings.Join(kinds, ", ")))
	}
	return strings.Join(parts, ", ")
}
