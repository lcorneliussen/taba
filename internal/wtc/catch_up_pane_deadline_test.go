package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type reloadLogicalContext struct {
	context.Context
	deadline time.Time
}

func (c reloadLogicalContext) Deadline() (time.Time, bool) { return c.deadline, true }

type reloadScript struct {
	mode                    string
	lists, interrupts, runs int
	reads                   map[string]int
	calls                   []string
}

func (s *reloadScript) phase() string {
	if s.runs > 0 {
		return "restart"
	}
	if s.interrupts > 0 {
		return "interrupt"
	}
	return "initial"
}
func reloadCommand(name string, args []string) string {
	if filepath.Base(name) == "mise" {
		return "mise where"
	}
	return strings.Join(args[2:4], " ")
}
func (s *reloadScript) record(key string) {
	s.calls = append(s.calls, key)
	switch key {
	case "pane list":
		s.lists++
	case "pane process-info":
		s.reads[s.phase()]++
	case "pane send-keys":
		s.interrupts++
	case "pane run":
		s.runs++
	}
}
func reloadJSON(body any) []byte {
	data, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return data
}
func (s *reloadScript) run(_ context.Context, name, _ string, args ...string) ([]byte, error) {
	key := reloadCommand(name, args)
	s.record(key)
	switch key {
	case "workspace list":
		return reloadJSON(map[string]any{"result": map[string]any{"workspaces": []any{map[string]any{"label": "main", "workspace_id": "w1"}}}}), nil
	case "pane list":
		pane := "w1:p3"
		var agent any
		if s.mode == "replaced-before" && s.lists == 2 || s.mode == "replaced-after" && s.lists == 3 {
			pane = "w1:p4"
		}
		if s.mode == "occupied" || s.mode == "agent-before" && s.lists == 2 || s.mode == "agent-after" && s.lists == 3 {
			agent = "synthetic-agent"
		}
		return reloadJSON(map[string]any{"result": map[string]any{"panes": []any{map[string]any{"label": "status", "pane_id": pane, "agent": agent}}}}), nil
	case "pane process-info":
		phase := s.phase()
		n := s.reads[phase]
		if s.mode == phase+"-unknown" || s.mode == phase+"-fifth" && n < 5 {
			return nil, errors.New("synthetic lookup unavailable")
		}
		if s.mode == "one-waitdelay" && phase == "interrupt" && n == 1 {
			return nil, exec.ErrWaitDelay
		}
		argv := []string{"wtc", "status", "--tui"}
		if phase == "interrupt" {
			argv = []string{"zsh"}
		}
		if s.mode == "wait-expiry" && phase == "interrupt" {
			argv = []string{"wtc", "status", "--tui"}
		}
		if s.mode == "unrelated" || s.mode == "foreground-before" && phase == "initial" && n == 2 || s.mode == "foreground-after" && phase == "interrupt" && n == 2 {
			argv = []string{"nvim"}
		}
		return reloadJSON(map[string]any{"result": map[string]any{"process_info": map[string]any{"foreground_process_group_id": 42, "foreground_processes": []any{map[string]any{"pid": 43, "argv": []string{"wtc", "status"}}, map[string]any{"pid": 42, "argv": argv}}}}}), nil
	case "pane send-keys", "pane run":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected synthetic command %s", key)
}

func reloadDeadlineFixture(t *testing.T) (*Context, string) {
	t.Helper()
	c := newWorkspaceFixture(t)
	state := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(c.Workspace, "deadline-bin")
	script := "#!/bin/sh\nexec \"$RELOAD_DEADLINE_BINARY\" -test.run=^TestReloadDeadlineProcess$ -- \"$@\"\n"
	fixtureFile(t, filepath.Join(bin, "herdr"), script, 0755)
	fixtureFile(t, filepath.Join(bin, "mise"), script, 0755)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HERDR_SESSION", "synthetic")
	t.Setenv("HARNESS_HERDR_SESSION", "synthetic")
	t.Setenv("RELOAD_DEADLINE_FIXTURE", "1")
	t.Setenv("RELOAD_DEADLINE_STATE", state)
	t.Setenv("RELOAD_DEADLINE_BINARY", binary)
	return c, state
}
func reloadTarget(c *Context) catchUpTarget {
	return catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}
}
func reloadAssert(t *testing.T, r CatchUpReport, outcome, reason string) {
	t.Helper()
	if len(r.Outcomes) != 1 || r.Outcomes[0].Outcome != outcome || !strings.Contains(r.Outcomes[0].Reason, reason) {
		t.Fatalf("outcomes=%+v, want %s with %q", r.Outcomes, outcome, reason)
	}
}

func TestCatchUpReloadLogical(t *testing.T) {
	for _, tc := range []struct {
		mode, outcome, reason                                 string
		interrupts, runs, interruptReads, restartReads, waits int
	}{
		{"healthy", "restarted", "observed status process", 1, 1, 2, 1, 0},
		{"interrupt-fifth", "restarted", "observed status process", 1, 1, 6, 1, 4},
		{"restart-fifth", "restarted", "observed status process", 1, 1, 2, 5, 4},
		{"interrupt-unknown", "failed", "no command sent", 1, 0, 5, 0, 4},
		{"restart-unknown", "failed", "restart not observed", 1, 1, 2, 5, 4},
		{"one-waitdelay", "restarted", "observed status process", 1, 1, 3, 1, 1},
		{"occupied", "needs-owner", "agent", 0, 0, 0, 0, 0},
		{"agent-before", "needs-owner", "agent", 0, 0, 0, 0, 0},
		{"agent-after", "needs-owner", "no command sent", 1, 0, 1, 0, 0},
		{"replaced-before", "failed", "no interruption", 0, 0, 0, 0, 0},
		{"replaced-after", "failed", "no command sent", 1, 0, 1, 0, 0},
		{"foreground-before", "needs-owner", "no interruption", 0, 0, 0, 0, 0},
		{"foreground-after", "needs-owner", "no command sent", 1, 0, 2, 0, 0},
		{"unrelated", "needs-owner", "unrelated process", 0, 0, 0, 0, 0},
		{"dry-run", "planned", "would reload", 0, 0, 0, 0, 0},
		{"late-shell", "failed", "budget expired; no command sent", 1, 0, 1, 0, 0},
		{"late-status", "failed", "budget expired; command delivered", 1, 1, 2, 1, 0},
		{"at-deadline-status", "failed", "budget expired; command delivered", 1, 1, 2, 1, 0},
		{"before-deadline-status", "restarted", "observed status process", 1, 1, 2, 1, 0},
		{"late-ack", "failed", "budget expired; restart delivery unconfirmed", 1, 1, 2, 0, 0},
		{"shared-budget", "failed", "budget expired; command delivered", 1, 1, 2, 1, 0},
		{"wait-expiry", "failed", "budget expired; no command sent", 1, 0, 1, 0, 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c, _ := reloadDeadlineFixture(t)
			now := time.Unix(1700000000, 0)
			start := now
			ctx := reloadLogicalContext{Context: context.Background(), deadline: now.Add(catchUpStatusReloadTimeout)}
			s := reloadScript{mode: tc.mode, reads: map[string]int{}}
			waits := []time.Duration{}
			ops := catchUpReloadOps{ctx: ctx, now: func() time.Time { return now }}
			ops.wait = func(ctx context.Context) error {
				if err := catchUpReloadContextErrAt(ctx, now); err != nil {
					return err
				}
				waits = append(waits, time.Second)
				now = now.Add(time.Second)
				return catchUpReloadContextErrAt(ctx, now)
			}
			ops.run = func(ctx context.Context, name, dir string, args ...string) ([]byte, error) {
				data, err := s.run(ctx, name, dir, args...)
				key := reloadCommand(name, args)
				phase := s.phase()
				if key == "pane process-info" && (tc.mode == "late-shell" && phase == "interrupt" || tc.mode == "late-status" && phase == "restart") || tc.mode == "late-ack" && key == "pane run" {
					now = now.Add(8 * time.Second)
				}
				if key == "pane process-info" && phase == "restart" {
					if tc.mode == "at-deadline-status" {
						now = start.Add(catchUpStatusReloadTimeout)
					}
					if tc.mode == "before-deadline-status" {
						now = start.Add(catchUpStatusReloadTimeout - time.Nanosecond)
					}
				}
				if tc.mode == "shared-budget" && key == "pane process-info" && s.reads[phase] == 1 {
					if phase == "interrupt" {
						now = now.Add(3 * time.Second)
					}
					if phase == "restart" {
						now = now.Add(5 * time.Second)
					}
				}
				if tc.mode == "wait-expiry" && key == "pane process-info" && phase == "interrupt" && s.reads[phase] == 1 {
					now = now.Add(7 * time.Second)
				}
				return data, err
			}
			r := CatchUpReport{Initiator: c.Collection}
			c.catchUpReloadStatusWithOps(&r, reloadTarget(c), CatchUpOptions{ReloadStatus: true, DryRun: tc.mode == "dry-run"}, ops)
			reloadAssert(t, r, tc.outcome, tc.reason)
			if s.interrupts != tc.interrupts || s.runs != tc.runs || s.reads["interrupt"] != tc.interruptReads || s.reads["restart"] != tc.restartReads || len(waits) != tc.waits {
				t.Fatalf("controls=%d/%d reads=%v waits=%v calls=%v", s.interrupts, s.runs, s.reads, waits, s.calls)
			}
			for _, d := range waits {
				if d != time.Second {
					t.Fatalf("cadence changed: %s", d)
				}
			}
			if tc.mode == "interrupt-fifth" || tc.mode == "restart-fifth" || strings.HasSuffix(tc.mode, "-unknown") {
				if now.Sub(start) != 4*time.Second {
					t.Fatalf("logical budget=%s, want four one-second waits", now.Sub(start))
				}
			}
			// The script is the provider protocol, not a second reload algorithm. These
			// successful writes must still follow fresh production parser/fence reads.
			for i, key := range s.calls {
				if key == "pane send-keys" || key == "pane run" {
					if i < 3 || strings.Join(s.calls[i-3:i], ",") != "workspace list,pane list,pane process-info" {
						t.Fatalf("write lacked fresh fences: %v", s.calls[:i+1])
					}
				}
			}
		})
	}
}

func reloadWaitFile(path string) bool {
	until := time.Now().Add(20 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// This process handles only the selected real boundary. All unrelated prelude
// observations are scripted in-process so runtime startup is not a health claim.
func reloadStatusBody() any {
	return map[string]any{"result": map[string]any{"process_info": map[string]any{
		"foreground_process_group_id": 42,
		"foreground_processes":        []any{map[string]any{"pid": 42, "argv": []string{"wtc", "status", "--tui"}}},
	}}}
}

func TestReloadDeadlineProcess(t *testing.T) {
	if os.Getenv("RELOAD_DEADLINE_FIXTURE") != "1" {
		return
	}
	state, mode := os.Getenv("RELOAD_DEADLINE_STATE"), os.Getenv("RELOAD_DEADLINE_BOUNDARY")
	mark := func(name string) {
		if os.WriteFile(filepath.Join(state, name), []byte("x"), 0600) != nil {
			os.Exit(98)
		}
	}
	if mode == "pipe-holder" {
		mark("pipe-ready")
		if !reloadWaitFile(filepath.Join(state, "release")) {
			os.Exit(97)
		}
		mark("pipe-done")
		os.Exit(0)
	}
	mark("entered")
	if os.WriteFile(filepath.Join(state, "owned-pid"), []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
		os.Exit(98)
	}
	switch mode {
	case "interrupt-ack":
		mark("actual-interrupt")
	case "restart-ack":
		mark("actual-run")
	case "pipe":
		child := exec.Command(os.Args[0], "-test.run=^TestReloadDeadlineProcess$")
		child.Env = append(os.Environ(), "RELOAD_DEADLINE_BOUNDARY=pipe-holder")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(98)
		}
		if os.WriteFile(filepath.Join(state, "pipe-pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600) != nil {
			os.Exit(98)
		}
		_ = child.Process.Release()
		if !reloadWaitFile(filepath.Join(state, "pipe-ready")) {
			os.Exit(97)
		}
		_ = json.NewEncoder(os.Stdout).Encode(reloadStatusBody())
		os.Exit(0)
	case "late-reply":
		if !reloadWaitFile(filepath.Join(state, "release")) {
			os.Exit(97)
		}
		mark("emitted")
		_ = json.NewEncoder(os.Stdout).Encode(reloadStatusBody())
		os.Exit(0)
	case "reply", "clean-eof", "stdout-large", "stderr-success", "exit-error":
		if mode == "stderr-success" || mode == "exit-error" {
			_, _ = os.Stderr.Write([]byte(strings.Repeat("synthetic-stderr;", 10000)))
		}
		body := reloadStatusBody().(map[string]any)
		if mode == "stdout-large" {
			body["padding"] = strings.Repeat("synthetic-output;", 20000)
		}
		_ = json.NewEncoder(os.Stdout).Encode(body)
		mark("emitted")
		if mode == "exit-error" {
			os.Exit(17)
		}
		os.Exit(0)
	}
	time.Sleep(30 * time.Second)
	os.Exit(99)
}

func reloadPipeCleanup(t *testing.T, state string) {
	t.Helper()
	_ = os.WriteFile(filepath.Join(state, "release"), nil, 0600)
	data, err := os.ReadFile(filepath.Join(state, "pipe-pid"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Error(err)
		return
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 1 {
		t.Errorf("invalid owned child PID %q", data)
		return
	}
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		if unix.Kill(pid, 0) == unix.ESRCH {
			if _, err := os.Stat(filepath.Join(state, "pipe-done")); err != nil {
				t.Error("owned child exited without release acknowledgement")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("owned pipe child %d exit unconfirmed", pid)
}

func TestCatchUpReloadSelectedCommand(t *testing.T) {
	if catchUpStatusReloadTimeout != 7500*time.Millisecond || catchUpReloadWaitDelay != 200*time.Millisecond {
		t.Fatal("production budget or cleanup limit changed")
	}
	for _, tc := range []struct {
		mode, outcome, reason                        string
		interrupts, runs, actualInterrupt, actualRun int
	}{
		{"initial-read", "failed", "budget expired; no pane control", 0, 0, 0, 0},
		{"interrupt-read", "failed", "budget expired; no command sent", 1, 0, 0, 0},
		{"restart-read", "failed", "budget expired; command delivered", 1, 1, 0, 0},
		{"interrupt-ack", "failed", "interrupt delivery unconfirmed", 1, 0, 1, 0},
		{"restart-ack", "failed", "restart delivery unconfirmed", 1, 1, 0, 1},
		{"mise", "failed", "pin inspection unconfirmed", 0, 0, 0, 0},
		{"pipe", "failed", "no pane control attempted", 0, 0, 0, 0},
		{"clean-eof", "restarted", "observed status process", 1, 1, 0, 0},
		{"stderr-success", "restarted", "observed status process", 1, 1, 0, 0},
		{"expired-write", "failed", "restart delivery unconfirmed", 1, 1, 0, 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c, state := reloadDeadlineFixture(t)
			t.Setenv("RELOAD_DEADLINE_BOUNDARY", tc.mode)
			if tc.mode == "pipe" {
				t.Cleanup(func() { reloadPipeCleanup(t, state) })
			}
			install := tc.mode == "mise"
			if install {
				fixtureFile(t, filepath.Join(c.Harness, ".wtc-cli-version"), "0.2.2\n", 0644)
				fixtureFile(t, filepath.Join(c.Harness, "wtc.toml"), "[compatibility]\nrequires = \">=0.2.0,<0.3\"\n", 0644)
				fixtureGit(t, "-C", c.Harness, "add", ".")
				fixtureGit(t, "-C", c.Harness, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic installed pin")
			}
			ctx, cancel := context.WithTimeout(context.Background(), catchUpStatusReloadTimeout)
			defer cancel()
			ops := catchUpReloadRealOps(ctx)
			actual := ops.run
			s := reloadScript{reads: map[string]int{}}
			selected := 0
			var selectedErr error
			ready, release, barrierDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			if tc.mode == "expired-write" {
				go func() {
					defer close(barrierDone)
					select {
					case <-ready:
						cancel()
					case <-ctx.Done():
					}
					close(release)
				}()
			}
			ops.run = func(ctx context.Context, name, dir string, args ...string) ([]byte, error) {
				key := reloadCommand(name, args)
				phase := s.phase()
				choose := tc.mode == "initial-read" && key == "pane process-info" && phase == "initial" || tc.mode == "interrupt-read" && key == "pane process-info" && phase == "interrupt" || tc.mode == "restart-read" && key == "pane process-info" && phase == "restart" || tc.mode == "interrupt-ack" && key == "pane send-keys" || (tc.mode == "restart-ack" || tc.mode == "expired-write") && key == "pane run" || tc.mode == "mise" && key == "mise where" || tc.mode == "pipe" && key == "pane process-info" || (tc.mode == "clean-eof" || tc.mode == "stderr-success") && key == "pane process-info" && phase == "restart"
				if !choose {
					return s.run(ctx, name, dir, args...)
				}
				selected++
				s.record(key)
				if tc.mode == "expired-write" {
					close(ready)
					<-release
				}
				data, err := actual(ctx, name, dir, args...)
				selectedErr = err
				return data, err
			}
			r := CatchUpReport{Initiator: c.Collection}
			start := time.Now()
			c.catchUpReloadStatusWithOps(&r, reloadTarget(c), CatchUpOptions{ReloadStatus: true, InstallTools: install}, ops)
			elapsed := time.Since(start)
			t.Logf("elapsed=%s selected_error=%v outcomes=%+v", elapsed, selectedErr, r.Outcomes)
			if elapsed > 8*time.Second {
				t.Errorf("real command budget exceeded: %s", elapsed)
			}
			reloadAssert(t, r, tc.outcome, tc.reason)
			if selected != 1 || s.interrupts != tc.interrupts || s.runs != tc.runs {
				t.Fatalf("selected=%d controls=%d/%d calls=%v", selected, s.interrupts, s.runs, s.calls)
			}
			for _, x := range []struct {
				name string
				want int
			}{{"actual-interrupt", tc.actualInterrupt}, {"actual-run", tc.actualRun}} {
				data, _ := os.ReadFile(filepath.Join(state, x.name))
				if len(data) != x.want {
					t.Errorf("%s bytes=%d want=%d", x.name, len(data), x.want)
				}
			}
			if tc.mode != "expired-write" {
				if _, err := os.Stat(filepath.Join(state, "entered")); err != nil {
					t.Fatal("selected subprocess did not reach the owned boundary")
				}
			}
			if tc.mode != "expired-write" {
				data, err := os.ReadFile(filepath.Join(state, "owned-pid"))
				pid, parseErr := strconv.Atoi(string(data))
				if err != nil || parseErr != nil || pid <= 1 {
					t.Fatalf("invalid owned child evidence: %q %v", data, err)
				}
				if err := unix.Kill(pid, 0); err != unix.ESRCH {
					t.Fatalf("owned command child was not reaped: %v", err)
				}
			}
			if tc.mode == "expired-write" {
				<-barrierDone
				if !errors.Is(selectedErr, context.Canceled) {
					t.Errorf("expired output error=%v", selectedErr)
				}
				if _, err := os.Stat(filepath.Join(state, "entered")); !os.IsNotExist(err) {
					t.Error("expired command started")
				}
			} else if tc.mode == "clean-eof" || tc.mode == "stderr-success" {
				if selectedErr != nil {
					t.Errorf("complete output rejected: %v", selectedErr)
				}
				if _, err := os.Stat(filepath.Join(state, "emitted")); err != nil {
					t.Error("real status reply was not emitted")
				}
			} else if tc.mode == "pipe" {
				if !errors.Is(selectedErr, exec.ErrWaitDelay) {
					t.Errorf("pipe boundary error=%v", selectedErr)
				}
			} else {
				if !errors.Is(selectedErr, context.DeadlineExceeded) {
					t.Errorf("stall boundary error=%v", selectedErr)
				}
			}
		})
	}
}

func TestCatchUpReloadLateOutput(t *testing.T) {
	_, state := reloadDeadlineFixture(t)
	t.Setenv("RELOAD_DEADLINE_BOUNDARY", "late-reply")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		if !reloadWaitFile(filepath.Join(state, "entered")) {
			done <- errors.New("child never ready")
			return
		}
		cancel()
		done <- os.WriteFile(filepath.Join(state, "release"), nil, 0600)
	}()
	// Deliberately unbound to prove that a successful reply after cancellation
	// is discarded even independently of CommandContext terminating the process.
	cmd := exec.Command("herdr", "--session", "synthetic", "pane", "process-info", "--pane", "w1:p3")
	data, err := catchUpReloadOutput(ctx, cmd)
	if barrierErr := <-done; barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if !errors.Is(err, context.Canceled) || len(data) != 0 {
		t.Fatalf("late reply accepted: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(state, "emitted")); err != nil {
		t.Fatal("late child did not emit its reply")
	}
	t.Setenv("RELOAD_DEADLINE_BOUNDARY", "reply")
	argv, err := catchUpForeground("synthetic", "w1:p3")
	if err != nil || !catchUpStatusProcess(argv) {
		t.Fatalf("ordinary reader changed: %v %v", argv, err)
	}
	start := time.Now()
	if err := catchUpReloadWait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if time.Since(start) >= time.Second {
		t.Fatal("canceled wait consumed cadence")
	}
}

func TestCatchUpReloadOutputStreams(t *testing.T) {
	for _, mode := range []string{"clean-eof", "stdout-large", "stderr-success", "exit-error"} {
		t.Run(mode, func(t *testing.T) {
			_, state := reloadDeadlineFixture(t)
			t.Setenv("RELOAD_DEADLINE_BOUNDARY", mode)
			ctx, cancel := context.WithTimeout(context.Background(), catchUpStatusReloadTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, "herdr", "--session", "synthetic", "pane", "process-info", "--pane", "w1:p3")
			start := time.Now()
			data, err := catchUpReloadOutput(ctx, cmd)
			if time.Since(start) > 8*time.Second {
				t.Fatal("stream collection exceeded original elapsed ceiling")
			}
			if mode == "exit-error" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 17 || len(exit.Stderr) == 0 || len(exit.Stderr) > 64<<10 {
					t.Fatalf("exit/stderr evidence: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var reply catchUpProcessInfo
			if json.Unmarshal(data, &reply) != nil || reply.Result.ProcessInfo.Group != 42 {
				t.Fatalf("complete stdout missing: %q", data)
			}
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				t.Fatal("owned child not reaped")
			}
			if _, err := os.Stat(filepath.Join(state, "emitted")); err != nil {
				t.Fatal("fixture did not emit output")
			}
		})
	}
}
