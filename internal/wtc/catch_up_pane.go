package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
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

// Reload Herdr/mise commands and observation waits share a deadline. Filesystem
// and Git inspection consumes that time but is not interrupted by it.
// TestCatchUpReloadSelectedCommand checks real-command cancellation and pipe
// cleanup headroom against the elapsed-time ceiling.
const catchUpStatusReloadTimeout = 7500 * time.Millisecond
const catchUpReloadWaitDelay = 200 * time.Millisecond

func catchUpReloadContextErr(ctx context.Context) error {
	return catchUpReloadContextErrAt(ctx, time.Now())
}

func catchUpReloadContextErrAt(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !now.Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// Drain the command pipes in this goroutine rather than racing a short cleanup
// timer against separately scheduled pipe-copy goroutines. EOF and a reaped
// successful child are still required; holding an inherited pipe stays unknown.
func catchUpReloadOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if err := catchUpReloadContextErr(ctx); err != nil {
		return nil, err
	}
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, fmt.Errorf("reload command output already configured")
	}
	stdout, outWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	defer outWriter.Close()
	stderr, errWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer stderr.Close()
	defer errWriter.Close()
	fds := []unix.PollFd{{Fd: int32(stdout.Fd()), Events: unix.POLLIN}, {Fd: int32(stderr.Fd()), Events: unix.POLLIN}}
	for _, fd := range fds {
		if err := unix.SetNonblock(int(fd.Fd), true); err != nil {
			return nil, err
		}
	}
	cmd.Stdout, cmd.Stderr = outWriter, errWriter
	cmd.WaitDelay = catchUpReloadWaitDelay
	if err := catchUpReloadContextErr(ctx); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = outWriter.Close()
	_ = errWriter.Close()
	type completion struct {
		err error
		at  time.Time
	}
	done := make(chan completion, 1)
	go func() { err := cmd.Wait(); done <- completion{err: err, at: time.Now()} }()
	var finished *completion
	var output bytes.Buffer
	var errHead, errTail []byte
	const errorPart = 32 << 10
	captureError := func(data []byte) {
		if len(errHead) < errorPart {
			n := min(len(data), errorPart-len(errHead))
			errHead = append(errHead, data[:n]...)
			data = data[n:]
		}
		if len(data) >= errorPart {
			errTail = append(errTail[:0], data[len(data)-errorPart:]...)
		} else if len(data) > 0 {
			errTail = append(errTail, data...)
			if len(errTail) > errorPart {
				errTail = append(errTail[:0], errTail[len(errTail)-errorPart:]...)
			}
		}
	}
	var readErr error
	buf := make([]byte, 32<<10)
	for {
		if finished == nil {
			select {
			case result := <-done:
				finished = &result
			default:
			}
		}
		if finished != nil {
			if expired := catchUpReloadContextErr(ctx); expired != nil {
				return nil, expired
			}
			if fds[0].Fd < 0 && fds[1].Fd < 0 {
				if exit, ok := finished.err.(*exec.ExitError); ok {
					exit.Stderr = append(errHead, errTail...)
				}
				if finished.err != nil {
					return output.Bytes(), finished.err
				}
				return output.Bytes(), readErr
			}
		}
		for i := range fds {
			for fds[i].Fd >= 0 {
				n, err := unix.Read(int(fds[i].Fd), buf)
				if n > 0 {
					if i == 0 {
						_, _ = output.Write(buf[:n])
					} else {
						captureError(buf[:n])
					}
				}
				if err == unix.EINTR && catchUpReloadContextErr(ctx) == nil {
					continue
				}
				if err == unix.EAGAIN {
					break
				}
				if err != nil {
					if readErr == nil {
						readErr = err
					}
					if i == 0 {
						_ = stdout.Close()
					} else {
						_ = stderr.Close()
					}
				}
				if err != nil || n == 0 {
					fds[i].Fd = -1
					break
				}
				// A continuously writing child must not monopolize draining
				// past the absolute deadline. Wait still reaps it below.
				if catchUpReloadContextErr(ctx) != nil {
					break
				}
			}
		}
		if fds[0].Fd < 0 && fds[1].Fd < 0 {
			if finished == nil {
				select {
				case result := <-done:
					finished = &result
				case <-time.After(10 * time.Millisecond):
				}
			}
			continue
		}
		// First consume immediately available data/EOF, even if this goroutine
		// was descheduled after Wait. The cleanup limit bounds further waiting,
		// not a race against collecting an already closed pipe. The absolute
		// observation deadline above still rejects late replies.
		if finished != nil && !time.Now().Before(finished.at.Add(catchUpReloadWaitDelay)) {
			return output.Bytes(), exec.ErrWaitDelay
		}
		// Poll only briefly so child completion and cancellation do not depend
		// on another I/O copier being scheduled. Never extend either deadline.
		delay := 10 * time.Millisecond
		if deadline, ok := ctx.Deadline(); ok && cmd.Cancel != nil {
			delay = min(delay, max(time.Millisecond, time.Until(deadline)))
		}
		if finished != nil {
			delay = min(delay, max(time.Millisecond, time.Until(finished.at.Add(catchUpReloadWaitDelay))))
		}
		if _, err := unix.Poll(fds, int((delay+time.Millisecond-1)/time.Millisecond)); err != nil && err != unix.EINTR {
			if readErr == nil {
				readErr = err
			}
			// A pipe error must not leave an unreaped child behind. Closing the
			// read ends unblocks writers; CommandContext retains cancellation.
			_ = stdout.Close()
			_ = stderr.Close()
			fds[0].Fd, fds[1].Fd = -1, -1
		}
	}
}

func catchUpReloadWait(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return catchUpReloadContextErr(ctx)
	}
}

// Dependencies belong to one invocation. Production uses real time and commands;
// logical sequencing tests can advance a clock without subprocess scheduling.
type catchUpReloadOps struct {
	ctx  context.Context
	now  func() time.Time
	wait func(context.Context) error
	run  func(context.Context, string, string, ...string) ([]byte, error)
}

func catchUpReloadRealOps(ctx context.Context) catchUpReloadOps {
	return catchUpReloadOps{ctx: ctx, now: time.Now, wait: catchUpReloadWait, run: catchUpReloadExec}
}

func catchUpReloadExec(ctx context.Context, name, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return catchUpReloadOutput(ctx, cmd)
}

func (ops catchUpReloadOps) err() error {
	return catchUpReloadContextErrAt(ops.ctx, ops.now())
}

func (ops catchUpReloadOps) output(name, dir string, args ...string) ([]byte, error) {
	if err := ops.err(); err != nil {
		return nil, err
	}
	data, err := ops.run(ops.ctx, name, dir, args...)
	if expired := ops.err(); expired != nil {
		return nil, expired
	}
	return data, err
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
	return catchUpPaneWithHerdr(session, collection, catchUpHerdr)
}

func catchUpPaneWithHerdr(session, collection string, herdr func(string, ...string) ([]byte, error)) (string, bool, error) {
	workspaces, err := herdr(session, "workspace", "list")
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
	panes, err := herdr(session, "pane", "list", "--workspace", id)
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
	return catchUpForegroundWithHerdr(session, pane, catchUpHerdr)
}

func catchUpForegroundWithHerdr(session, pane string, herdr func(string, ...string) ([]byte, error)) ([]string, error) {
	data, err := herdr(session, "pane", "process-info", "--pane", pane)
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
	ctx, cancel := context.WithTimeout(context.Background(), catchUpStatusReloadTimeout)
	defer cancel()
	c.catchUpReloadStatusWithOps(report, t, opt, catchUpReloadRealOps(ctx))
}

func (c *Context) catchUpReloadStatusWithOps(report *CatchUpReport, t catchUpTarget, opt CatchUpOptions, ops catchUpReloadOps) {
	herdr := func(session string, args ...string) ([]byte, error) {
		command := append([]string{"--session", session}, args...)
		return ops.output("herdr", "", command...)
	}

	add := func(outcome, reason string) {
		report.add("pane", t.collection, "status", outcome, reason, "", "", "")
	}
	expired := func(outcome, state string) bool {
		if ops.err() == nil {
			return false
		}
		add(outcome, "reload observation budget expired; "+state+"; inspect pane before retrying reload")
		return true
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
	pane, occupied, err := catchUpPaneWithHerdr(session, t.collection, herdr)
	if err != nil {
		if expired("failed", "no pane control attempted") {
			return
		}
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
	argv, err := catchUpForegroundWithHerdr(session, pane, herdr)
	if err != nil {
		if expired("failed", "no pane control attempted") {
			return
		}
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
			if out, err := ops.output(mise, target.Collection, "where", cliMiseTool+"@"+version.String()); err != nil || strings.TrimSpace(string(out)) == "" {
				if expired("failed", "pin inspection unconfirmed; pane left running") {
					return
				}
				add("failed", "pinned release unavailable after install; pane left running")
				return
			}
		}
		// Reuse the selected-input chain guard after installer inspection too:
		// a late edit through a tracked alias must not interrupt the old pane.
		pinSelectedNow, pinSelectErr := pinInput(target.Harness)
		configSelectedNow, configSelectErr := configInput(target.Harness)
		pinNow, pinErr := upgradeInput(target.Harness, pinSelectedNow)
		configNow, configErr := upgradeInput(target.Harness, configSelectedNow)
		pinBytesNow, pinReadErr := os.ReadFile(pinNow)
		configBytesNow, configReadErr := os.ReadFile(configNow)
		if pinSelectErr != nil || configSelectErr != nil || pinSelectedNow != pin || configSelectedNow != config ||
			pinErr != nil || configErr != nil || pinReadErr != nil || configReadErr != nil || pinNow != pinTarget || configNow != configTarget ||
			!bytes.Equal(raw, pinBytesNow) || !bytes.Equal(configBytes, configBytesNow) || catchUpOperation(catchUpTarget{path: target.Harness}) != "" {
			add("needs-owner", "installed input chain changed; pane left running")
			return
		}
		command += "TABA_DEV_BUILD=off WTC_DEV_BUILD=off MISE_AUTO_INSTALL=0 MISE_EXEC_AUTO_INSTALL=0 '" + strings.ReplaceAll(mise, "'", "'\"'\"'") + "' exec '" + cliMiseTool + "@" + version.String() + "' -- "
	}
	command += "wtc status --tui"
	if expired("failed", "no pane control attempted") {
		return
	}
	if opt.DryRun {
		add("planned", "would reload "+pane)
		return
	}
	// Tool/input inspection can be slow. Herdr reads are snapshots; validate
	// the pane and current foreground again before each control write.
	currentPane, occupied, err := catchUpPaneWithHerdr(session, t.collection, herdr)
	if err != nil || currentPane != pane {
		if expired("failed", "no interruption attempted") {
			return
		}
		add("failed", "status pane identity changed; no interruption attempted")
		return
	}
	if occupied {
		add("needs-owner", "agent appeared in status pane; untouched")
		return
	}
	argv, err = catchUpForegroundWithHerdr(session, pane, herdr)
	if err != nil || (!catchUpShell(argv) && !catchUpStatusProcess(argv)) {
		if expired("needs-owner", "no interruption attempted") {
			return
		}
		add("needs-owner", "foreground changed; no interruption attempted")
		return
	}
	if !catchUpShell(argv) {
		if _, err := herdr(session, "pane", "send-keys", pane, "ctrl+c"); err != nil {
			if expired("failed", "interrupt delivery unconfirmed for "+pane) {
				return
			}
			add("failed", "interrupt delivery unconfirmed for "+pane+"; inspect pane before retrying reload")
			return
		}
	}
	idle := false
	for attempt := 0; attempt < 5; attempt++ {
		argv, err = catchUpForegroundWithHerdr(session, pane, herdr)
		// A foreground group can disappear while the interrupted process exits.
		// An unknown observation consumes the same budget as a busy process.
		if err == nil && ops.err() == nil && catchUpShell(argv) {
			idle = true
			break
		}
		if attempt < 4 {
			if waitErr := ops.wait(ops.ctx); waitErr != nil {
				break
			}
		}
	}
	if !idle {
		if expired("failed", "no command sent") {
			return
		}
		if err != nil {
			add("failed", "foreground unknown in "+pane+"; no command sent; inspect pane before retrying reload")
			return
		}
		add("needs-owner", "pane "+pane+" did not become a verified shell; no command sent; inspect pane before retrying reload")
		return
	}
	checkPane, agent, err := catchUpPaneWithHerdr(session, t.collection, herdr)
	if err != nil || checkPane != pane {
		if expired("failed", "no command sent") {
			return
		}
		add("failed", "status pane identity changed; no command sent")
		return
	}
	if agent {
		add("needs-owner", "agent appeared in status pane "+pane+"; no command sent")
		return
	}
	argv, err = catchUpForegroundWithHerdr(session, pane, herdr)
	if err != nil || !catchUpShell(argv) {
		if expired("needs-owner", "no command sent") {
			return
		}
		add("needs-owner", "foreground changed before restart; no command sent")
		return
	}
	if _, err := herdr(session, "pane", "run", pane, command); err != nil {
		if expired("failed", "restart delivery unconfirmed for "+pane) {
			return
		}
		add("failed", "restart delivery unconfirmed for "+pane+"; inspect pane before retrying reload")
		return
	}
	for attempt := 0; attempt < 5; attempt++ {
		argv, err = catchUpForegroundWithHerdr(session, pane, herdr)
		// Command delivery is not proof that the status process has started.
		if err == nil && ops.err() == nil && catchUpStatusProcess(argv) {
			add("restarted", "observed status process in "+pane+"; ongoing health not monitored")
			return
		}
		if attempt < 4 {
			if waitErr := ops.wait(ops.ctx); waitErr != nil {
				break
			}
		}
	}
	if expired("failed", "command delivered to "+pane+" but status restart not observed") {
		return
	}
	add("failed", "command delivered to "+pane+" but status restart not observed; inspect pane before retrying reload")
}
