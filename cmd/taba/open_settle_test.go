package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// countSettleSleeps replaces settle pacing with a counter for one test.
func countSettleSleeps(t *testing.T) *int {
	t.Helper()
	count := 0
	previous := openSettlePause
	openSettlePause = func(context.Context) error { count++; return nil }
	t.Cleanup(func() { openSettlePause = previous })
	return &count
}

// A new workspace settles its shells once, tool panes included. Each settle
// still needs three consecutive idle observations of every pane.
func TestOpenFreshWorkspaceSettlesToolPanesOnce(t *testing.T) {
	c, _, _, log, _ := openTestFixture(t)
	sleeps := countSettleSleeps(t)
	opt := openOptions{Session: "test", Tools: []string{"browse"}, NoAgent: true, NoStatus: true}
	item := openCollection(c, "sample", opt, "narrow", true, true)
	if item.Error != "" {
		t.Fatalf("open failed: %+v", item)
	}
	if *sleeps != 2 {
		t.Fatalf("fresh workspace paced %d settle intervals, want one settle (2)", *sleeps)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(calls)
	split := strings.Index(text, "pane split")
	browseSplit := strings.LastIndex(text, "pane split")
	if split < 0 || browseSplit < 0 {
		t.Fatalf("expected layout and tool splits:\n%s", text)
	}
	// The settle observes every pane, including the browse pane, after it exists.
	after := text[browseSplit:]
	for _, pane := range []string{"p1", "p2", "p3"} {
		if n := strings.Count(after, "pane process-info --pane "+pane+"\n"); n < 3 {
			t.Fatalf("pane %s observed %d times after the tool split, want at least 3:\n%s", pane, n, text)
		}
	}
}

// Adding a tool pane to an existing workspace keeps its own settle, and a
// settled existing workspace is not settled again.
func TestOpenExistingWorkspaceSettlesOnlyAddedPane(t *testing.T) {
	c, _, _, _, _ := openTestFixture(t)
	sleeps := countSettleSleeps(t)
	opt := openOptions{Session: "test", NoAgent: true, NoStatus: true}
	if item := openCollection(c, "sample", opt, "narrow", true, true); item.Error != "" {
		t.Fatalf("open failed: %+v", item)
	}
	*sleeps = 0
	opt.Tools = []string{"browse"}
	item := openCollection(c, "sample", opt, "narrow", true, true)
	if item.Error != "" {
		t.Fatalf("open failed: %+v", item)
	}
	if *sleeps != 2 {
		t.Fatalf("existing workspace paced %d settle intervals for one added pane, want 2", *sleeps)
	}
	*sleeps = 0
	if item := openCollection(c, "sample", opt, "narrow", true, true); item.Error != "" || *sleeps != 0 {
		t.Fatalf("already open workspace settled again (%d): %+v", *sleeps, item)
	}
}
