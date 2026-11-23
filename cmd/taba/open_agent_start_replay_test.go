package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

// openAgentStartFixture is a collection whose herdr records each agent start,
// then fails it with reply. With launched, the agent appears in its pane.
func openAgentStartFixture(t *testing.T, reply string, launched bool) (c *wtc.Context, starts func() int) {
	t.Helper()
	c, _, bin, log, _ := openTestFixture(t)
	state := `{"created":true,"panes":[{"pane_id":"p1","tab_id":"t1","label":"agent"},{"pane_id":"p2","tab_id":"t1","label":"status","x":0,"y":1}],"tabs":[{"tab_id":"t1","label":"main"}]}`
	if err := os.WriteFile(os.Getenv("OPEN_TEST_STATE"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(bin, "herdr"), filepath.Join(bin, "herdr-base")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("START_REPLY", reply)
	t.Setenv("START_LAUNCHES", map[bool]string{true: "1"}[launched])
	failing := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[3:]
if args[:2]!=["agent","start"]: os.execv(os.path.join(os.path.dirname(sys.argv[0]),"herdr-base"),sys.argv)
with open(os.environ["OPEN_TEST_CALLS"],"a") as f: f.write(" ".join(args)+"\n")
if os.environ["START_LAUNCHES"]:
    state=json.load(open(os.environ["OPEN_TEST_STATE"]))
    state.setdefault("running",{})[args[args.index("--pane")+1]]=["claude"]
    json.dump(state,open(os.environ["OPEN_TEST_STATE"],"w"))
print(os.environ["START_REPLY"],file=sys.stderr)
sys.exit(1)
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(failing), 0755); err != nil {
		t.Fatal(err)
	}
	return c, func() int {
		calls, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(calls), "agent start ")
	}
}

// Replays 128bbe8: herdr starts the agent and then reports it not ready. The
// start was sent and may have taken effect, so it must not be sent again.
// Before that fix, every failure was retried and the agent started 15 times.
func TestOpenAgentStartAfterNotReadyIsNotResent(t *testing.T) {
	c, starts := openAgentStartFixture(t, `{"error":{"code":"agent_not_ready","message":"synthetic startup dialog"}}`, true)
	opt := openOptions{Session: "test", NoBrowse: true, NoStatus: true, NoFirstPrompt: true}
	item := openCollection(c, "sample", opt, "narrow", true, true)
	if n := starts(); n != 1 {
		t.Fatalf("agent started %d times, want 1; result: %+v", n, item)
	}
}

// A start whose reply was lost may still launch the agent later. A later open
// finds the pane empty, but must not send a second start until someone has
// looked at the pane and removed the marker.
func TestOpenAgentStartWithAnUnknownOutcomeIsNotResentByALaterOpen(t *testing.T) {
	c, starts := openAgentStartFixture(t, "synthetic transport failure", false)
	opt := openOptions{Session: "test", NoBrowse: true, NoStatus: true, NoFirstPrompt: true}
	if item := openCollection(c, "sample", opt, "narrow", true, true); !strings.Contains(item.Error, "transport failure") {
		t.Fatalf("first open: %+v", item)
	}
	marker := filepath.Join(c.Collection, ".wtc-agent-start.pending")
	item := openCollection(c, "sample", opt, "narrow", true, true)
	if n := starts(); n != 1 || !strings.Contains(item.Error, marker) {
		t.Fatalf("later open started %d times; result: %+v", starts(), item)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	openCollection(c, "sample", opt, "narrow", true, true)
	if n := starts(); n != 2 {
		t.Fatalf("open after the marker was removed started %d times in all, want 2", n)
	}
}
