package wtc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeCatchUpPanes struct {
	busy       map[string]string
	foreground []string
	interrupts []string
	runs       []string
}

func (f *fakeCatchUpPanes) AgentBusy(collection string) (string, error) {
	if collection == "broken" {
		return "", errors.New("synthetic lookup failure")
	}
	return f.busy[collection], nil
}

func (f *fakeCatchUpPanes) StatusPane(context.Context, string) (string, bool, error) {
	return "block:status", false, nil
}

func (f *fakeCatchUpPanes) Foreground(context.Context, string) ([]string, error) {
	return f.foreground, nil
}

func (f *fakeCatchUpPanes) Interrupt(_ context.Context, pane string) error {
	f.interrupts = append(f.interrupts, pane)
	f.foreground = []string{"-zsh"}
	return nil
}

func (f *fakeCatchUpPanes) Run(_ context.Context, pane, command string) error {
	f.runs = append(f.runs, pane+" "+command)
	f.foreground = []string{"wtc", "status", "--tui"}
	return nil
}

func TestCatchUpIdleOnlyAsksTheCollectionsPanes(t *testing.T) {
	c := newWorkspaceFixture(t)
	for _, slug := range []string{"busy", "silent", "quiet", "broken"} {
		if _, err := c.NewCollection(NewOptions{Slug: slug}); err != nil {
			t.Fatal(err)
		}
	}
	panes := &fakeCatchUpPanes{busy: map[string]string{"busy": "working", "silent": "unknown"}}
	opt := CatchUpOptions{Collections: []string{"busy", "silent", "quiet", "broken"}, Repos: []string{"harness"}, IdleOnly: true, DryRun: true, NoSkills: true, NoMCP: true, NoSecrets: true, NoEnv: true, Panes: panes}
	report, _ := c.CatchUp(opt)
	outcomes := map[string]string{}
	for _, row := range report.Outcomes {
		if row.Kind == "repo" {
			outcomes[row.Collection] = row.Outcome + ": " + row.Reason
		}
	}
	for name, want := range map[string]string{"busy": "needs-owner: agent working", "silent": "needs-owner: agent unknown", "broken": "needs-owner: agent unknown: synthetic lookup failure"} {
		if !strings.HasPrefix(outcomes[name], want) {
			t.Fatalf("%s: got %q, want %q", name, outcomes[name], want)
		}
	}
	if strings.HasPrefix(outcomes["quiet"], "needs-owner") || outcomes["quiet"] == "" {
		t.Fatalf("idle collection was left alone: %v", outcomes)
	}
}

func TestCatchUpReloadsTheStatusPaneThroughTheCollectionsPanes(t *testing.T) {
	c := newWorkspaceFixture(t)
	panes := &fakeCatchUpPanes{foreground: []string{"wtc", "status", "--tui"}}
	start := time.Unix(1700000000, 0)
	now := start
	ctx := reloadLogicalContext{Context: context.Background(), deadline: start.Add(catchUpStatusReloadTimeout)}
	ops := catchUpReloadOps{ctx: ctx, now: func() time.Time { return now }, wait: func(context.Context) error { now = now.Add(time.Second); return nil }}
	ops.run = func(_ context.Context, name, _ string, args ...string) ([]byte, error) {
		t.Fatalf("reload ran %s %v instead of using the collection's panes", name, args)
		return nil, nil
	}
	report := CatchUpReport{Initiator: c.Collection}
	c.catchUpReloadStatusWithOps(&report, reloadTarget(c), CatchUpOptions{ReloadStatus: true, Panes: panes}, ops)
	reloadAssert(t, report, "restarted", "observed status process in block:status")
	if len(panes.interrupts) != 1 || len(panes.runs) != 1 || !strings.HasSuffix(panes.runs[0], "&& wtc status --tui") {
		t.Fatalf("status pane was not interrupted and restarted once: %q %q", panes.interrupts, panes.runs)
	}
}
