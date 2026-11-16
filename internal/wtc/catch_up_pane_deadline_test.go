package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type reloadDeadlineEvent struct {
	Args []string
	At   time.Time
}

// Each invocation is a separate synthetic Herdr process. No real provider is
// reachable through the fixture PATH, and control writes only create markers.
func TestReloadDeadlineFixtureProcess(t *testing.T) {
	if os.Getenv("RELOAD_DEADLINE_FIXTURE") != "1" {
		return
	}
	state, mode := os.Getenv("RELOAD_DEADLINE_STATE"), os.Getenv("RELOAD_DEADLINE_MODE")
	if os.Getenv("RELOAD_DEADLINE_PIPE_CHILD") == "1" {
		time.Sleep(400 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(state, "pipe-child-done"), nil, 0600)
		os.Exit(0)
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(97)
	}
	args = args[1:]
	kind := args[0]
	if kind == "--session" {
		if len(args) < 4 {
			os.Exit(97)
		}
		args = args[2:]
		kind = strings.Join(args[:2], " ")
	}
	count := func(name string) int {
		p := filepath.Join(state, name)
		data, _ := os.ReadFile(p)
		data = append(data, 'x')
		if os.WriteFile(p, data, 0600) != nil {
			os.Exit(98)
		}
		return len(data)
	}
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(state, name)); return err == nil }
	stall := func() { time.Sleep(30 * time.Second) }
	reply := func(body any) {
		f, err := os.OpenFile(filepath.Join(state, "events"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(98)
		}
		_ = json.NewEncoder(f).Encode(reloadDeadlineEvent{Args: args, At: time.Now()})
		_ = f.Close()
		if body != nil {
			_ = json.NewEncoder(os.Stdout).Encode(body)
		}
		os.Exit(0)
	}
	switch kind {
	case "workspace list":
		reply(map[string]any{"result": map[string]any{"workspaces": []any{map[string]any{"label": "main", "workspace_id": "w1"}}}})
	case "pane list":
		n := count("lists")
		pane := "w1:p3"
		var agent any
		if mode == "before-interrupt" && n == 2 || mode == "before-restart" && n == 3 {
			stall()
		}
		if mode == "replaced-before" && n == 2 || mode == "replaced-after" && n == 3 {
			pane = "w1:p4"
		}
		if mode == "occupied" || mode == "agent-after" && n == 3 {
			agent = "synthetic-agent"
		}
		reply(map[string]any{"result": map[string]any{"panes": []any{map[string]any{"label": "status", "pane_id": pane, "agent": agent}}}})
	case "pane process-info":
		phase := "initial"
		if exists("interrupts") {
			phase = "interrupt"
		}
		if exists("runs") {
			phase = "restart"
		}
		n := count(phase + "-reads")
		if mode == "stall-"+phase {
			stall()
		}
		if mode == "shared-budget" && n == 1 {
			if phase == "interrupt" {
				time.Sleep(3 * time.Second)
			}
			if phase == "restart" {
				time.Sleep(5 * time.Second)
			}
		}
		argv := []string{"wtc", "status", "--tui"}
		if phase == "interrupt" {
			argv = []string{"zsh"}
		}
		if mode == "unrelated" || mode == "shell-changed" && phase == "interrupt" && n == 2 {
			argv = []string{"nvim"}
		}
		if mode == phase+"-fifth" && n < 5 || mode == phase+"-unknown" {
			if n == 5 {
				_ = os.WriteFile(filepath.Join(state, "last-failed-read"), nil, 0600)
			}
			os.Exit(1)
		}
		if mode == "inherited-pipe" {
			child := exec.Command(os.Args[0], "-test.run=^TestReloadDeadlineFixtureProcess$")
			child.Env = append(os.Environ(), "RELOAD_DEADLINE_PIPE_CHILD=1")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if child.Start() != nil {
				os.Exit(98)
			}
			_ = child.Process.Release()
		}
		if mode == "short-reply" {
			time.Sleep(150 * time.Millisecond)
		}
		reply(map[string]any{"result": map[string]any{"process_info": map[string]any{"foreground_process_group_id": 42, "foreground_processes": []any{map[string]any{"pid": 43, "argv": []string{"wtc", "status"}}, map[string]any{"pid": 42, "argv": argv}}}}})
	case "pane send-keys":
		count("interrupts")
		if mode == "stall-interrupt-ack" {
			stall()
		}
		reply(nil)
	case "pane run":
		count("runs")
		if mode == "stall-restart-ack" {
			stall()
		}
		reply(nil)
	case "where":
		count("mise-where")
		stall()
	default:
		os.Exit(97)
	}
}

func reloadDeadlineFixture(t *testing.T, mode string) (*Context, string) {
	t.Helper()
	c := newWorkspaceFixture(t)
	state := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(c.Workspace, "deadline-bin")
	script := "#!/bin/sh\nexec \"$RELOAD_DEADLINE_BINARY\" -test.run=^TestReloadDeadlineFixtureProcess$ -- \"$@\"\n"
	fixtureFile(t, filepath.Join(bin, "herdr"), script, 0755)
	fixtureFile(t, filepath.Join(bin, "mise"), script, 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HERDR_SESSION", "synthetic")
	t.Setenv("HARNESS_HERDR_SESSION", "synthetic")
	t.Setenv("RELOAD_DEADLINE_FIXTURE", "1")
	t.Setenv("RELOAD_DEADLINE_STATE", state)
	t.Setenv("RELOAD_DEADLINE_MODE", mode)
	t.Setenv("RELOAD_DEADLINE_BINARY", binary)
	return c, state
}

func reloadDeadlineCount(t *testing.T, state, name string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(state, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return len(data)
}

func TestCatchUpReloadDeadline(t *testing.T) {
	for _, tc := range []struct {
		mode, outcome                                  string
		interrupts, runs, interruptReads, restartReads int
		reason                                         string
	}{
		{"healthy", "restarted", 1, 1, 2, 1, "observed status process"},
		{"interrupt-fifth", "restarted", 1, 1, 6, 1, "observed status process"},
		{"restart-fifth", "restarted", 1, 1, 2, 5, "observed status process"},
		{"interrupt-unknown", "failed", 1, 0, 5, 0, "no command sent"},
		{"restart-unknown", "failed", 1, 1, 2, 5, "restart not observed"},
		{"occupied", "needs-owner", 0, 0, 0, 0, "agent"},
		{"replaced-before", "failed", 0, 0, 0, 0, "no interruption"},
		{"replaced-after", "failed", 1, 0, 1, 0, "no command sent"},
		{"agent-after", "needs-owner", 1, 0, 1, 0, "no command sent"},
		{"unrelated", "needs-owner", 0, 0, 0, 0, "unrelated process"},
		{"shell-changed", "needs-owner", 1, 0, 2, 0, "no command sent"},
		{"dry-run", "planned", 0, 0, 0, 0, "would reload"},
		{"stall-initial", "failed", 0, 0, 0, 0, "no pane control"},
		{"stall-interrupt", "failed", 1, 0, 1, 0, "no command sent"},
		{"stall-restart", "failed", 1, 1, 2, 1, "restart not observed"},
		{"before-interrupt", "failed", 0, 0, 0, 0, "no interruption"},
		{"before-restart", "failed", 1, 0, 1, 0, "no command sent"},
		{"stall-interrupt-ack", "failed", 1, 0, 0, 0, "interrupt delivery unconfirmed"},
		{"stall-restart-ack", "failed", 1, 1, 2, 0, "restart delivery unconfirmed"},
		{"shared-budget", "failed", 1, 1, 2, 1, "restart not observed"},
		{"inherited-pipe", "failed", 0, 0, 0, 0, "no pane control"},
		{"mise-where", "failed", 0, 0, 0, 0, "pane left running"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c, state := reloadDeadlineFixture(t, tc.mode)
			install := tc.mode == "mise-where"
			if install {
				fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.2.2\n", 0644)
				fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[compatibility]\nrequires = \">=0.2.0,<0.3\"\n", 0644)
				fixtureGit(t, "-C", c.Harness, "add", ".")
				fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic installed pin")
			}
			report := CatchUpReport{Initiator: c.Collection}
			start := time.Now()
			c.catchUpReloadStatus(&report, catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}, CatchUpOptions{ReloadStatus: true, DryRun: tc.mode == "dry-run", InstallTools: install})
			elapsed := time.Since(start)
			t.Logf("elapsed=%s outcome=%+v", elapsed, report.Outcomes)
			if elapsed > 8*time.Second {
				t.Errorf("reload exceeded eight seconds: %s", elapsed)
			}
			if len(report.Outcomes) != 1 || report.Outcomes[0].Outcome != tc.outcome || !strings.Contains(report.Outcomes[0].Reason, tc.reason) {
				t.Fatalf("outcome=%+v, want %s with %q", report.Outcomes, tc.outcome, tc.reason)
			}
			for _, count := range []struct {
				name string
				want int
			}{{"interrupts", tc.interrupts}, {"runs", tc.runs}, {"interrupt-reads", tc.interruptReads}, {"restart-reads", tc.restartReads}} {
				if got := reloadDeadlineCount(t, state, count.name); got != count.want {
					t.Errorf("%s=%d, want %d", count.name, got, count.want)
				}
			}
			if strings.HasSuffix(tc.mode, "-unknown") {
				info, err := os.Stat(filepath.Join(state, "last-failed-read"))
				if err != nil {
					t.Fatal(err)
				}
				if tail := time.Since(info.ModTime()); tail >= time.Second {
					t.Errorf("waited %s after final failed read", tail)
				}
			}
			if install && reloadDeadlineCount(t, state, "mise-where") != 1 {
				t.Error("installed-pin subprocess was not exercised")
			}
			if tc.mode == "inherited-pipe" {
				// The bounded reader closes inherited pipes; the fixture-owned child has
				// its own short lifetime, and is observed exiting before this case ends.
				until := time.Now().Add(time.Second)
				for time.Now().Before(until) {
					if _, err := os.Stat(filepath.Join(state, "pipe-child-done")); err == nil {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
				t.Error("synthetic pipe child did not exit")
			}
		})
	}
}

func TestCatchUpReloadExpiredAndLateOutput(t *testing.T) {
	_, state := reloadDeadlineFixture(t, "short-reply")
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.CommandContext(expired, "herdr", "--session", "synthetic", "pane", "run", "w1:p3", "synthetic-command")
	if _, err := catchUpReloadOutput(expired, cmd); !errors.Is(err, context.Canceled) {
		t.Fatalf("expired command: %v", err)
	}
	if cmd.Process != nil || reloadDeadlineCount(t, state, "runs") != 0 {
		t.Fatal("expired context launched a control write")
	}
	ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	// Deliberately unbound command: exercise the output fence even when a reply
	// becomes available after expiry without relying on cancellation to kill it.
	late := exec.Command("herdr", "--session", "synthetic", "pane", "process-info", "--pane", "w1:p3")
	data, err := catchUpReloadOutput(ctx, late)
	if !errors.Is(err, context.DeadlineExceeded) || len(data) != 0 {
		t.Fatalf("accepted late reply: %s %v", data, err)
	}
	start := time.Now()
	if err := catchUpReloadWait(expired); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("expired wait used retry interval: %s", elapsed)
	}
	// The ordinary, non-reload reader retains its existing process behavior.
	argv, err := catchUpForeground("synthetic", "w1:p3")
	if err != nil || !catchUpStatusProcess(argv) {
		t.Fatal(fmt.Sprintf("ordinary reader changed: %v %v", argv, err))
	}
}
