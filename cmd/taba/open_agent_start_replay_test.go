package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Replays 128bbe8: herdr starts the agent and then reports it not ready. The
// start was sent and may have taken effect, so it must not be sent again.
// Before that fix, every failure was retried and the agent started 15 times.
func TestOpenAgentStartAfterNotReadyIsNotResent(t *testing.T) {
	c, _, bin, log, _ := openTestFixture(t)
	state := `{"created":true,"panes":[{"pane_id":"p1","tab_id":"t1","label":"agent"},{"pane_id":"p2","tab_id":"t1","label":"status","x":0,"y":1}],"tabs":[{"tab_id":"t1","label":"main"}]}`
	if err := os.WriteFile(os.Getenv("OPEN_TEST_STATE"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(bin, "herdr"), filepath.Join(bin, "herdr-base")); err != nil {
		t.Fatal(err)
	}
	notReady := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[3:]
if args[:2]!=["agent","start"]: os.execv(os.path.join(os.path.dirname(sys.argv[0]),"herdr-base"),sys.argv)
with open(os.environ["OPEN_TEST_CALLS"],"a") as f: f.write(" ".join(args)+"\n")
state=json.load(open(os.environ["OPEN_TEST_STATE"]))
state.setdefault("running",{})[args[args.index("--pane")+1]]=["claude"]
json.dump(state,open(os.environ["OPEN_TEST_STATE"],"w"))
print(json.dumps({"error":{"code":"agent_not_ready","message":"synthetic startup dialog"}}),file=sys.stderr)
sys.exit(1)
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(notReady), 0755); err != nil {
		t.Fatal(err)
	}
	opt := openOptions{Session: "test", NoBrowse: true, NoStatus: true, NoFirstPrompt: true}
	item := openCollection(c, "sample", opt, "narrow", true, true)
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(calls), "agent start "); n != 1 {
		t.Fatalf("agent started %d times, want 1; result: %+v", n, item)
	}
}
