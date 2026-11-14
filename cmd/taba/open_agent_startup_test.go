package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the production collection path with a stateful executable, without
// opening terminals or answering startup dialogs.
func TestOpenPartialAgentStartup(t *testing.T) {
	for _, tc := range []struct {
		mode, want        string
		starts, prompts   int
		noAgent, noPrompt bool
	}{
		{mode: "blocked", want: "waiting for input", starts: 1},
		{mode: "ready", want: "now ready after interrupted startup", starts: 1},
		{mode: "working", want: "not ready (working)", starts: 1},
		{mode: "unknown", want: "not ready (unknown)", starts: 1},
		{mode: "wrong-pane", want: "identity could not be verified", starts: 1},
		{mode: "wrong-cwd", want: "identity could not be verified", starts: 1},
		{mode: "wrong-foreground", want: "identity could not be verified", starts: 1},
		{mode: "wrong-kind", want: "identity could not be verified", starts: 1},
		{mode: "wrong-name", want: "identity could not be verified", starts: 1},
		{mode: "wrong-workspace", want: "identity could not be verified", starts: 1},
		{mode: "changed", want: "identity could not be verified", starts: 1},
		{mode: "missing-identity", want: "identity could not be verified", starts: 1},
		{mode: "disappeared", want: "pane lookup failed", starts: 1},
		{mode: "lookup-failed", want: "identity lookup failed", starts: 1},
		{mode: "workspace-failed", want: "workspace lookup failed", starts: 1},
		{mode: "collision", want: "agent_name_taken", starts: 1},
		{mode: "missing-binary", want: "agent_start_failed", starts: 1},
		{mode: "transport", want: "transport failure", starts: 1},
		{mode: "text-code", want: "agent_not_ready", starts: 1},
		{mode: "malformed", want: "invalid response", starts: 1},
		{mode: "busy-once", starts: 2, prompts: 1},
		{mode: "busy-agent", want: "agent_pane_busy", starts: 1},
		{mode: "success", starts: 1, prompts: 1},
		{mode: "success", starts: 1, noPrompt: true},
		{mode: "blocked", noAgent: true},
	} {
		t.Run(tc.mode+map[bool]string{true: "-no-prompt"}[tc.noPrompt], func(t *testing.T) {
			c, _, bin, log, collection := openTestFixture(t)
			state := `{"created":true,"panes":[{"pane_id":"p1","tab_id":"t1","label":"agent"},{"pane_id":"p2","tab_id":"t1","label":"status","x":0,"y":1}],"tabs":[{"tab_id":"t1","label":"main"}]}`
			if err := os.WriteFile(os.Getenv("OPEN_TEST_STATE"), []byte(state), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(collection, "HANDOFF.md"), []byte("synthetic task"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(bin, "herdr"), filepath.Join(bin, "herdr-base")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("STARTUP_MODE", tc.mode)
			t.Setenv("STARTUP_CWD", collection)
			if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(openStartupHerdr), 0755); err != nil {
				t.Fatal(err)
			}
			opt := openOptions{Session: "test", NoBrowse: true, NoStatus: true, NoAgent: tc.noAgent, NoFirstPrompt: tc.noPrompt}
			item := openCollection(c, "sample", opt, "narrow", true, true)
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			text := string(calls)
			if n := strings.Count(text, "agent start "); n != tc.starts {
				t.Fatalf("started %d times; result: %+v; calls:\n%s", n, item, text)
			}
			if tc.want == "" && item.Error != "" || tc.want != "" && !strings.Contains(item.Error, tc.want) {
				t.Fatalf("unexpected result: %+v", item)
			}
			if n := strings.Count(text, "agent prompt "); n != tc.prompts {
				t.Fatalf("prompt count %d, want %d: %s", n, tc.prompts, text)
			}
			for _, forbidden := range []string{"send-keys", "pane run", "workspace focus", "config ", "update ", "trust"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("unexpected control %q: %s", forbidden, text)
				}
			}
			verified := tc.mode == "blocked" || tc.mode == "ready" || tc.mode == "working" || tc.mode == "unknown"
			if verified && !tc.noAgent && !strings.Contains(strings.Join(item.Actions, " "), "agent started;") {
				t.Fatalf("partial launch reported as failure: %+v", item)
			}
			if !tc.noAgent && (verified || tc.mode == "success") {
				// A normal rerun leaves the already-live pane alone, even while blocked.
				again := openCollection(c, "sample", opt, "narrow", true, true)
				after, _ := os.ReadFile(log)
				if !strings.Contains(strings.Join(again.Actions, " "), "agent live") || strings.Count(string(after), "agent start ") != tc.starts || strings.Count(string(after), "agent prompt ") != tc.prompts {
					t.Fatalf("rerun touched live agent: %+v\n%s", again, after)
				}
			}
		})
	}
}

const openStartupHerdr = `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[3:]
mode=os.environ["STARTUP_MODE"]
state_path=os.environ["OPEN_TEST_STATE"]
calls_path=os.environ["OPEN_TEST_CALLS"]
state=json.load(open(state_path))
def emit(x): print(json.dumps({"result":x}))
def fail(code,message="synthetic startup dialog"):
    print(json.dumps({"error":{"code":code,"message":message}}),file=sys.stderr)
    sys.exit(1)
intercept=args[0]=="agent" or mode=="workspace-failed" and args[:2]==["workspace","list"] and state.get("running")
if not intercept: os.execv(os.path.join(os.path.dirname(sys.argv[0]),"herdr-base"),sys.argv)
with open(calls_path,"a") as f: f.write(" ".join(args)+"\n")
if args[:2]==["agent","start"]:
    count=sum(line.startswith("agent start ") for line in open(calls_path))
    state["running"]={"p1":["claude"]}
    if mode=="busy-once" and count==1: state["running"]={}
    json.dump(state,open(state_path,"w"))
    if mode=="success" or mode=="busy-once" and count==2: emit({});sys.exit(0)
    if mode.startswith("busy-"): fail("agent_pane_busy")
    if mode=="collision": fail("agent_name_taken")
    if mode=="missing-binary": fail("agent_start_failed","synthetic executable missing")
    if mode in ["transport","text-code","malformed"]:
        print({"transport":"transport failure","text-code":"agent_not_ready","malformed":'{"error":{"code":"agent_not_ready" invalid response'}[mode],file=sys.stderr)
        sys.exit(1)
    if count>1: fail("agent_name_taken")
    fail("agent_not_ready")
elif args[:2]==["agent","get"]:
    if mode=="lookup-failed" or mode=="disappeared" and args[2]=="p1": fail("agent_not_found")
    a={"name":"test--sample","agent":"claude","pane_id":"p1","terminal_id":"term1","workspace_id":"ws1","cwd":os.environ["STARTUP_CWD"],"foreground_cwd":os.environ["STARTUP_CWD"],"agent_status":"blocked","interactive_ready":False,"launch_pending":False}
    for m,k,v in [("wrong-pane","pane_id","p9"),("wrong-cwd","cwd","/synthetic/other"),("wrong-foreground","foreground_cwd","/synthetic/other"),("wrong-kind","agent","codex"),("wrong-name","name","another"),("wrong-workspace","workspace_id","ws9"),("missing-identity","terminal_id","")]:
        if mode==m:a[k]=v
    if mode=="changed" and args[2]=="p1":a["terminal_id"]="replacement"
    if mode in ["ready","working","unknown"]:a["agent_status"]={"ready":"idle","working":"working","unknown":"unknown"}[mode]
    if mode=="ready":a["interactive_ready"]=True
    emit({"agent":a})
elif args[:2]==["workspace","list"]:fail("lookup_failed")
elif args[:2]==["agent","prompt"]:emit({})
else:fail("unexpected_control")
`
