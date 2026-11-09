package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taba.sh/taba/internal/wtc"
)

func TestOpenCreatesWideWorkspaceWithoutStartingDisabledProcesses(t *testing.T) {
	c, root, bin, log, collection := openTestFixture(t)
	opt := openOptions{Session: "test", NoAgent: true, NoBrowse: true, NoStatus: true, Tools: []string{"browse", "shell"}}
	preview := openCollection(c, "sample", openOptions{Session: "test", DryRun: true}, "wide", true, true)
	if preview.Error != "" || !strings.Contains(strings.Join(preview.Actions, " "), "would create") {
		t.Fatalf("wrong preview: %+v", preview)
	}
	if _, err := os.Stat(os.Getenv("OPEN_TEST_STATE")); !os.IsNotExist(err) {
		t.Fatalf("dry run created workspace: %v", err)
	}
	opened := openCollection(c, "sample", opt, "wide", true, true)
	if opened.Error != "" || opened.Workspace != "ws1" || !strings.Contains(strings.Join(opened.Actions, " "), "workspace created") {
		t.Fatalf("open failed: %+v", opened)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	if strings.Count(calls, "pane split ") != 3 || !strings.Contains(calls, "workspace create --cwd "+collection) || !strings.Contains(calls, "--env WTC_COLLECTION='sample'") {
		t.Fatalf("incorrect workspace setup:\n%s", calls)
	}
	if !strings.Contains(calls, "--env WTC_TOOLCHAIN_PATH=") || !strings.Contains(calls, "--env TABA_TOOLCHAIN_PATH=") || !strings.Contains(calls, "--env WTC_AGENT_NAME=") || !strings.Contains(calls, "--env TABA_AGENT_NAME=") || strings.Contains(calls, "BASH_ENV=") {
		t.Fatalf("workspace depends on a shell entry point:\n%s", calls)
	}
	if strings.Contains(calls, "agent start") || strings.Contains(calls, "pane run") {
		t.Fatalf("disabled processes were started:\n%s", calls)
	}
	again := openCollection(c, "sample", opt, "wide", true, true)
	if again.Error != "" || !strings.Contains(strings.Join(again.Actions, " "), "already open") && !strings.Contains(strings.Join(again.Actions, " "), "skipped") {
		t.Fatalf("rerun did not reuse the workspace: %+v", again)
	}
	data, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "workspace create ") != 1 || strings.Count(string(data), "pane split ") != 3 {
		t.Fatalf("rerun changed a complete layout:\n%s", data)
	}
	narrowOpt := opt
	narrowOpt.LayoutSet = true
	switched := openCollection(c, "sample", narrowOpt, "narrow", true, true)
	if switched.Error != "" || !strings.Contains(strings.Join(switched.Actions, " "), "layout switched") {
		t.Fatalf("wide-to-narrow switch failed: %+v", switched)
	}
	state, err := os.ReadFile(os.Getenv("OPEN_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if agent, browse := openTestPane(t, state, "agent"), openTestPane(t, state, "browse"); agent.ID != "p1" || agent.TabLabel != "main" || browse.TabLabel != "tools" {
		t.Fatalf("switch did not preserve the agent and move browse: %s", state)
	}
	if status := openTestPane(t, state, "status"); status.TabLabel != "main" || status.X != 0 || status.Y != 1 || strings.Contains(string(state), "restack-tmp") {
		t.Fatalf("switch did not stack the status pane under the agent: %s", state)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "wide", true, true); listed.Layout != "narrow" {
		t.Fatalf("stacked tabs are not reported as narrow: %+v", listed)
	}
	sideBySide := strings.Replace(string(state), `"label": "status", "x": 0, "y": 1`, `"label": "status", "x": 80, "y": 0`, 1)
	if err := os.WriteFile(os.Getenv("OPEN_TEST_STATE"), []byte(sideBySide), 0644); err != nil {
		t.Fatal(err)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "wide", true, true); listed.Layout != "unstacked" || !strings.Contains(strings.Join(listed.Actions, " "), "unstacked (4 panes)") {
		t.Fatalf("side-by-side main tab is reported as narrow: %+v", listed)
	}
	if planned := openCollection(c, "sample", openOptions{Session: "test", DryRun: true}, "wide", true, true); !strings.Contains(strings.Join(planned.Actions, " "), "would layout unstacked → narrow") {
		t.Fatalf("automatic layout would not restack: %+v", planned)
	}
	restacked := openCollection(c, "sample", narrowOpt, "narrow", true, true)
	if restacked.Error != "" || !strings.Contains(strings.Join(restacked.Actions, " "), "layout restacked (narrow)") {
		t.Fatalf("side-by-side main tab was not restacked: %+v", restacked)
	}
	state, err = os.ReadFile(os.Getenv("OPEN_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if status := openTestPane(t, state, "status"); status.TabLabel != "main" || status.X != 0 || status.Y != 1 || strings.Contains(string(state), "restack-tmp") {
		t.Fatalf("restack did not stack the status pane under the agent: %s", state)
	}
	restackCalls, _ := os.ReadFile(log)
	if strings.Contains(string(restackCalls), "pane close ") || strings.Contains(string(restackCalls), "pane split p1 --direction down --ratio 0.65") {
		t.Fatalf("restack replaced a pane instead of moving it:\n%s", restackCalls)
	}
	if again := openCollection(c, "sample", narrowOpt, "narrow", true, true); again.Error != "" || strings.Contains(strings.Join(again.Actions, " "), "layout ") {
		t.Fatalf("stacked narrow layout was changed again: %+v", again)
	}
	t.Setenv("OPEN_TEST_FAIL_SNAPSHOT", "1")
	blind := openCollection(c, "sample", opt, "narrow", true, true)
	blindCalls, _ := os.ReadFile(log)
	if !strings.Contains(blind.Error, "synthetic snapshot failure") || strings.Count(string(blindCalls), "pane move ") != strings.Count(string(restackCalls), "pane move ") {
		t.Fatalf("unreadable geometry was treated as unstacked: %+v\n%s", blind, blindCalls)
	}
	t.Setenv("OPEN_TEST_FAIL_SNAPSHOT", "")
	t.Setenv("OPEN_TEST_EMPTY_SNAPSHOT", "1")
	blind = openCollection(c, "sample", opt, "narrow", true, true)
	blindCalls, _ = os.ReadFile(log)
	if !strings.Contains(blind.Error, "snapshot has no layout") || strings.Count(string(blindCalls), "pane move ") != strings.Count(string(restackCalls), "pane move ") {
		t.Fatalf("missing geometry was treated as unstacked: %+v\n%s", blind, blindCalls)
	}
	t.Setenv("OPEN_TEST_EMPTY_SNAPSHOT", "")
	shellBeside := strings.Replace(string(state), `"label": "shell", "x": 0, "y": 1`, `"label": "shell", "x": 80, "y": 0`, 1)
	if shellBeside == string(state) {
		t.Fatalf("fixture has no stacked shell to displace: %s", state)
	}
	if err := os.WriteFile(os.Getenv("OPEN_TEST_STATE"), []byte(shellBeside), 0644); err != nil {
		t.Fatal(err)
	}
	auto := openCollection(c, "sample", opt, "wide", true, true)
	state, err = os.ReadFile(os.Getenv("OPEN_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if auto.Error != "" || auto.Layout != "narrow" || !strings.Contains(strings.Join(auto.Actions, " "), "layout restacked (narrow)") || !openTestPane(t, state, "shell").at("tools", 0, 1) || strings.Contains(string(state), "restack-tmp") {
		t.Fatalf("automatic open did not restack the tools tab as narrow: %+v\n%s", auto, state)
	}
	if err := os.WriteFile(os.Getenv("OPEN_TEST_STATE"), []byte(sideBySide), 0644); err != nil {
		t.Fatal(err)
	}
	wideOpt := opt
	wideOpt.LayoutSet = true
	widened := openCollection(c, "sample", wideOpt, "wide", true, true)
	state, err = os.ReadFile(os.Getenv("OPEN_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if widened.Error != "" || !strings.Contains(strings.Join(widened.Actions, " "), "layout switched (wide)") || !openTestPane(t, state, "status").at(openTestPane(t, state, "agent").TabLabel, 80, 1) || strings.Contains(string(state), `"label": "tools"`) {
		t.Fatalf("unstacked workspace did not switch to wide: %+v\n%s", widened, state)
	}
	if listed := openCollection(c, "sample", openOptions{Session: "test", List: true}, "wide", true, true); listed.Layout != "wide" {
		t.Fatalf("switched workspace is not reported as wide: %+v", listed)
	}
	if err := os.Remove(os.Getenv("OPEN_TEST_STATE")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	narrow := openCollection(c, "sample", opt, "narrow", true, true)
	if narrow.Error != "" || narrow.Layout != "narrow" {
		t.Fatalf("narrow creation failed: %+v", narrow)
	}
	state, err = os.ReadFile(os.Getenv("OPEN_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	if openTestPane(t, state, "agent").TabLabel != "main" || openTestPane(t, state, "shell").TabLabel != "tools" || openTestPane(t, state, "browse").TabLabel != "tools" {
		t.Fatalf("narrow layout is incomplete: %s", state)
	}
	if err := os.WriteFile(filepath.Join(bin, "nvim"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	enabled := openOptions{Session: "test", Tools: []string{"browse", "shell"}}
	first := openCollection(c, "sample", enabled, "narrow", true, true)
	if first.Error != "" || !strings.Contains(strings.Join(first.Actions, " "), "agent started") || !strings.Contains(strings.Join(first.Actions, " "), "browse started") || !strings.Contains(strings.Join(first.Actions, " "), "status started") {
		t.Fatalf("normal start failed: %+v", first)
	}
	before, _ := os.ReadFile(log)
	if !strings.Contains(string(before), "wtc browse --here") || !strings.Contains(string(before), "wtc status --tui") {
		t.Fatalf("browse and status panes did not launch native commands: %s", before)
	}
	second := openCollection(c, "sample", enabled, "narrow", true, true)
	after, _ := os.ReadFile(log)
	if second.Error != "" || strings.Count(string(after), "agent start ") != strings.Count(string(before), "agent start ") || strings.Count(string(after), "pane run ") != strings.Count(string(before), "pane run ") {
		t.Fatalf("live panes were restarted: %+v\n%s", second, after)
	}
	liveSwitch := openCollection(c, "sample", openOptions{Session: "test", LayoutSet: true, Tools: []string{"browse", "shell"}}, "wide", true, true)
	liveState, _ := os.ReadFile(os.Getenv("OPEN_TEST_STATE"))
	liveCalls, _ := os.ReadFile(log)
	if liveSwitch.Error != "" || !openTestPane(t, liveState, "status").at("main", 80, 1) || strings.Contains(string(liveCalls), "pane close ") {
		t.Fatalf("layout switch did not preserve live status pane: %+v\n%s\n%s", liveSwitch, liveState, liveCalls)
	}
	stale := strings.Replace(string(state), `"panes":`, `"running": {}, "panes":`, 1)
	if err := os.WriteFile(os.Getenv("OPEN_TEST_STATE"), []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	recovered := openCollection(c, "sample", enabled, "narrow", true, true)
	afterRecovery, _ := os.ReadFile(log)
	if recovered.Error != "" || strings.Count(string(afterRecovery), "agent start ") != strings.Count(string(after), "agent start ")+1 || strings.Count(string(afterRecovery), "pane run ") != strings.Count(string(after), "pane run ")+2 {
		t.Fatalf("idle panes were not recovered: %+v\n%s", recovered, afterRecovery)
	}
	if err := os.Remove(os.Getenv("OPEN_TEST_STATE")); err != nil {
		t.Fatal(err)
	}
	if err := openAfterNew(c, collection, true, false); err != nil {
		t.Fatalf("new --open path failed: %v", err)
	}
	if err := os.Remove(os.Getenv("OPEN_TEST_STATE")); err != nil {
		t.Fatal(err)
	}
	if err := openAfterNew(c, collection, false, false); err != nil {
		t.Fatalf("new automatic open path failed: %v", err)
	}
	autoState, err := os.ReadFile(os.Getenv("OPEN_TEST_STATE"))
	if err != nil || !strings.Contains(string(autoState), `"created": true`) || !strings.Contains(string(autoState), `"running": {`) {
		t.Fatalf("new automatic open did not create and start panes: %s, %v", autoState, err)
	}
	if err := os.Remove(os.Getenv("OPEN_TEST_STATE")); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "session-running")
	t.Setenv("OPEN_TEST_SESSION_MARKER", marker)
	if err := openAfterNew(c, collection, true, false); err != nil {
		t.Fatalf("new --open with an absent session failed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("headless session was not started: %v", err)
	}
	if err := os.Remove(os.Getenv("OPEN_TEST_STATE")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPEN_TEST_FAIL_PANE_RUN", "1")
	if err := openAfterNew(c, collection, true, false); err == nil || !strings.Contains(err.Error(), "synthetic pane failure") {
		t.Fatalf("new --open concealed pane startup failure: %v", err)
	}
	t.Setenv("OPEN_TEST_FAIL_PANE_RUN", "")
	t.Setenv("OPEN_TEST_FAIL_PROCESS_INFO", "p1")
	if opened := openCollection(c, "sample", openOptions{Session: "test"}, "wide", true, true); !strings.Contains(opened.Error, "synthetic inspection failure") {
		t.Fatalf("open concealed process inspection failure: %+v", opened)
	}
	for _, preview := range []openOptions{{Session: "test", List: true}, {Session: "test", DryRun: true}} {
		if item := openCollection(c, "sample", preview, "wide", true, true); !strings.Contains(item.Error, "synthetic inspection failure") {
			t.Fatalf("read-only open concealed process inspection failure: %+v", item)
		}
	}
	t.Setenv("OPEN_TEST_FAIL_PROCESS_INFO", "")
	t.Setenv("OPEN_TEST_FAIL_SECOND_PANE_LIST", "1")
	if err := os.WriteFile(log, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if item := openCollection(c, "sample", openOptions{Session: "test", DryRun: true}, "wide", true, true); !strings.Contains(item.Error, "synthetic pane list failure") {
		t.Fatalf("dry run concealed pane listing failure: %+v", item)
	}
}

type openTestPaneState struct {
	ID, TabLabel string
	X, Y         int
}

func (p openTestPaneState) at(tab string, x, y int) bool {
	return p.TabLabel == tab && p.X == x && p.Y == y
}

// openTestPane reads one labelled pane, with its tab's label, from the herdr
// fixture's state.
func openTestPane(t *testing.T, state []byte, label string) openTestPaneState {
	t.Helper()
	var parsed struct {
		Panes []struct {
			ID    string `json:"pane_id"`
			Tab   string `json:"tab_id"`
			Label string `json:"label"`
			X     int    `json:"x"`
			Y     int    `json:"y"`
		} `json:"panes"`
		Tabs []struct {
			ID    string `json:"tab_id"`
			Label string `json:"label"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal(state, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, pane := range parsed.Panes {
		if pane.Label != label {
			continue
		}
		found := openTestPaneState{ID: pane.ID, X: pane.X, Y: pane.Y}
		for _, tab := range parsed.Tabs {
			if tab.ID == pane.Tab {
				found.TabLabel = tab.Label
			}
		}
		return found
	}
	return openTestPaneState{}
}

// openTestFixture builds a synthetic collection and a herdr stand-in that keeps
// workspace, tab and pane state in a JSON file.
func openTestFixture(t *testing.T) (c *wtc.Context, root, bin, log, collection string) {
	t.Helper()
	var err error
	root = t.TempDir()
	collection = filepath.Join(root, "sample")
	harness := filepath.Join(collection, "harness")
	if err := os.MkdirAll(harness, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte("repos:\n  - name: example\n    remote: https://github.com/example/example.git\n    default_ref: origin/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, ".env.collection"), []byte("# generated\nWTC_COLLECTION='sample'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(collection, "tools", "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness, "wtc.toml"), []byte("[agent_env]\nprepend_paths = [\"tools/bin\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(harness, "tools"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness, "tools", "agent-env.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	fixture := `#!/usr/bin/env python3
import json, os, sys
state_path = os.environ["OPEN_TEST_STATE"]
calls_path = os.environ["OPEN_TEST_CALLS"]
args = sys.argv[3:]
with open(calls_path, "a") as f: f.write(" ".join(args) + "\n")
marker = os.environ.get("OPEN_TEST_SESSION_MARKER")
if args == ["server"]:
    if marker: open(marker, "w").close()
    sys.exit(0)
if args == ["workspace", "list"] and marker and not os.path.exists(marker):
    sys.exit(1)
try:
    state = json.load(open(state_path))
except FileNotFoundError:
    state = {"created": False, "panes": []}
def save():
    with open(state_path, "w") as f: json.dump(state, f)
def emit(result): print(json.dumps({"result": result}))
def new_tab(label):
    state["next_tab"] = state.get("next_tab", len(state["tabs"])) + 1
    tab_id = "t" + str(state["next_tab"])
    state["tabs"].append({"tab_id":tab_id,"label":label})
    return tab_id
def beside(pane, target, direction):
    pane["tab_id"] = target["tab_id"]
    if direction == "right":
        pane["x"], pane["y"] = target.get("x", 0) + 80, target.get("y", 0)
    else:
        pane["x"], pane["y"] = target.get("x", 0), target.get("y", 0) + 1
if args == ["workspace", "list"]:
    emit({"workspaces": [{"label": "sample", "workspace_id": "ws1"}] if state["created"] else []})
elif args[:2] == ["workspace", "create"]:
    state["created"] = True
    state["panes"] = [{"pane_id":"p1","tab_id":"t1","label":"1"}]
    state["tabs"] = [{"tab_id":"t1","label":"1"}]
    save(); emit({"workspace_id":"ws1", "pane_id":"p1"})
elif args[:2] == ["pane", "list"]:
    if os.environ.get("OPEN_TEST_FAIL_SECOND_PANE_LIST"):
        count = sum(line.startswith("pane list ") for line in open(calls_path))
        if count >= 2:
            print("synthetic pane list failure", file=sys.stderr)
            sys.exit(2)
    emit({"panes": state["panes"]})
elif args[:2] == ["tab", "list"]:
    emit({"tabs":state["tabs"]})
elif args[:2] == ["tab", "rename"]:
    for tab in state["tabs"]:
        if tab["tab_id"] == args[2]: tab["label"] = args[3]
    save(); emit({})
elif args[:2] == ["tab", "create"]:
    tab_id = new_tab(args[args.index("--label")+1])
    new_id = "p" + str(len(state["panes"])+1)
    state["panes"].append({"pane_id":new_id,"tab_id":tab_id,"label":new_id})
    save(); emit({"pane_id":new_id})
elif args[:2] == ["pane", "split"]:
    new_id = "p" + str(len(state["panes"])+1)
    parent = next(p for p in state["panes"] if p["pane_id"] == args[2])
    pane = {"pane_id":new_id,"tab_id":parent["tab_id"],"label":new_id}
    beside(pane, parent, args[args.index("--direction")+1])
    state["panes"].append(pane)
    save(); emit({"pane_id":new_id})
elif args[:2] == ["pane", "get"]:
    emit({"pane":next(p for p in state["panes"] if p["pane_id"] == args[2])})
elif args[:2] == ["pane", "rename"]:
    for pane in state["panes"]:
        if pane["pane_id"] == args[2]: pane["label"] = args[3]
    save(); emit({})
elif args[:2] == ["pane", "move"]:
    pane = next(p for p in state["panes"] if p["pane_id"] == args[2])
    changed = True
    if "--new-tab" in args:
        pane["tab_id"], pane["x"], pane["y"] = new_tab(args[args.index("--label")+1]), 0, 0
    elif pane["tab_id"] == args[args.index("--tab")+1]:
        changed = False # herdr ignores a move within one tab
    else:
        target = next(p for p in state["panes"] if p["pane_id"] == args[args.index("--target-pane")+1])
        beside(pane, target, args[args.index("--split")+1])
    state["tabs"] = [t for t in state["tabs"] if any(p["tab_id"] == t["tab_id"] for p in state["panes"])]
    save(); emit({"move_result":{"changed":changed}})
elif args[:2] == ["pane", "close"]:
    state["panes"] = [p for p in state["panes"] if p["pane_id"] != args[2]]
    state["tabs"] = [t for t in state["tabs"] if any(p["tab_id"] == t["tab_id"] for p in state["panes"])]
    save(); emit({})
elif args[:2] == ["pane", "process-info"]:
    if os.environ.get("OPEN_TEST_FAIL_PROCESS_INFO") == args[args.index("--pane")+1]:
        print("synthetic inspection failure", file=sys.stderr)
        sys.exit(2)
    argv = state.get("running", {}).get(args[args.index("--pane")+1], ["-zsh"])
    emit({"process_info":{"foreground_process_group_id":1,"foreground_processes":[{"pid":1,"argv":argv}]}})
elif args[:2] == ["agent", "start"]:
    state.setdefault("running", {})[args[args.index("--pane")+1]] = ["claude"]
    save(); emit({})
elif args[:2] == ["pane", "run"]:
    if os.environ.get("OPEN_TEST_FAIL_PANE_RUN"):
        print("synthetic pane failure", file=sys.stderr)
        sys.exit(2)
    state.setdefault("running", {})[args[2]] = ["bash", args[3]]
    save(); emit({})
elif args[:2] == ["api", "snapshot"]:
    if os.environ.get("OPEN_TEST_FAIL_SNAPSHOT"):
        print("synthetic snapshot failure", file=sys.stderr)
        sys.exit(2)
    emit({"snapshot":{"layouts":[{"area":{"width":180},"tab_id":tab["tab_id"],"panes":[
        {"pane_id":pane["pane_id"],"rect":{"x":pane.get("x", 0),"y":pane.get("y", 0)}}
        for pane in state["panes"] if pane["tab_id"] == tab["tab_id"]]} for tab in state["tabs"] if not os.environ.get("OPEN_TEST_EMPTY_SNAPSHOT")]}})
else:
    print("unexpected command: " + " ".join(args), file=sys.stderr)
    sys.exit(2)
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(fixture), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPEN_TEST_STATE", filepath.Join(root, "state.json"))
	log = filepath.Join(root, "calls.log")
	t.Setenv("OPEN_TEST_CALLS", log)
	c, err = wtc.OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	return c, root, bin, log, collection
}

func TestOpenAgentNamesFitHerdrLimit(t *testing.T) {
	for _, session := range []string{"wtc", "123", strings.Repeat("s", 30), strings.Repeat("s", 40)} {
		name := openAgentName(session, "123-long-collection")
		if len(name) > 32 || name[0] < 'a' || name[0] > 'z' {
			t.Fatalf("invalid agent name %q", name)
		}
	}
	long := strings.Repeat("s", 40)
	if openAgentName(long, "first") == openAgentName(long, "second") {
		t.Fatal("long session name loses the collection identity")
	}
}

func TestOpenReadEnvironmentPreservesAssignments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.collection")
	content := "# generated\nWTC_COLLECTION='sample'\nTOKEN=abc=def\nSPACED=\"two words\"\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := openReadEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"WTC_COLLECTION='sample'", "TOKEN=abc=def", `SPACED="two words"`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("assignments changed: got %q, want %q", got, want)
	}
}

func TestOpenAgentStartsWithFirstPromptRecovery(t *testing.T) {
	root := t.TempDir()
	collection := filepath.Join(root, "sample")
	if err := os.MkdirAll(collection, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, "HANDOFF.md"), []byte("start here\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "calls.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$OPEN_TEST_CALLS"
case "$*" in
  *'agent prompt '*) echo agent_prompt_stalled >&2; exit 1 ;;
esac
printf '%s\n' '{"result":{}}'
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPEN_TEST_CALLS", log)
	c := &wtc.Context{Collection: collection, ConfigRoot: root}
	if err := openStartAgent(c, openOptions{Session: "test"}, "pane-1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	for _, want := range []string{
		"agent start test--sample --kind claude --pane pane-1 -- --dangerously-skip-permissions --remote-control test--sample",
		"agent prompt pane-1 /wtc-start --wait --until working --timeout 20000",
		"agent send-keys pane-1 enter",
		"agent wait pane-1 --until working --timeout 10000",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing %q in:\n%s", want, calls)
		}
	}
}
