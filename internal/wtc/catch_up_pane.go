package wtc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type catchUpWorkspaceList struct {
	Result struct {
		Workspaces []struct {
			Label string `json:"label"`
			ID    string `json:"workspace_id"`
		} `json:"workspaces"`
	} `json:"result"`
}

type catchUpPaneList struct {
	Result struct {
		Panes []struct {
			Label       string          `json:"label"`
			ID          string          `json:"pane_id"`
			Agent       json.RawMessage `json:"agent"`
			AgentStatus string          `json:"agent_status"`
		} `json:"panes"`
	} `json:"result"`
}

type catchUpProcessInfo struct {
	Result struct {
		ProcessInfo struct {
			Group     int `json:"foreground_process_group_id"`
			Processes []struct {
				PID     int      `json:"pid"`
				Argv    []string `json:"argv"`
				Cmdline string   `json:"cmdline"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	} `json:"result"`
}

func catchUpHerdr(session string, args ...string) ([]byte, error) {
	command := append([]string{"--session", session}, args...)
	return exec.Command("herdr", command...).Output()
}

func catchUpSession(c *Context) string {
	for _, value := range []string{os.Getenv("HARNESS_HERDR_SESSION"), os.Getenv("HERDR_SESSION"), c.Config.Herdr.Session} {
		if value != "" {
			return value
		}
	}
	return strings.TrimSuffix(strings.TrimSuffix(filepath.Base(c.Workspace), "-harness"), "-wtc")
}

func catchUpPane(c *Context, session, collection string) (string, bool, error) {
	workspaces, err := catchUpHerdr(session, "workspace", "list")
	if err != nil {
		return "", false, fmt.Errorf("herdr workspace lookup failed; no pane control attempted")
	}
	var ws catchUpWorkspaceList
	if json.Unmarshal(workspaces, &ws) != nil {
		return "", false, fmt.Errorf("herdr workspace lookup failed; no pane control attempted")
	}
	id := ""
	for _, item := range ws.Result.Workspaces {
		if item.Label == collection {
			id = item.ID
			break
		}
	}
	if id == "" {
		return "", false, nil
	}
	panes, err := catchUpHerdr(session, "pane", "list", "--workspace", id)
	if err != nil {
		return "", false, fmt.Errorf("herdr pane lookup failed; no pane control attempted")
	}
	var listing catchUpPaneList
	if json.Unmarshal(panes, &listing) != nil {
		return "", false, fmt.Errorf("herdr pane lookup failed; no pane control attempted")
	}
	for _, item := range listing.Result.Panes {
		if item.Label == "status" {
			occupied := len(item.Agent) != 0 && string(item.Agent) != "null" && string(item.Agent) != `""`
			return item.ID, occupied, nil
		}
	}
	return "", false, nil
}

// catchUpAgentBusy returns the status of a working or blocked agent in the
// collection's herdr workspace, or "" when there is none. Without herdr, or
// without a running session, no agent can be busy; any other herdr failure is
// an error, which callers treat as unknown and leave to the owner.
func catchUpAgentBusy(session, collection string) (string, error) {
	if _, err := exec.LookPath("herdr"); err != nil {
		return "", nil
	}
	workspaces, err := catchUpHerdr(session, "workspace", "list")
	if err != nil {
		// herdr reports a stopped server on stderr.
		var exit *exec.ExitError
		if errors.As(err, &exit) && bytes.Contains(exit.Stderr, []byte(`"server_not_running"`)) {
			return "", nil
		}
		return "", fmt.Errorf("herdr workspace lookup failed")
	}
	var ws catchUpWorkspaceList
	if json.Unmarshal(workspaces, &ws) != nil {
		return "", fmt.Errorf("herdr workspace lookup failed")
	}
	id := ""
	for _, item := range ws.Result.Workspaces {
		if item.Label == collection {
			id = item.ID
			break
		}
	}
	if id == "" {
		return "", nil
	}
	panes, err := catchUpHerdr(session, "pane", "list", "--workspace", id)
	if err != nil {
		return "", fmt.Errorf("herdr pane lookup failed")
	}
	var listing catchUpPaneList
	if json.Unmarshal(panes, &listing) != nil {
		return "", fmt.Errorf("herdr pane lookup failed")
	}
	for _, item := range listing.Result.Panes {
		if item.AgentStatus == "working" || item.AgentStatus == "blocked" {
			return item.AgentStatus, nil
		}
	}
	return "", nil
}

// AgentBusy checks this collection's owner, including when it is also the
// caller. Status controls must not use catch-up's initiating-agent exemption.
func (c *Context) AgentBusy() (string, error) {
	return catchUpAgentBusy(catchUpSession(c), filepath.Base(c.Collection))
}

func catchUpForeground(session, pane string) ([]string, error) {
	data, err := catchUpHerdr(session, "pane", "process-info", "--pane", pane)
	if err != nil {
		return nil, err
	}
	var info catchUpProcessInfo
	if json.Unmarshal(data, &info) != nil {
		return nil, fmt.Errorf("invalid foreground process response")
	}
	for _, process := range info.Result.ProcessInfo.Processes {
		if process.PID != info.Result.ProcessInfo.Group {
			continue
		}
		if len(process.Argv) != 0 {
			return process.Argv, nil
		}
		return strings.Fields(process.Cmdline), nil
	}
	return nil, fmt.Errorf("foreground process unknown")
}

func catchUpShell(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	name := filepath.Base(strings.TrimPrefix(argv[0], "-"))
	switch name {
	case "bash", "sh", "zsh", "fish", "nu", "nushell":
		return len(argv) == 1 || len(argv) == 2 && strings.HasPrefix(argv[1], "-")
	}
	return false
}

func catchUpStatusProcess(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if name := filepath.Base(argv[0]); name == "bash" || name == "sh" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return false
	}
	switch filepath.Base(argv[0]) {
	case "wtc", "taba":
		return len(argv) > 1 && argv[1] == "status"
	case "wtc-status-tui.sh", "wtc-status.sh", "wtc-status-legacy-tui.sh", "wtc-status-legacy.sh":
		return true
	}
	return false
}

func (c *Context) catchUpReloadStatus(report *CatchUpReport, t catchUpTarget, opt CatchUpOptions) {
	add := func(outcome, reason string) {
		report.add("pane", t.collection, "status", outcome, reason, "", "", "")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		add("skipped", "herdr unavailable; no pane control attempted")
		return
	}
	target, err := OpenCollection(filepath.Join(c.Workspace, t.collection))
	if err != nil {
		add("failed", "target collection unavailable; no pane control attempted")
		return
	}
	session := catchUpSession(target)
	pane, occupied, err := catchUpPane(target, session, t.collection)
	if err != nil {
		add("failed", err.Error())
		return
	}
	if pane == "" {
		add("skipped", "no matching status pane")
		return
	}
	if occupied {
		add("needs-owner", "status-labelled pane contains an agent; untouched")
		return
	}
	argv, err := catchUpForeground(session, pane)
	if err != nil {
		add("failed", "foreground lookup failed; no pane control attempted")
		return
	}
	if !catchUpShell(argv) && !catchUpStatusProcess(argv) {
		add("needs-owner", "status pane runs an unrelated process; untouched")
		return
	}
	command := "cd '" + strings.ReplaceAll(target.Collection, "'", "'\"'\"'") + "' && "
	if opt.InstallTools {
		// A pane's PATH may still name the previous release. This process uses
		// the installed pin with automatic installs and dev handover disabled.
		pin, err := pinInput(target.Harness)
		if err != nil {
			add("failed", "cannot select installed pin; pane left running")
			return
		}
		pinTarget, err := upgradeInput(target.Harness, pin)
		if err != nil {
			add("needs-owner", "cannot verify installed pin: "+err.Error()+"; pane left running")
			return
		}
		raw, err := os.ReadFile(pinTarget)
		if err != nil {
			add("failed", "cannot read installed pin; pane left running")
			return
		}
		version, err := releaseVersion(strings.TrimSpace(string(raw)))
		if err != nil {
			add("failed", "invalid installed pin; pane left running")
			return
		}
		mise, err := exec.LookPath("mise")
		if err != nil {
			add("failed", "mise unavailable; pane left running")
			return
		}
		config, err := configInput(target.Harness)
		if err != nil {
			add("failed", "cannot select installed config; pane left running")
			return
		}
		configTarget, err := upgradeInput(target.Harness, config)
		if err != nil {
			add("needs-owner", "cannot verify installed config: "+err.Error()+"; pane left running")
			return
		}
		configBytes, err := os.ReadFile(configTarget)
		if err != nil {
			add("failed", "cannot read installed config; pane left running")
			return
		}
		if catchUpOperation(catchUpTarget{path: target.Harness}) != "" {
			add("needs-owner", "pin/config changed or operation in progress; pane left running")
			return
		}
		if !opt.DryRun {
			installed := exec.Command(mise, "where", cliMiseTool+"@"+version.String())
			installed.Dir = target.Collection
			if out, err := installed.Output(); err != nil || strings.TrimSpace(string(out)) == "" {
				add("failed", "pinned release unavailable after install; pane left running")
				return
			}
		}
		// Reuse the selected-input chain guard after installer inspection too:
		// a late edit through a tracked alias must not interrupt the old pane.
		pinNow, pinErr := upgradeInput(target.Harness, pin)
		configNow, configErr := upgradeInput(target.Harness, config)
		pinBytesNow, pinReadErr := os.ReadFile(pinNow)
		configBytesNow, configReadErr := os.ReadFile(configNow)
		if pinErr != nil || configErr != nil || pinReadErr != nil || configReadErr != nil || pinNow != pinTarget || configNow != configTarget ||
			!bytes.Equal(raw, pinBytesNow) || !bytes.Equal(configBytes, configBytesNow) || catchUpOperation(catchUpTarget{path: target.Harness}) != "" {
			add("needs-owner", "installed input chain changed; pane left running")
			return
		}
		command += "TABA_DEV_BUILD=off WTC_DEV_BUILD=off MISE_AUTO_INSTALL=0 MISE_EXEC_AUTO_INSTALL=0 '" + strings.ReplaceAll(mise, "'", "'\"'\"'") + "' exec '" + cliMiseTool + "@" + version.String() + "' -- "
	}
	command += "wtc status --tui"
	if opt.DryRun {
		add("planned", "would reload "+pane)
		return
	}
	if !catchUpShell(argv) {
		if _, err := catchUpHerdr(session, "pane", "send-keys", pane, "ctrl+c"); err != nil {
			add("failed", "could not interrupt "+pane)
			return
		}
	}
	idle := false
	for attempt := 0; attempt < 5; attempt++ {
		argv, err = catchUpForeground(session, pane)
		if err != nil {
			add("failed", "foreground lookup failed after interrupting "+pane)
			return
		}
		if catchUpShell(argv) {
			idle = true
			break
		}
		time.Sleep(time.Second)
	}
	if !idle {
		add("needs-owner", "pane "+pane+" did not become a verified shell")
		return
	}
	checkPane, agent, err := catchUpPane(target, session, t.collection)
	if err != nil || checkPane != pane {
		add("failed", "status pane identity changed; no command sent")
		return
	}
	if agent {
		add("needs-owner", "agent appeared in status pane "+pane+"; no command sent")
		return
	}
	if _, err := catchUpHerdr(session, "pane", "run", pane, command); err != nil {
		add("failed", "restart failed for "+pane)
		return
	}
	for attempt := 0; attempt < 5; attempt++ {
		argv, err = catchUpForeground(session, pane)
		if err != nil {
			add("failed", "foreground lookup failed after restarting "+pane)
			return
		}
		if catchUpStatusProcess(argv) {
			add("restarted", "observed status process in "+pane+"; ongoing health not monitored")
			return
		}
		time.Sleep(time.Second)
	}
	add("failed", "command delivered to "+pane+" but status restart not observed")
}
