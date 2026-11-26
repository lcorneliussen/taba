package main

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// rexTestAgents opens a Rex session per collection under "test" whose only
// pane runs fg and reports status.
func rexTestAgents(t *testing.T, panes map[string]rexTestBlock) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for collection, block := range panes {
		_, pane, err := newTerminalHost("rex", "test").CreateWorkspace(collection, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		state := rexTestRead(t)
		current := state.Blocks[pane]
		current.Label, current.Fg, current.Status, current.ChildPID, current.Interruptible = block.Label, block.Fg, block.Status, block.ChildPID, block.Interruptible
		state.Blocks[pane] = current
		rexTestWrite(t, state)
		ids[collection] = pane
	}
	return ids
}

func TestRexIdleOnlyLeavesWorkingAndUnreportedAgentsAlone(t *testing.T) {
	rexTestSetup(t, t.TempDir())
	rexTestAgents(t, map[string]rexTestBlock{
		"busy":     {Label: "agent", Fg: "claude", Status: "working"},
		"stuck":    {Label: "agent", Fg: "codex", Status: "blocked"},
		"silent":   {Label: "agent", Fg: "agy"},
		"quiet":    {Label: "agent", Fg: "claude", Status: "idle"},
		"finished": {Label: "agent", Fg: "claude", Status: "done"},
		"shell":    {Label: "agent", Fg: "-zsh"},
	})
	panes := rexCatchUpPanes{session: "test"}
	for collection, want := range map[string]string{"busy": "working", "stuck": "blocked", "silent": "unknown", "quiet": "", "finished": "", "shell": "", "missing": ""} {
		if got, err := panes.AgentBusy(collection); err != nil || got != want {
			t.Fatalf("%s: got %q, %v; want %q", collection, got, err, want)
		}
	}
	if got, err := (rexCatchUpPanes{session: "other"}).AgentBusy("busy"); err != nil || got != "" {
		t.Fatalf("another project's session counted: %q, %v", got, err)
	}
	t.Setenv("WTC_TEST_REX_BROKEN", "1")
	if got, err := panes.AgentBusy("busy"); err == nil {
		t.Fatalf("a failed agent lookup was not an error: %q", got)
	}
	t.Setenv("WTC_TEST_REX_BROKEN", "")
	t.Setenv("WTC_TEST_REX_DOWN", "1")
	if got, err := panes.AgentBusy("busy"); err != nil || got != "" {
		t.Fatalf("without a Rex server no agent can be busy: %q, %v", got, err)
	}
}

func TestRexCatchUpReachesTheStatusPane(t *testing.T) {
	rexTestSetup(t, t.TempDir())
	ids := rexTestAgents(t, map[string]rexTestBlock{
		"sample": {Label: "status", Fg: "wtc", Interruptible: true},
		"taken":  {Label: "status", Fg: "claude"},
		"plain":  {Label: "pane", Fg: "-zsh"},
	})
	panes, ctx := rexCatchUpPanes{session: "test"}, context.Background()
	pane, agent, err := panes.StatusPane(ctx, "sample")
	if err != nil || pane != ids["sample"] || agent {
		t.Fatalf("status pane: %q %v %v", pane, agent, err)
	}
	if _, agent, err := panes.StatusPane(ctx, "taken"); err != nil || !agent {
		t.Fatalf("an agent in the status pane was not reported: %v %v", agent, err)
	}
	for _, collection := range []string{"plain", "missing"} {
		if pane, _, err := panes.StatusPane(ctx, collection); err != nil || pane != "" {
			t.Fatalf("%s has no status pane: %q %v", collection, pane, err)
		}
	}
	if err := panes.Interrupt(ctx, pane); err != nil {
		t.Fatal(err)
	}
	if argv, err := panes.Foreground(ctx, pane); err != nil || !slices.Equal(argv, []string{"-zsh"}) {
		t.Fatalf("interrupted status pane is not back at its shell: %q %v", argv, err)
	}
	if err := panes.Run(ctx, pane, "cd '/x' && wtc status --tui"); err != nil {
		t.Fatal(err)
	}
	block := rexTestRead(t).Blocks[pane]
	if !slices.Equal(block.Keys, []string{"ctrl+c"}) || block.Ran != "cd '/x' && wtc status --tui" {
		t.Fatalf("status pane was not interrupted and restarted: %+v", block)
	}
	t.Setenv("WTC_TEST_REX_DOWN", "1")
	if _, _, err := panes.StatusPane(ctx, "sample"); err == nil || !strings.Contains(err.Error(), "no pane control attempted") {
		t.Fatalf("an unreachable server was not a failed lookup: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	t.Setenv("WTC_TEST_REX_DOWN", "")
	if _, err := panes.Foreground(cancelled, pane); err == nil {
		t.Fatal("a call outlived catch-up's deadline")
	}
}

func TestCatchUpAndProcsFollowTheMultiplexerSetting(t *testing.T) {
	c, _, bin, _, _ := openTestFixture(t)
	rexTestSetup(t, bin)
	t.Setenv("HARNESS_HERDR_SESSION", "test")
	t.Setenv("REX_BLOCK", "")
	t.Setenv("WTC_MULTIPLEXER", "herdr")
	if panes, err := catchUpPanes(c); err != nil || panes != nil {
		t.Fatalf("herdr catch-up must keep its own panes: %#v %v", panes, err)
	}
	t.Setenv("WTC_MULTIPLEXER", "zellij")
	if _, err := catchUpPanes(c); err == nil {
		t.Fatal("an unknown multiplexer was accepted")
	}
	t.Setenv("WTC_MULTIPLEXER", "rex")
	if panes, err := catchUpPanes(c); err != nil || panes != (rexCatchUpPanes{session: "test"}) {
		t.Fatalf("rex catch-up: %#v %v", panes, err)
	}

	shell := exec.Command("sleep", "30")
	other := exec.Command("sleep", "31")
	for _, process := range []*exec.Cmd{shell, other} {
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	}
	rexTestAgents(t, map[string]rexTestBlock{"sample": {Label: "agent", Fg: "-zsh", ChildPID: shell.Process.Pid}})
	if _, pane, err := newTerminalHost("rex", "elsewhere").CreateWorkspace("sample", t.TempDir(), nil); err != nil {
		t.Fatal(err)
	} else {
		state := rexTestRead(t)
		block := state.Blocks[pane]
		block.ChildPID = other.Process.Pid
		state.Blocks[pane] = block
		rexTestWrite(t, state)
	}
	processes, err := statusProcesses(c)
	if err != nil {
		t.Fatal(err)
	}
	pids := []int{}
	for _, process := range processes {
		pids = append(pids, process.PID)
	}
	if !slices.Equal(pids, []int{shell.Process.Pid}) {
		t.Fatalf("procs did not list exactly the collection panes' processes: %+v", processes)
	}
	t.Setenv("WTC_TEST_REX_DOWN", "1")
	if processes, err := statusProcesses(c); err != nil || len(processes) != 0 {
		t.Fatalf("without a Rex server there are no pane processes: %+v %v", processes, err)
	}
}

func TestRexSkipsAPaneThatClosedWhileListed(t *testing.T) {
	rexTestSetup(t, t.TempDir())
	host := newTerminalHost("rex", "test")
	ids := rexTestAgents(t, map[string]rexTestBlock{"sample": {Label: "agent", Fg: "claude", Status: "working", ChildPID: 4242}})
	for _, label := range []string{"status", "shell"} {
		pane, err := host.Split(ids["sample"], "right", "0.5", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		state := rexTestRead(t)
		block := state.Blocks[pane]
		block.Label, block.Gone = label, true
		state.Blocks[pane] = block
		rexTestWrite(t, state)
	}
	workspace, err := host.(*rexHost).workspace("sample")
	if err != nil {
		t.Fatal(err)
	}
	if panes, err := host.Panes(workspace); err != nil || len(panes) != 1 || panes[0].ID != ids["sample"] {
		t.Fatalf("panes: %+v %v", panes, err)
	}
	panes, ctx := rexCatchUpPanes{session: "test"}, context.Background()
	if busy, err := panes.AgentBusy("sample"); err != nil || busy != "working" {
		t.Fatalf("busy: %q %v", busy, err)
	}
	if pane, _, err := panes.StatusPane(ctx, "sample"); err != nil || pane != "" {
		t.Fatalf("a closed status pane: %q %v", pane, err)
	}
	if shells, err := host.(*rexHost).paneShells(); err != nil || len(shells) != 1 || !shells[4242] {
		t.Fatalf("shells: %v %v", shells, err)
	}
	t.Setenv("WTC_TEST_REX_BROKEN", "1")
	if _, err := panes.AgentBusy("sample"); err == nil {
		t.Fatal("a failed lookup was skipped like a closed pane")
	}
	if _, err := host.(*rexHost).paneShells(); err == nil {
		t.Fatal("a failed lookup was skipped like a closed pane")
	}
}

func TestStatusNextAsksTheCollectionsAgentInItsMultiplexer(t *testing.T) {
	c, _, bin, _, _ := openTestFixture(t)
	rexTestSetup(t, bin)
	t.Setenv("HARNESS_HERDR_SESSION", "test")
	t.Setenv("REX_BLOCK", "")
	t.Setenv("WTC_MULTIPLEXER", "rex")
	busy := statusNextDefaultRunner().busy
	ids := rexTestAgents(t, map[string]rexTestBlock{"sample": {Label: "agent", Fg: "claude", Status: "working"}})
	for status, want := range map[string]string{"working": "working", "blocked": "blocked", "": "unknown", "idle": "", "done": ""} {
		state := rexTestRead(t)
		block := state.Blocks[ids["sample"]]
		block.Status = status
		state.Blocks[ids["sample"]] = block
		rexTestWrite(t, state)
		if got, err := busy(c); err != nil || got != want {
			t.Fatalf("agent %q: got %q, %v; want %q", status, got, err, want)
		}
	}
	t.Setenv("WTC_TEST_REX_BROKEN", "1")
	if got, err := busy(c); err == nil {
		t.Fatalf("a failed agent lookup let the next action run: %q", got)
	}
}
